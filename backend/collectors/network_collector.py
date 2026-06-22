"""Network metrics collector using psutil."""
import asyncio
import logging

logger = logging.getLogger(__name__)


class NetworkCollector:
    async def collect(self) -> dict:
        try:
            import psutil
            net_io = psutil.net_io_counters()
            addrs = psutil.net_if_addrs()
            stats = psutil.net_if_stats()

            # Find main adapter (the one with a default route)
            adapter = ""
            ipv4 = ""
            link_speed = ""
            vendor = ""

            try:
                with open("/proc/net/route") as f:
                    for line in f:
                        parts = line.split()
                        if len(parts) > 1 and parts[1] == "00000000":
                            adapter = parts[0]
                            break
            except Exception:
                pass

            if not adapter:
                # Fallback: pick first non-loopback
                for name, ips in addrs.items():
                    for ip in ips:
                        if ip.family == 2 and not ip.address.startswith("127."):
                            adapter = name
                            break
                    if adapter:
                        break

            if adapter:
                if adapter in stats:
                    s = stats[adapter]
                    link_speed = f"{s.speed} Mbps" if s.speed > 0 else "--"
                if adapter in addrs:
                    for ip in addrs[adapter]:
                        if ip.family == 2:
                            ipv4 = ip.address
                            break

            # Current throughput (approximate via first sample)
            # In real deployment, track delta between calls
            recv_bps = 0
            sent_bps = 0
            if net_io:
                recv_bps = net_io.bytes_recv
                sent_bps = net_io.bytes_sent

            return {
                "adapter": adapter,
                "vendor": vendor,
                "link_speed": link_speed,
                "ipv4": ipv4,
                "recv_bps": recv_bps,
                "sent_bps": sent_bps,
                "recv_str": self._fmt_speed(recv_bps),
                "sent_str": self._fmt_speed(sent_bps),
            }
        except ImportError:
            return {"adapter": "--", "recv_bps": 0, "sent_bps": 0}
        except Exception as e:
            logger.error(f"Network collection error: {e}")
            return {"adapter": "--", "recv_bps": 0, "sent_bps": 0}

    def _fmt_speed(self, bps):
        if bps >= 1073741824:
            return f"{bps / 1073741824:.2f} GB/s"
        if bps >= 1048576:
            return f"{bps / 1048576:.2f} MB/s"
        if bps >= 1024:
            return f"{bps / 1024:.1f} KB/s"
        return f"{bps:.0f} B/s"