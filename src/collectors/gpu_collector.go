package collectors

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"inference-hub-v3/src/shared"
)

// GPUCollector collects GPU metrics from NVIDIA (nvidia-smi), AMD (rocm-smi),
// and Intel/generic GPUs (sysfs). It auto-detects the vendor on startup.
type GPUCollector struct {
	base          *BaseCollector
	http          *shared.HTTPClient
	vendor        string
	gpuCount      int
	specsCache    map[string]*GPUSpec
	vendorInfo    VendorInfo
	pcieInfoCache map[int]*shared.PCIEInfo
	pcieMu        sync.Mutex
}

func NewGPUCollector(base *BaseCollector, httpClient *shared.HTTPClient) *GPUCollector {
	c := &GPUCollector{
		base:          base,
		http:          httpClient,
		specsCache:    make(map[string]*GPUSpec),
		pcieInfoCache: make(map[int]*shared.PCIEInfo),
	}
	c.vendor = c.detectVendor()
	c.vendorInfo = getVendorInfo(c.vendor)
	c.gpuCount = c.detectGPUCount()
	shared.Infof("[gpu] vendor=%s count=%d brand=%s", c.vendor, c.gpuCount, c.vendorInfo.Brand)
	return c
}

// detectVendor identifies the GPU vendor from the kernel device identity first.
// The host deliberately exposes an AMD-compatible nvidia-smi shim, so command
// output cannot be treated as proof that an NVIDIA device exists.
func (c *GPUCollector) detectVendor() string {
	// Try sysfs DRM vendor IDs
	if dirs, err := os.ReadDir("/sys/class/drm/"); err == nil {
		for _, d := range dirs {
			if !strings.HasPrefix(d.Name(), "card") || strings.Contains(d.Name(), "-") {
				continue
			}
			vendorPath := filepath.Join("/sys/class/drm", d.Name(), "device", "vendor")
			data, err := os.ReadFile(vendorPath)
			if err != nil {
				continue
			}
			vendor := strings.TrimSpace(string(data))
			switch vendor {
			case "0x10de":
				return "nvidia"
			case "0x1002":
				return "amd"
			case "0x8086":
				return "intel"
			}
		}
	}
	// Try rocm-smi before nvidia-smi because an AMD compatibility shim may
	// intentionally return NVIDIA-shaped output on ROCm hosts.
	if out, err := exec.Command("sh", "-c", "rocm-smi --showproductname 2>/dev/null").Output(); err == nil {
		if strings.Contains(string(out), "Card:") {
			return "amd"
		}
	}
	if out, err := exec.Command("sh", "-c", "nvidia-smi --query-gpu=count --format=csv,noheader 2>/dev/null").Output(); err == nil {
		outStr := strings.TrimSpace(string(out))
		if outStr != "" && !strings.Contains(outStr, "has failed") {
			return "nvidia"
		}
	}
	return "unknown"
}

func (c *GPUCollector) detectGPUCount() int {
	switch c.vendor {
	case "nvidia":
		if out, err := exec.Command("sh", "-c", "nvidia-smi -L 2>/dev/null").Output(); err == nil {
			count := 0
			for _, line := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "GPU ") {
					count++
				}
			}
			if count > 0 {
				return count
			}
		}
		return 1
	case "amd":
		if out, err := exec.Command("sh", "-c", "rocm-smi --showproductname 2>/dev/null").Output(); err == nil {
			count := strings.Count(string(out), "Card:")
			if count > 0 {
				return count
			}
		}
		return 1
	default:
		// Count DRM cards (excluding connector suffixes like card0-DP-1)
		count := 0
		if dirs, err := os.ReadDir("/sys/class/drm/"); err == nil {
			seen := map[string]bool{}
			for _, d := range dirs {
				name := d.Name()
				if !strings.HasPrefix(name, "card") || strings.Contains(name, "-") {
					continue
				}
				// Extract base card name (e.g., "card0" from "card0")
				if seen[name] {
					continue
				}
				seen[name] = true
				// Check it has a device directory
				if _, err := os.Stat(filepath.Join("/sys/class/drm", name, "device")); err == nil {
					count++
				}
			}
		}
		if count == 0 {
			count = 1
		}
		return count
	}
}

