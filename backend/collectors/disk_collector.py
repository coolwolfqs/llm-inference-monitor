"""Disk metrics collector using psutil."""
import asyncio
import logging

logger = logging.getLogger(__name__)


class DiskCollector:
    async def collect(self) -> dict:
        try:
            import psutil
            # Disk I/O
            io = psutil.disk_io_counters()
            disk_usage = psutil.disk_usage("/")

            # Partitions
            partitions = []
            for part in psutil.disk_partitions():
                try:
                    usage = psutil.disk_usage(part.mountpoint)
                    partitions.append({
                        "mountpoint": part.mountpoint,
                        "device": part.device,
                        "fstype": part.fstype,
                        "total": usage.total,
                        "used": usage.used,
                        "free": usage.free,
                        "used_pct": usage.percent,
                    })
                except Exception:
                    continue

            # NVMe temperature (if available)
            nvme_temp = 0
            try:
                import glob
                temp_files = glob.glob("/sys/class/nvme/nvme*/device/temp1_input")
                if temp_files:
                    with open(temp_files[0]) as f:
                        nvme_temp = int(f.read().strip()) // 1000
            except Exception:
                pass

            # Disk model (redacted)
            model = ""
            disk_type = ""
            try:
                import glob as g2
                model_files = g2.glob("/sys/block/nvme*/device/model")
                if model_files:
                    with open(model_files[0]) as f:
                        model = f.read().strip()
                    disk_type = "NVMe SSD"
                else:
                    sata = g2.glob("/sys/block/sd*/device/model")
                    if sata:
                        with open(sata[0]) as f:
                            model = f.read().strip()
                        disk_type = "SATA SSD/HDD"
            except Exception:
                pass

            # Compute rates from io counters
            read_bps = io.read_bytes if io else 0
            write_bps = io.write_bytes if io else 0

            # Active pct estimation
            try:
                with open("/proc/diskstats") as f:
                    lines = f.readlines()
                active = 0
                for line in lines:
                    parts = line.split()
                    if len(parts) >= 13 and parts[2].startswith(("nvme", "sd")):
                        io_ms = int(parts[12])
                        active = min(100, io_ms / 100)  # rough estimate
            except Exception:
                active = 0

            size_gb = disk_usage.total / (1024**3) if disk_usage else 0

            return {
                "active_pct": round(active, 1),
                "read_bps": read_bps,
                "write_bps": write_bps,
                "read_str": self._fmt_speed(read_bps),
                "write_str": self._fmt_speed(write_bps),
                "model": model,
                "type": disk_type,
                "size_gb": round(size_gb, 1),
                "nvme_temp": nvme_temp,
                "partitions": partitions,
            }
        except ImportError:
            return {"active_pct": 0, "read_bps": 0, "write_bps": 0, "partitions": []}
        except Exception as e:
            logger.error(f"Disk collection error: {e}")
            return {"active_pct": 0, "partitions": []}

    def _fmt_speed(self, bps):
        if bps >= 1073741824:
            return f"{bps / 1073741824:.2f} GB/s"
        if bps >= 1048576:
            return f"{bps / 1048576:.2f} MB/s"
        if bps >= 1024:
            return f"{bps / 1024:.1f} KB/s"
        return f"{bps:.0f} B/s"