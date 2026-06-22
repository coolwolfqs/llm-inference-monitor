"""CPU metrics collector using /proc and psutils."""
import asyncio
import logging

logger = logging.getLogger(__name__)


class CPUCollector:
    """Collect CPU metrics."""

    async def collect(self) -> dict:
        try:
            import psutil
            # CPU percent (non-blocking avg)
            cpu_percent = psutil.cpu_percent(interval=0.5)
            per_core = psutil.cpu_percent(interval=0.1, percpu=True)
            freq = psutil.cpu_freq()
            cpu_info = {"usage": cpu_percent, "per_core": per_core}
            if freq:
                cpu_info["freq_current"] = freq.current
                cpu_info["max_mhz"] = freq.max
            else:
                cpu_info["freq_current"] = 0
                cpu_info["max_mhz"] = 0

            # Get model info from /proc/cpuinfo
            model = ""
            physical_cores = 0
            logical_cores = 0
            virt = ""
            l2 = ""
            l3 = ""
            try:
                with open("/proc/cpuinfo") as f:
                    for line in f:
                        if line.startswith("model name"):
                            model = line.split(":")[1].strip()
                        elif line.startswith("siblings"):
                            logical_cores = int(line.split(":")[1].strip())
                        elif line.startswith("cpu cores"):
                            physical_cores = int(line.split(":")[1].strip())
                        elif line.startswith("flags") and "vmx" in line:
                            virt = "VT-x"
                        elif line.startswith("flags") and "svm" in line:
                            virt = "AMD-V"
            except (OSError, IOError):
                pass

            # L2/L3 cache
            try:
                import glob
                l2_dirs = glob.glob("/sys/devices/system/cpu/cpu0/cache/index2/size")
                l3_dirs = glob.glob("/sys/devices/system/cpu/cpu0/cache/index3/size")
                if l2_dirs:
                    with open(l2_dirs[0]) as f:
                        l2 = f.read().strip()
                if l3_dirs:
                    with open(l3_dirs[0]) as f:
                        l3 = f.read().strip()
            except Exception:
                pass

            # Temperature
            temp_tctl = 0
            try:
                temps = psutil.sensors_temperatures()
                if "coretemp" in temps:
                    temp_tctl = temps["coretemp"][0].current
                elif "k10temp" in temps:
                    temp_tctl = temps["k10temp"][0].current
            except Exception:
                pass

            # Load
            load1, load5, load15 = psutil.getloadavg()
            # Process count
            proc_count = len(psutil.pids())

            return {
                "usage": cpu_percent,
                "model": model,
                "freq_current": cpu_info["freq_current"],
                "max_mhz": cpu_info["max_mhz"],
                "temp_tctl": temp_tctl,
                "physical_cores": physical_cores,
                "logical_cores": logical_cores,
                "virt": virt,
                "l2_cache": l2,
                "l3_cache": l3,
                "per_core": per_core,
                "load_1": load1,
                "load_5": load5,
                "load_15": load15,
                "process_count": proc_count,
            }
        except ImportError:
            logger.warning("psutil not installed. Using /proc fallback.")
            return await self._fallback()
        except Exception as e:
            logger.error(f"CPU collection error: {e}")
            return {"usage": 0, "model": "", "per_core": []}

    async def _fallback(self) -> dict:
        try:
            with open("/proc/stat") as f:
                first = f.readline().split()
            total = sum(int(v) for v in first[1:])
            idle = int(first[4])
            usage = 100.0 * (1 - idle / max(total, 1))
            return {"usage": round(usage, 1), "model": "", "per_core": []}
        except Exception:
            return {"usage": 0, "model": "", "per_core": []}