func (c *GPUCollector) Collect(ctx context.Context) (interface{}, error) {
	var gpus []shared.GPUMetrics

	switch c.vendor {
	case "nvidia":
		gpus = c.collectNVIDIA()
	case "amd":
		gpus = c.collectAMD()
	case "intel":
		gpus = c.collectIntel()
	default:
		gpus = c.collectSysfs()
	}

	// Enrich with vendor info, specs, PCIe, processes
	for i := range gpus {
		g := &gpus[i]
		g.Vendor = c.vendor
		g.VendorDisplay = c.vendorInfo.Brand
		g.VendorColor = c.vendorInfo.Color
		g.EncoderName = c.vendorInfo.Encoder
		g.DecoderName = c.vendorInfo.Decoder

		// GPU specs lookup
		if spec := lookupGPUSpec(g.Name); spec != nil {
			g.CUDACores = spec.Cores
			g.CoreType = spec.CoreType
			g.BusWidth = spec.Bus
			g.MemType = spec.Mem
			g.TDPMin = float64(spec.TDPMin)
			g.TDPMax = float64(spec.TDPMax)
			g.Arch = spec.Arch
		} else {
			g.CoreType = c.vendorInfo.Brand
		}

		// PCIe info via sysfs
		g.PCIE = c.getPCIEInfo(g.Index, g.PCIBusID)

		// GPU processes
		g.Processes = c.collectProcesses(g.Index)

		// Fill missing power_limit from TDPMax
		if g.PowerLimit == 0 && g.TDPMax > 0 {
			g.PowerLimit = g.TDPMax
			if g.PowerLimit > 0 {
				g.PowerPct = g.PowerDraw / g.PowerLimit * 100
			}
		}
	}

	agg := c.aggregate(gpus)
	result := shared.GPUAggregate{GPUs: gpus, Aggregate: agg}

	// Write metrics to VictoriaMetrics
	ts := time.Now()
	if agg != nil {
		c.writeAggregateMetrics(agg, ts)
	}
	for _, g := range gpus {
		labels := map[string]string{
			"gpu_id":   strconv.Itoa(g.Index),
			"gpu_name": g.Name,
			"vendor":   c.vendor,
		}
		c.writeMetricDirect("gpu_util", g.Util, labels, ts)
		c.writeMetricDirect("gpu_mem_used", g.MemUsed, labels, ts)
		c.writeMetricDirect("gpu_mem_total", g.MemTotal, labels, ts)
		c.writeMetricDirect("gpu_mem_free", g.MemFree, labels, ts)
		c.writeMetricDirect("gpu_mem_util_pct", g.MemUtilPct, labels, ts)
		c.writeMetricDirect("gpu_temp", g.Temp, labels, ts)
		c.writeMetricDirect("gpu_power_draw", g.PowerDraw, labels, ts)
		c.writeMetricDirect("gpu_power_pct", g.PowerPct, labels, ts)
		c.writeMetricDirect("gpu_clock", g.Clock, labels, ts)
		c.writeMetricDirect("gpu_clock_max", g.ClockMax, labels, ts)
		c.writeMetricDirect("gpu_fan_speed", shared.Float64Value(g.FanSpeed), labels, ts)
	}

	return result, nil
}

// ==================== NVIDIA Collection ====================

