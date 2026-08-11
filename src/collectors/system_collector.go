package collectors

import (
	"context"
	"math"
	stdnet "net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"

	"inference-hub-v3/src/shared"
)

type SystemCollector struct {
	base          *BaseCollector
	rateMu        sync.Mutex
	previousAt    time.Time
	previousRead  uint64
	previousWrite uint64
	previousIOms  uint64
	previousRecv  uint64
	previousSent  uint64
}

func NewSystemCollector(base *BaseCollector) *SystemCollector {
	return &SystemCollector{base: base}
}

func (c *SystemCollector) Collect(ctx context.Context) (interface{}, error) {
	ts := time.Now()
	result := shared.SystemMetrics{}

	// CPU
	cpuPcts, err := cpu.PercentWithContext(ctx, 0, false)
	if err == nil && len(cpuPcts) > 0 {
		result.CPUUtil = cpuPcts[0]
	}
	perCPU, _ := cpu.PercentWithContext(ctx, 0, true)
	result.CPUPerCore = perCPU
	result.CPULogical = runtime.NumCPU()

	// CPU model from /proc/cpuinfo
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") {
				result.CPUModel = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			}
			if strings.HasPrefix(line, "cpu MHz") {
				if v, e := strconv.ParseFloat(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), 64); e == nil {
					result.CPUFreqCur = v
				}
			}
			if strings.HasPrefix(line, "flags") {
				flags := strings.SplitN(line, ":", 2)[1]
				if strings.Contains(flags, "svm") {
					result.CPUVirt = "AMD-V"
				}
				if strings.Contains(flags, "vmx") {
					result.CPUVirt = "Intel VT-x"
				}
			}
		}
	}
	if data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cpufreq/cpuinfo_max_freq"); err == nil {
		if khz, parseErr := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); parseErr == nil {
			result.CPUFreqMax = khz / 1000.0
		}
	}
	// CPU cache from lscpu
	if out, err := exec.Command("lscpu").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "L2 cache:") {
				result.CPUL2 = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			}
			if strings.Contains(line, "L3 cache:") {
				result.CPUL3 = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			}
		}
	}
	// CPU temp from thermal zone
	if data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
		if temp, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
			result.CPUTemp = temp / 1000.0
		}
	}

	// Memory
	vmem, err := mem.VirtualMemoryWithContext(ctx)
	if err == nil {
		result.MemTotal = vmem.Total
		result.MemUsed = vmem.Used
		result.MemFree = vmem.Free
		result.MemUsedPct = vmem.UsedPercent
		result.MemAvailable = vmem.Available
		result.MemBuffers = vmem.Buffers
		result.MemCached = vmem.Cached
	}

	// Swap
	swap, err := mem.SwapMemoryWithContext(ctx)
	if err == nil {
		result.SwapTotal = swap.Total
		result.SwapUsed = swap.Used
		result.SwapUsedPct = swap.UsedPercent
		result.SwapFree = swap.Free
	}

	// Disk
	partitions, err := disk.PartitionsWithContext(ctx, false)
	if err == nil {
		seenDevices := make(map[string]bool)
		for _, p := range partitions {
			deviceKey := p.Device + "|" + p.Fstype
			if p.Device == "" || seenDevices[deviceKey] {
				continue
			}
			usage, err := disk.UsageWithContext(ctx, p.Mountpoint)
			if err == nil {
				seenDevices[deviceKey] = true
				result.Disks = append(result.Disks, shared.DiskMetrics{
					Device:     p.Device,
					Mountpoint: p.Mountpoint,
					Fstype:     usage.Fstype,
					Total:      usage.Total,
					Used:       usage.Used,
					Free:       usage.Free,
					UsedPct:    usage.UsedPercent,
				})
			}
		}
	}

	// Disk model and NVMe temp
	if data, err := os.ReadFile("/sys/block/nvme0n1/device/model"); err == nil {
		result.DiskModel = strings.TrimSpace(string(data))
		result.DiskType = "NVMe SSD"
	}
	if data, err := os.ReadFile("/sys/block/nvme0n1/size"); err == nil {
		if sectors, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); err == nil {
			result.DiskSize = sectors * 512
		}
	}
	result.NvmeTemp = readNVMeTemp()

	// IO counters
	ioCounters, err := disk.IOCountersWithContext(ctx)
	var diskRead, diskWrite, diskIOms uint64
	if err == nil {
		for name, counter := range ioCounters {
			result.IOCounters = append(result.IOCounters, shared.DiskIO{
				Device:     name,
				ReadBytes:  counter.ReadBytes,
				WriteBytes: counter.WriteBytes,
				ReadCount:  counter.ReadCount,
				WriteCount: counter.WriteCount,
				IoTime:     counter.IoTime,
				WeightedIO: counter.WeightedIO,
			})
		}
		for name, counter := range ioCounters {
			if !isPhysicalBlockDevice(name) {
				continue
			}
			diskRead += counter.ReadBytes
			diskWrite += counter.WriteBytes
			diskIOms += counter.IoTime
		}
	}

	// Network
	result.NetAdapter = defaultRouteInterface()
	netIO, err := net.IOCountersWithContext(ctx, true)
	if err == nil {
		for _, counters := range netIO {
			if counters.Name != result.NetAdapter {
				continue
			}
			result.NetBytesSent = counters.BytesSent
			result.NetBytesRecv = counters.BytesRecv
			result.NetPacketsSent = counters.PacketsSent
			result.NetPacketsRecv = counters.PacketsRecv
			break
		}
	}

	// Convert monotonic counters into rates once in the collector. API and SSE
	// consumers reuse this snapshot instead of calculating their own deltas.
	c.rateMu.Lock()
	if !c.previousAt.IsZero() {
		elapsed := time.Since(c.previousAt).Seconds()
		if elapsed > 0 {
			if diskRead >= c.previousRead {
				result.DiskReadBps = float64(diskRead-c.previousRead) / elapsed
			}
			if diskWrite >= c.previousWrite {
				result.DiskWriteBps = float64(diskWrite-c.previousWrite) / elapsed
			}
			if diskIOms >= c.previousIOms {
				result.DiskActivePct = math.Min(float64(diskIOms-c.previousIOms)/(elapsed*1000)*100, 100)
			}
			if result.NetBytesRecv >= c.previousRecv {
				result.NetRecvBps = float64(result.NetBytesRecv-c.previousRecv) / elapsed
			}
			if result.NetBytesSent >= c.previousSent {
				result.NetSentBps = float64(result.NetBytesSent-c.previousSent) / elapsed
			}
		}
	}
	c.previousAt = time.Now()
	c.previousRead = diskRead
	c.previousWrite = diskWrite
	c.previousIOms = diskIOms
	c.previousRecv = result.NetBytesRecv
	c.previousSent = result.NetBytesSent
	c.rateMu.Unlock()
	// Network adapter details
	if addrs, err := net.Interfaces(); err == nil {
		for _, iface := range addrs {
			if iface.Name != result.NetAdapter {
				continue
			}
			for _, addr := range iface.Addrs {
				ip := strings.Split(addr.Addr, "/")[0]
				if strings.Contains(ip, ".") {
					result.NetIPv4 = ip
				}
			}
			break
		}
	}
	// gopsutil may return interface counters while omitting addresses on some
	// kernels. Resolve the selected route interface through the standard
	// library as an authoritative fallback.
	if result.NetIPv4 == "" {
		result.NetIPv4 = interfaceIPv4(result.NetAdapter)
	}
	if result.NetAdapter != "" {
		if data, err := os.ReadFile("/sys/class/net/" + result.NetAdapter + "/speed"); err == nil {
			if speed, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				if speed >= 1000 {
					result.NetLinkSpeed = strconv.Itoa(speed/1000) + " Gbps"
				} else {
					result.NetLinkSpeed = strconv.Itoa(speed) + " Mbps"
				}
			}
		}
		vendorMap := map[string]string{"0x10ec": "Realtek", "0x8086": "Intel", "0x14e4": "Broadcom", "0x10de": "NVIDIA"}
		vendorPath := "/sys/class/net/" + result.NetAdapter + "/device/vendor"
		if data, err := os.ReadFile(vendorPath); err == nil {
			vid := strings.TrimSpace(string(data))
			if v, ok := vendorMap[vid]; ok {
				result.NetVendor = v
			} else {
				result.NetVendor = vid
			}
		} else if data, err := firstBondVendor(result.NetAdapter); err == nil {
			vid := strings.TrimSpace(string(data))
			if v, ok := vendorMap[vid]; ok {
				result.NetVendor = v
			} else {
				result.NetVendor = vid
			}
		}
	}

	// Processes
	procs, err := process.ProcessesWithContext(ctx)
	if err == nil {
		result.ProcessCount = len(procs)
	}

	// Load avg
	load, err := loadAverage(ctx)
	if err == nil {
		result.Load1 = load.Load1
		result.Load5 = load.Load5
		result.Load15 = load.Load15
	}

	// Write metrics to store
	result.CollectedAt = ts.UnixMilli()
	c.writeMetrics(&result, ts)

	return result, nil
}

