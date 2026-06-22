"""System-level metrics collector."""
import asyncio
import time
import logging

logger = logging.getLogger(__name__)


class SystemCollector:
    async def collect(self) -> dict:
        try:
            import psutil
            boot_time = psutil.boot_time()
            uptime_seconds = time.time() - boot_time
            uptime_str = self._fmt_uptime(uptime_seconds)
            return {"uptime_seconds": uptime_seconds, "uptime_str": uptime_str}
        except Exception:
            return {"uptime_str": "running"}

    def _fmt_uptime(self, seconds):
        days = int(seconds // 86400)
        hours = int((seconds % 86400) // 3600)
        minutes = int((seconds % 3600) // 60)
        if days > 0:
            return f"{days}d {hours}h {minutes}m"
        if hours > 0:
            return f"{hours}h {minutes}m"
        return f"{minutes}m"