func (c *GPUCollector) collectNVIDIA() []shared.GPUMetrics {
	fields := "name,utilization.gpu,utilization.memory,utilization.encoder,utilization.decoder," +
		"memory.used,memory.total,memory.free,temperature.gpu," +
		"power.draw,power.limit,fan.speed," +
		"clocks.current.graphics,clocks.max.graphics," +
		"driver_version,pcie.link.gen.current,pcie.link.width.current,pcie.link.gen.max,pcie.link.width.max"

	cmd := exec.Command("sh", "-c",
		"nvidia-smi --query-gpu="+fields+" --format=csv,noheader,nounits 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var gpus []shared.GPUMetrics
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	for i, line := range lines {
		vals := strings.Split(line, ",")
		if len(vals) < 16 {
			continue
		}

		memUsed := parseFloat64(vals[5])
		memTotal := parseFloat64(vals[6])
		powerDraw := parseFloat64(vals[9])
		powerLimit := parseFloat64(vals[10])

		gpu := shared.GPUMetrics{
			Index:      i,
			Name:       strings.TrimSpace(vals[0]),
			Util:       parseFloat64(vals[1]),
			MemUtil:    parseFloat64(vals[2]),
			EncUtil:    parseFloat64(vals[3]),
			DecUtil:    parseFloat64(vals[4]),
			MemUsed:    memUsed,
			MemTotal:   memTotal,
			MemFree:    parseFloat64(vals[7]),
			MemUtilPct: safeDiv(memUsed, memTotal) * 100,
			Temp:       parseFloat64(vals[8]),
			PowerDraw:  powerDraw,
			PowerLimit: powerLimit,
			PowerPct:   safeDiv(powerDraw, powerLimit) * 100,
			FanSpeed:   shared.Float64Ptr(parseFloat64(vals[11])),
			Clock:      parseFloat64(vals[12]),
			ClockMax:   parseFloat64(vals[13]),
			Driver:     strings.TrimSpace(vals[14]),
			PCIE: shared.PCIEInfo{
				CurrentGen:   strings.TrimSpace(vals[15]),
				CurrentWidth: strings.TrimSpace(vals[16]),
				Gen:          strings.TrimSpace(vals[17]),
				Width:        strings.TrimSpace(vals[18]),
			},
		}

		// Get PCI bus ID for sysfs cross-reference
		gpu.PCIBusID = c.getNvidiaPCIBusID(i)
		gpus = append(gpus, gpu)
	}
	return gpus
}

func (c *GPUCollector) getNvidiaPCIBusID(index int) string {
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("nvidia-smi -i %d --query-gpu=pci.bus_id --format=csv,noheader 2>/dev/null", index))
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(out))
	parts := strings.Split(raw, ":")
	if len(parts) >= 3 {
		return "0000:" + parts[len(parts)-2] + ":" + parts[len(parts)-1]
	}
	return raw
}

// ==================== AMD Collection ====================

func (c *GPUCollector) collectAMD() []shared.GPUMetrics {
	var gpus []shared.GPUMetrics

	// Try rocm-smi first
	cmd := exec.Command("sh", "-c", "rocm-smi --showallinfo 2>/dev/null")
	out, err := cmd.Output()
	if err == nil && len(out) > 0 {
		gpu := c.parseRocmSMI(string(out))
		if gpu != nil {
			gpu.Index = 0
			gpu.Name = c.getAMDGPUName()
			gpu.Driver = c.getAMDDriverVersion()
			gpus = append(gpus, *gpu)
			return gpus
		}
	}

	// Fallback to sysfs
	return c.collectSysfs()
}

func (c *GPUCollector) parseRocmSMI(output string) *shared.GPUMetrics {
	gpu := &shared.GPUMetrics{}

	// GPU utilization
	if m := regexp.MustCompile(`GPU use \(%\)\s*:\s*(\d+)`).FindStringSubmatch(output); len(m) > 1 {
		gpu.Util = parseFloat64(m[1])
	}

	// Memory
	if mu := regexp.MustCompile(`Memory used \(bytes\)\s*:\s*(\d+)`).FindStringSubmatch(output); len(mu) > 1 {
		if mt := regexp.MustCompile(`Memory total \(bytes\)\s*:\s*(\d+)`).FindStringSubmatch(output); len(mt) > 1 {
			usedBytes, _ := strconv.ParseUint(mu[1], 10, 64)
			totalBytes, _ := strconv.ParseUint(mt[1], 10, 64)
			gpu.MemUsed = float64(usedBytes / (1024 * 1024))
			gpu.MemTotal = float64(totalBytes / (1024 * 1024))
			gpu.MemFree = gpu.MemTotal - gpu.MemUsed
			gpu.MemUtilPct = safeDiv(gpu.MemUsed, gpu.MemTotal) * 100
			gpu.MemUtil = gpu.MemUtilPct
		}
	}

	// Temperature (edge sensor)
	if m := regexp.MustCompile(`Temperature \(Sensor edge\) \(C\)\s*:\s*([\d.]+)`).FindStringSubmatch(output); len(m) > 1 {
		gpu.Temp = parseFloat64(m[1])
	}

	// Power
	if m := regexp.MustCompile(`Average Graphics Package Power \(W\)\s*:\s*([\d.]+)`).FindStringSubmatch(output); len(m) > 1 {
		gpu.PowerDraw = parseFloat64(m[1])
	}
	if m := regexp.MustCompile(`Power limit \(W\)\s*:\s*([\d.]+)`).FindStringSubmatch(output); len(m) > 1 {
		gpu.PowerLimit = parseFloat64(m[1])
	}
	gpu.PowerPct = safeDiv(gpu.PowerDraw, gpu.PowerLimit) * 100

	// Clock
	if m := regexp.MustCompile(`GPU Clock \(Mhz\)\s*:\s*(\d+)`).FindStringSubmatch(output); len(m) > 1 {
		gpu.Clock = parseFloat64(m[1])
	}

	return gpu
}

