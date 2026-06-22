"""GPU metrics collector using nvidia-smi or NVML bindings."""
import asyncio
import subprocess
import json
import re
import logging

logger = logging.getLogger(__name__)


class GPUCollector:
    """Collect GPU metrics via nvidia-smi."""

    def __init__(self):
        self._binary = self._find_nvidia_smi()

    def _find_nvidia_smi(self) -> str:
        import shutil
        return shutil.which("nvidia-smi") or "/usr/bin/nvidia-smi"

    async def collect(self) -> dict:
        """Collect all GPU metrics. Returns {'gpus': [...]}."""
        try:
            result = await asyncio.create_subprocess_exec(
                self._binary,
                "--query-gpu=index,name,uuid,utilization.gpu,utilization.memory,"
                "memory.total,memory.used,memory.free,temperature.gpu,"
                "power.draw,power.limit,fan.speed,clocks.current.graphics,"
                "clocks.max.graphics,clocks.current.memory,clocks.max.memory,"
                "pcie.link.gen.current,pcie.link.gen.max,pcie.link.width.current,"
                "pcie.link.width.max,encoder.utilization,decoder.utilization",
                "--format=csv,noheader,nounits",
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            stdout, stderr = await result.communicate()
            if result.returncode != 0:
                return {"gpus": []}

            gpus = []
            lines = stdout.decode().strip().split("\n")
            for line in lines:
                if not line.strip():
                    continue
                cols = [c.strip() for c in line.split(", ")]
                if len(cols) < 10:
                    continue

                try:
                    gpu = {
                        "index": int(cols[0]),
                        "name": cols[1],
                        "uuid": "REDACTED",
                        "util": float(cols[3]) if cols[3] != "[Not Supported]" else 0,
                        "mem_util_pct": float(cols[4]) if cols[4] != "[Not Supported]" else 0,
                        "mem_total": float(cols[5]) if cols[5] else 0,
                        "mem_used": float(cols[6]) if cols[6] else 0,
                        "mem_free": float(cols[7]) if cols[7] else 0,
                        "temp": float(cols[8]) if cols[8] else 0,
                        "power_draw": float(cols[9]) if cols[9] else 0,
                        "power_limit": float(cols[10]) if cols[10] else 0,
                        "fan_speed": float(cols[11]) if cols[11] and cols[11] != "[Not Supported]" else 0,
                        "clock": float(cols[12]) if cols[12] else 0,
                        "clock_max": float(cols[14]) if cols[14] else 0,
                        "mem_clock": float(cols[14]) if cols[14] else 0,
                        "mem_clock_max": float(cols[15]) if cols[15] else 0,
                        "enc_util": float(cols[20]) if len(cols) > 20 and cols[20] != "[Not Supported]" else None,
                        "dec_util": float(cols[21]) if len(cols) > 21 and cols[21] != "[Not Supported]" else None,
                    }
                    # Get PCIe info
                    pcie = await self._get_pcie_info(cols[0] if cols[0] else "0")
                    if pcie:
                        gpu["pcie"] = pcie
                    # Get processes
                    procs = await self._get_gpu_processes(cols[0] if cols[0] else "0")
                    if procs:
                        gpu["processes"] = procs
                    gpus.append(gpu)
                except (ValueError, IndexError) as e:
                    logger.warning(f"Failed to parse GPU line: {line}: {e}")
                    continue

            return {"gpus": gpus}
        except FileNotFoundError:
            logger.warning("nvidia-smi not found. GPU monitoring disabled.")
            return {"gpus": []}
        except Exception as e:
            logger.error(f"GPU collection error: {e}")
            return {"gpus": []}

    async def _get_pcie_info(self, gpu_index: str) -> dict:
        try:
            result = await asyncio.create_subprocess_exec(
                self._binary, f"--id={gpu_index}",
                "--query-gpu=pcie.link.gen.current,pcie.link.gen.max,"
                "pcie.link.width.current,pcie.link.width.max",
                "--format=csv,noheader,nounits",
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            stdout, _ = await result.communicate()
            cols = [c.strip() for c in stdout.decode().strip().split(", ")]
            if len(cols) >= 4:
                return {
                    "current_gen": cols[0],
                    "gen": cols[1],
                    "current_width": cols[2],
                    "width": cols[3],
                }
        except Exception:
            pass
        return {}

    async def _get_gpu_processes(self, gpu_index: str) -> list:
        try:
            result = await asyncio.create_subprocess_exec(
                self._binary, f"--id={gpu_index}",
                "--query-compute-apps=pid,process_name,used_memory",
                "--format=csv,noheader,nounits",
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            stdout, _ = await result.communicate()
            procs = []
            for line in stdout.decode().strip().split("\n"):
                if not line.strip():
                    continue
                cols = [c.strip() for c in line.split(", ")]
                if len(cols) >= 3:
                    try:
                        procs.append({
                            "pid": 0,  # Redacted
                            "name": cols[1].split("/")[-1] if "/" in cols[1] else cols[1],
                            "used_memory": float(cols[2]) if cols[2] else 0,
                        })
                    except (ValueError, IndexError):
                        pass
            return procs
        except Exception:
            return []

    async def set_power_limit(self, gpu_index: int, percentage: int) -> dict:
        """Set GPU power limit. Requires sudo/nvidia-smi admin."""
        try:
            proc = await asyncio.create_subprocess_exec(
                "sudo", self._binary, f"-i", str(gpu_index),
                f"-pl", str(percentage),
                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            )
            stdout, stderr = await proc.communicate()
            if proc.returncode == 0:
                return {"status": "ok", "message": f"GPU {gpu_index} power limit set to {percentage}%"}
            else:
                return {"status": "error", "message": stderr.decode().strip()}
        except Exception as e:
            return {"status": "error", "message": str(e)}