func isPhysicalBlockDevice(name string) bool {
	if name == "" || strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") || strings.HasPrefix(name, "dm-") {
		return false
	}
	if _, err := os.Stat("/sys/class/block/" + name + "/partition"); err == nil {
		return false
	}
	_, err := os.Stat("/sys/class/block/" + name)
	return err == nil
}

func defaultRouteInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[1] == "00000000" {
				flags, _ := strconv.ParseUint(fields[3], 16, 64)
				if flags&0x1 != 0 {
					return fields[0]
				}
			}
		}
	}
	return ""
}

func interfaceIPv4(adapter string) string {
	if adapter == "" {
		return ""
	}
	iface, err := stdnet.InterfaceByName(adapter)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		host := strings.SplitN(addr.String(), "/", 2)[0]
		ip := stdnet.ParseIP(host)
		if ip != nil && ip.To4() != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	return ""
}

func readNVMeTemp() *float64 {
	paths, _ := filepath.Glob("/sys/class/hwmon/hwmon*")
	var hottest *float64
	for _, hwmonPath := range paths {
		name, err := os.ReadFile(filepath.Join(hwmonPath, "name"))
		if err != nil || !strings.EqualFold(strings.TrimSpace(string(name)), "nvme") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hwmonPath, "temp1_input"))
		if err != nil {
			continue
		}
		milliCelsius, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		value := milliCelsius / 1000.0
		if hottest == nil || value > *hottest {
			copy := value
			hottest = &copy
		}
	}
	return hottest
}