func (c *GPUCollector) getAMDGPUName() string {
	cmd := exec.Command("sh", "-c", "rocm-smi --showproductname 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return "AMD GPU"
	}
	if m := regexp.MustCompile(`Card series\s*:\s*(.+)`).FindStringSubmatch(string(out)); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return "AMD GPU"
}

func (c *GPUCollector) getAMDDriverVersion() string {
	cmd := exec.Command("sh", "-c", "cat /sys/module/amdgpu/version 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return "amdgpu"
	}
	v := strings.TrimSpace(string(out))
	if v != "" {
		return v
	}
	return "amdgpu"
}

// ==================== Intel Collection ====================

func (c *GPUCollector) collectIntel() []shared.GPUMetrics {
	return c.collectSysfs()
}

// ==================== Sysfs Collection (AMD/Intel/generic) ====================

func (c *GPUCollector) collectSysfs() []shared.GPUMetrics {
	var gpus []shared.GPUMetrics

	dirs, err := os.ReadDir("/sys/class/drm/")
	if err != nil {
		return gpus
	}

	processed := map[string]bool{}
	gpuIndex := 0

	for _, d := range dirs {
		name := d.Name()
		if !strings.HasPrefix(name, "card") || strings.Contains(name, "-") {
			continue
		}
		if processed[name] {
			continue
		}
		processed[name] = true

		deviceDir := filepath.Join("/sys/class/drm", name, "device")
		if _, err := os.Stat(deviceDir); err != nil {
			continue
		}

		gpu := shared.GPUMetrics{
			Index:    gpuIndex,
			Name:     c.getSysfsGPUName(deviceDir),
			Vendor:   c.vendor,
			FanSpeed: nil, // *float64, nil means not available
		}

		// Temperature via hwmon
		if tempFiles, err := filepath.Glob(filepath.Join(deviceDir, "hwmon", "hwmon*", "temp1_input")); err == nil {
			for _, tf := range tempFiles {
				data, err := os.ReadFile(tf)
				if err == nil {
					if v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
						gpu.Temp = v / 1000.0
						break
					}
				}
			}
		}

		// Memory (VRAM)
		for _, pair := range [][2]string{
			{"mem_info_vram_total", "MemTotal"},
			{"mem_info_vram_used", "MemUsed"},
		} {
			p := filepath.Join(deviceDir, pair[0])
			if data, err := os.ReadFile(p); err == nil {
				if v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
					mb := float64(v / (1024 * 1024))
					switch pair[1] {
					case "MemTotal":
						gpu.MemTotal = mb
					case "MemUsed":
						gpu.MemUsed = mb
					}
				}
			}
		}
		if gpu.MemTotal > 0 {
			gpu.MemFree = gpu.MemTotal - gpu.MemUsed
			gpu.MemUtilPct = safeDiv(gpu.MemUsed, gpu.MemTotal) * 100
			gpu.MemUtil = gpu.MemUtilPct
		}

		// Power via hwmon
		if powerCapFiles, err := filepath.Glob(filepath.Join(deviceDir, "hwmon", "hwmon*", "power1_cap")); err == nil {
			for _, pf := range powerCapFiles {
				data, err := os.ReadFile(pf)
				if err == nil {
					if v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
						gpu.PowerLimit = float64(v) / 1000000.0
						break
					}
				}
			}
		}
		if powerAvgFiles, err := filepath.Glob(filepath.Join(deviceDir, "hwmon", "hwmon*", "power1_average")); err == nil {
			for _, pf := range powerAvgFiles {
				data, err := os.ReadFile(pf)
				if err == nil {
					if v, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
						gpu.PowerDraw = float64(v) / 1000000.0
						break
					}
				}
			}
		}
		gpu.PowerPct = safeDiv(gpu.PowerDraw, gpu.PowerLimit) * 100

		// PCI bus ID from uevent
		if ueventData, err := os.ReadFile(filepath.Join(deviceDir, "uevent")); err == nil {
			for _, line := range strings.Split(string(ueventData), "\n") {
				if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
					gpu.PCIBusID = strings.TrimSpace(strings.TrimPrefix(line, "PCI_SLOT_NAME="))
					break
				}
			}
		}

		gpu.Driver = c.vendor
		gpus = append(gpus, gpu)
		gpuIndex++
	}
	return gpus
}

