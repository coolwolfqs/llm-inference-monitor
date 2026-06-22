"""Memory metrics collector using psutil."""
import asyncio
import logging

logger = logging.getLogger(__name__)


class MemoryCollector:
    async def collect(self) -> dict:
        try:
            import psutil
            mem = psutil.virtual_memory()
            swap = psutil.swap_memory()

            used_gb = mem.total - mem.available
            return {
                "percent": mem.percent,
                "used": mem.total - mem.available,
                "available": mem.available,
                "total": mem.total,
                "used_str": self._fmt_gb(mem.total - mem.available),
                "free_str": self._fmt_gb(mem.available),
                "total_str": self._fmt_gb(mem.total),
                "used_gb": (mem.total - mem.available) / (1024**3),
                "cached": mem.cached if hasattr(mem, "cached") else 0,
                "buffers": mem.buffers if hasattr(mem, "buffers") else 0,
                "swap_pct": swap.percent,
                "swap_used": swap.used,
                "swap_total": swap.total,
            }
        except ImportError:
            return self._fallback()
        except Exception as e:
            logger.error(f"Memory collection error: {e}")
            return {"percent": 0, "used_str": "0 GB", "free_str": "0 GB", "total_str": "0 GB"}

    def _fmt_gb(self, bytes_val):
        gb = bytes_val / (1024**3)
        return f"{gb:.1f} GB"

    def _fallback(self):
        try:
            with open("/proc/meminfo") as f:
                data = {}
                for line in f:
                    parts = line.split(":")
                    if len(parts) == 2:
                        key = parts[0].strip()
                        val_str = parts[1].strip().split()[0]
                        try:
                            data[key] = int(val_str) * 1024  # kB to bytes
                        except ValueError:
                            pass
            total = data.get("MemTotal", 0)
            free = data.get("MemFree", 0)
            cached = data.get("Cached", 0)
            buffers = data.get("Buffers", 0)
            available = data.get("MemAvailable", free + cached)
            used = total - available
            pct = (used / max(total, 1)) * 100
            return {
                "percent": round(pct, 1),
                "used": used, "available": available, "total": total,
                "used_str": self._fmt_gb(used), "free_str": self._fmt_gb(available),
                "total_str": self._fmt_gb(total), "used_gb": used / (1024**3),
                "cached": cached, "buffers": buffers,
                "swap_pct": 0, "swap_used": 0, "swap_total": 0,
            }
        except Exception as e:
            logger.error(f"Memory fallback error: {e}")
            return {"percent": 0, "used_str": "0 GB", "free_str": "0 GB", "total_str": "0 GB"}