func firstBondVendor(adapter string) ([]byte, error) {
	data, err := os.ReadFile("/sys/class/net/" + adapter + "/bonding/slaves")
	if err != nil {
		return nil, err
	}
	for _, slave := range strings.Fields(string(data)) {
		if vendor, readErr := os.ReadFile("/sys/class/net/" + slave + "/device/vendor"); readErr == nil {
			return vendor, nil
		}
	}
	return nil, os.ErrNotExist
}

func loadAverage(ctx context.Context) (*shared.LoadAvg, error) {
	avg, err := load.AvgWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return &shared.LoadAvg{
		Load1:  avg.Load1,
		Load5:  avg.Load5,
		Load15: avg.Load15,
	}, nil
}

func (c *SystemCollector) writeMetrics(m *shared.SystemMetrics, ts time.Time) {
	c.write("cpu_util", m.CPUUtil, nil, ts)
	c.write("mem_total", float64(m.MemTotal), nil, ts)
	c.write("mem_used", float64(m.MemUsed), nil, ts)
	c.write("mem_used_pct", m.MemUsedPct, nil, ts)
	c.write("mem_available", float64(m.MemAvailable), nil, ts)
	c.write("swap_used_pct", m.SwapUsedPct, nil, ts)
	c.write("process_count", float64(m.ProcessCount), nil, ts)
	c.write("load_1", m.Load1, nil, ts)
	c.write("load_5", m.Load5, nil, ts)
	c.write("load_15", m.Load15, nil, ts)
	c.write("net_bytes_sent", float64(m.NetBytesSent), nil, ts)
	c.write("net_bytes_recv", float64(m.NetBytesRecv), nil, ts)
	c.write("net_packets_sent", float64(m.NetPacketsSent), nil, ts)
	c.write("net_packets_recv", float64(m.NetPacketsRecv), nil, ts)
	c.write("net_sent_bps", m.NetSentBps, nil, ts)
	c.write("net_recv_bps", m.NetRecvBps, nil, ts)
	c.write("disk_read_bps", m.DiskReadBps, nil, ts)
	c.write("disk_write_bps", m.DiskWriteBps, nil, ts)
	c.write("disk_active_pct", m.DiskActivePct, nil, ts)
	c.write("cpu_freq_current", m.CPUFreqCur, nil, ts)
	c.write("mem_used_gb", float64(m.MemUsed)/1073741824, nil, ts)

	for _, d := range m.Disks {
		labels := map[string]string{"mountpoint": d.Mountpoint, "fstype": d.Fstype}
		c.write("disk_total", float64(d.Total), labels, ts)
		c.write("disk_used", float64(d.Used), labels, ts)
		c.write("disk_used_pct", d.UsedPct, labels, ts)
		c.write("disk_free", float64(d.Free), labels, ts)
	}
}

func (c *SystemCollector) write(name string, value float64, labels map[string]string, ts time.Time) {
	if c.base != nil && c.base.store != nil {
		c.base.store.Write(name, value, labels, ts)
	}
}