func (c *GPUCollector) getSysfsGPUName(deviceDir string) string {
	// Try to get card name from modalias or uevent
	if data, err := os.ReadFile(filepath.Join(deviceDir, "uevent")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PCI_ID=") {
				// Could map PCI ID to name, but for now return generic
				break
			}
		}
	}

	// For AMD, try rocm-smi
	if c.vendor == "amd" {
		return c.getAMDGPUName()
	}

	// For Intel, try a generic name
	if c.vendor == "intel" {
		return "Intel GPU"
	}

	return "GPU"
}

// ==================== PCIe Info ====================

func (c *GPUCollector) getPCIEInfo(index int, pciBusID string) shared.PCIEInfo {
	c.pcieMu.Lock()
	defer c.pcieMu.Unlock()

	if cached, ok := c.pcieInfoCache[index]; ok {
		return *cached
	}

	info := &shared.PCIEInfo{
		Bus:          "N/A",
		CurrentGen:   "N/A",
		CurrentWidth: "N/A",
		Gen:          "N/A",
		Width:        "N/A",
	}

	// Find the DRM card matching this PCI bus ID
	targetCard := ""
	if pciBusID != "" {
		normalized := strings.ReplaceAll(pciBusID, "0000:", "")
		normalized = strings.ToLower(normalized)
		if dirs, err := os.ReadDir("/sys/class/drm/"); err == nil {
			for _, d := range dirs {
				name := d.Name()
				if !strings.HasPrefix(name, "card") || strings.Contains(name, "-") {
					continue
				}
				ueventPath := filepath.Join("/sys/class/drm", name, "device", "uevent")
				if data, err := os.ReadFile(ueventPath); err == nil {
					for _, line := range strings.Split(string(data), "\n") {
						if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
							bus := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "PCI_SLOT_NAME=")))
							bus = strings.ReplaceAll(bus, "0000:", "")
							if bus == normalized {
								targetCard = name
								break
							}
						}
					}
				}
				if targetCard != "" {
					break
				}
			}
		}
	}

	// Fallback: find card by index
	if targetCard == "" {
		if dirs, err := os.ReadDir("/sys/class/drm/"); err == nil {
			for _, d := range dirs {
				name := d.Name()
				if name == fmt.Sprintf("card%d", index) && !strings.Contains(name, "-") {
					targetCard = name
					break
				}
			}
		}
	}

	if targetCard == "" {
		return *info
	}

	deviceDir := filepath.Join("/sys/class/drm", targetCard, "device")

	// Read PCI_SLOT_NAME
	if data, err := os.ReadFile(filepath.Join(deviceDir, "uevent")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
				info.Bus = strings.TrimSpace(strings.TrimPrefix(line, "PCI_SLOT_NAME="))
				break
			}
		}
	}

	// Read link speed/width
	readFile := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(data))
	}

	maxGenRaw := readFile(filepath.Join(deviceDir, "max_link_speed"))
	maxWidthRaw := readFile(filepath.Join(deviceDir, "max_link_width"))
	curGenRaw := readFile(filepath.Join(deviceDir, "current_link_speed"))
	curWidthRaw := readFile(filepath.Join(deviceDir, "current_link_width"))

	if maxGenRaw != "" {
		info.Gen = maxGenRaw
	}
	if maxWidthRaw != "" {
		info.Width = maxWidthRaw + "x"
	}
	if curGenRaw != "" {
		info.CurrentGen = curGenRaw
	}
	if curWidthRaw != "" {
		info.CurrentWidth = curWidthRaw + "x"
	}

	c.pcieInfoCache[index] = info
	return *info
}

// ==================== GPU Processes ====================

func (c *GPUCollector) collectProcesses(gpuIdx int) []shared.GPUProcess {
	switch c.vendor {
	case "nvidia":
		return c.collectNVIDIAProcesses(gpuIdx)
	case "amd":
		return c.collectSysfsProcesses(gpuIdx)
	default:
		return c.collectSysfsProcesses(gpuIdx)
	}
}

func (c *GPUCollector) collectNVIDIAProcesses(gpuIdx int) []shared.GPUProcess {
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("nvidia-smi --query-compute-apps=pid,name,used_memory --format=csv,noheader,nounits -i %d 2>/dev/null", gpuIdx))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var procs []shared.GPUProcess
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 3 {
			mem, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
			procs = append(procs, shared.GPUProcess{
				PID:  strings.TrimSpace(parts[0]),
				Name: strings.TrimSpace(parts[1]),
				Mem:  mem,
			})
		}
	}
	return procs
}

func (c *GPUCollector) collectSysfsProcesses(gpuIdx int) []shared.GPUProcess {
	// For AMD/Intel, processes are harder to get from sysfs
	// Try nvidia-smi fallback (in case of hybrid setups)
	if c.vendor != "nvidia" {
		return nil
	}
	return nil
}

// ==================== Helpers ====================

func (c *GPUCollector) aggregate(gpus []shared.GPUMetrics) *shared.GPUMetrics {
	if len(gpus) == 0 {
		return nil
	}

	var agg shared.GPUMetrics
	agg.Name = fmt.Sprintf("Aggregate (%d GPUs)", len(gpus))
	agg.Index = -1

	for _, g := range gpus {
		agg.Util += g.Util
		agg.MemUsed += g.MemUsed
		agg.MemTotal += g.MemTotal
		agg.MemFree += g.MemFree
		if g.Temp > agg.Temp {
			agg.Temp = g.Temp
		}
		agg.PowerDraw += g.PowerDraw
		agg.PowerLimit += g.PowerLimit
		agg.Clock += g.Clock
		if g.ClockMax > agg.ClockMax {
			agg.ClockMax = g.ClockMax
		}
		if agg.Driver == "" {
			agg.Driver = g.Driver
		}
		if agg.FanSpeed == nil && g.FanSpeed != nil {
			agg.FanSpeed = g.FanSpeed
		}
	}

	n := float64(len(gpus))
	agg.Util /= n
	agg.MemUtilPct = safeDiv(agg.MemUsed, agg.MemTotal) * 100
	agg.Clock /= n
	agg.PowerPct = safeDiv(agg.PowerDraw, agg.PowerLimit) * 100

	return &agg
}

func (c *GPUCollector) writeAggregateMetrics(agg *shared.GPUMetrics, ts time.Time) {
	labels := map[string]string{"gpu": "aggregate", "vendor": c.vendor}
	c.writeMetricDirect("gpu_util", agg.Util, labels, ts)
	c.writeMetricDirect("gpu_mem_used", agg.MemUsed, labels, ts)
	c.writeMetricDirect("gpu_mem_total", agg.MemTotal, labels, ts)
	c.writeMetricDirect("gpu_mem_free", agg.MemFree, labels, ts)
	c.writeMetricDirect("gpu_mem_util_pct", agg.MemUtilPct, labels, ts)
	c.writeMetricDirect("gpu_temp", agg.Temp, labels, ts)
	c.writeMetricDirect("gpu_power_draw", agg.PowerDraw, labels, ts)
	c.writeMetricDirect("gpu_power_limit", agg.PowerLimit, labels, ts)
	c.writeMetricDirect("gpu_clock", agg.Clock, labels, ts)
	c.writeMetricDirect("gpu_clock_max", agg.ClockMax, labels, ts)
}

func (c *GPUCollector) writeMetricDirect(name string, value float64, labels map[string]string, ts time.Time) {
	if c.base != nil && c.base.store != nil {
		c.base.store.Write(name, value, labels, ts)
	}
}

// getGPUProcessesLegacy is kept for backward compatibility
func getGPUProcessesLegacy(vendor string, index int) []shared.GPUProcess {
	if vendor == "nvidia" {
		cmd := exec.Command("sh", "-c",
			fmt.Sprintf("nvidia-smi --query-compute-apps=pid,name,used_memory --format=csv,noheader,nounits -i %d 2>/dev/null", index))
		out, err := cmd.Output()
		if err != nil {
			return nil
		}
		var procs []shared.GPUProcess
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			parts := strings.Split(line, ",")
			if len(parts) >= 3 {
				mem, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
				procs = append(procs, shared.GPUProcess{
					PID:  strings.TrimSpace(parts[0]),
					Name: strings.TrimSpace(parts[1]),
					Mem:  mem,
				})
			}
		}
		return procs
	}
	return nil
}

func parseFloat64(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" || s == "[Not Supported]" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

// parseProcesses is kept for backward compatibility with gateway/main.go
func (c *GPUCollector) parseProcesses(gpuIdx int) []shared.GPUProcess {
	return c.collectProcesses(gpuIdx)
}

// ReadFile is a helper to read a file and return its contents as string
func ReadFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// readLines reads a file and returns its lines
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// Suppress unused import warnings
var _ = json.Marshal
var _ = runtime.NumCPU
