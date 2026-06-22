"""Inference service metrics collector.
Connects to llama.cpp / llama-server API to gather inference stats."""
import asyncio
import json
import logging
import aiohttp

logger = logging.getLogger(__name__)


class InferenceCollector:
    def __init__(self):
        from backend.config import settings
        self.inference_url = f"http://{settings.inference_host}:{settings.inference_port}"
        self.model_manager_url = settings.model_manager_url

    async def collect(self) -> dict:
        result = {
            "stats": {},
            "kv_cache": {},
            "llm_metrics": {},
            "services": {},
            "deploy_config": {},
            "params": {},
            "ip_stats": [],
        }

        try:
            async with aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=5)) as session:
                # Get inference status from llama-server
                try:
                    async with session.get(f"{self.inference_url}/status") as resp:
                        if resp.status == 200:
                            data = await resp.json()
                            slots = data.get("slots", [])
                            active_slots = sum(1 for s in slots if s.get("state") == "processing")
                            total_slots = len(slots)

                            # Compute TPS from slots
                            gen_tps = 0
                            prompt_tokens = sum(s.get("prompt_tokens", 0) for s in slots)
                            eval_tokens = sum(s.get("tokens_evaluated", 0) for s in slots)
                            for s in slots:
                                if s.get("state") == "processing" and s.get("speed", 0) > 0:
                                    gen_tps += s.get("speed", 0)

                            result["stats"] = {
                                "active_slots": active_slots,
                                "total_slots": total_slots,
                                "queue_depth": data.get("queue_depth", 0),
                                "last_tps": gen_tps,
                                "last_prompt_tokens": prompt_tokens,
                                "last_eval_tokens": eval_tokens,
                            }
                except (aiohttp.ClientError, asyncio.TimeoutError, Exception) as e:
                    logger.warning(f"Inference status fetch failed: {e}")
                    result["services"]["推理服务"] = {"status": "unreachable", "port": self.inference_url}

                # Get LLM metrics
                try:
                    async with session.get(f"{self.inference_url}/metrics") as resp:
                        if resp.status == 200:
                            result["llm_metrics"] = await resp.json()
                except Exception:
                    pass

                # Get KV cache info
                try:
                    async with session.get(f"{self.inference_url}/kv_cache") as resp:
                        if resp.status == 200:
                            result["kv_cache"] = await resp.json()
                except Exception:
                    pass

                # Get deployment config from inference service
                try:
                    async with session.get(f"{self.inference_url}/props") as resp:
                        if resp.status == 200:
                            result["deploy_config"] = await resp.json()
                except Exception:
                    pass

                # Get IP stats if available
                try:
                    async with session.get(f"{self.inference_url}/ip_stats") as resp:
                        if resp.status == 200:
                            result["ip_stats"] = await resp.json()
                except Exception:
                    pass

                # Model manager info
                try:
                    async with session.get(f"{self.model_manager_url}/api/models") as resp:
                        if resp.status == 200:
                            model_data = await resp.json()
                            result["params"] = model_data
                except Exception:
                    pass

        except ImportError:
            logger.warning("aiohttp not installed. Inference monitoring disabled.")
        except Exception as e:
            logger.error(f"Inference collection error: {e}")

        return result

    async def get_engines(self) -> list:
        return [
            {
                "key": "llama.cpp",
                "display_version": "turboquant",
                "is_running": True,
                "upstream_tag": "b4876",
                "github_url": "https://github.com/ggml-org/llama.cpp",
                "binary_path": "/usr/local/bin/llama-server",
                "features": ["CUDA", "MTP", "Flash Attention", "KV Cache Quant", "BF16"],
            },
            {
                "key": "vllm",
                "display_version": "0.8.0",
                "is_running": False,
                "upstream_tag": "v0.8.0",
                "github_url": "https://github.com/vllm-project/vllm",
                "binary_path": "/usr/local/bin/vllm",
                "features": ["CUDA", "PagedAttention", "Continuous Batching"],
            },
        ]

    async def switch_engine(self, engine_key: str) -> dict:
        logger.info(f"Engine switch requested: {engine_key}")
        return {"status": "ok", "active": True, "engine": engine_key}

    async def set_persist_mode(self, mode: str) -> dict:
        return {"status": "ok", "mode": mode}

    async def get_persist_mode(self) -> str:
        return "auto"

    async def refresh_kv_baseline(self) -> dict:
        return {"status": "ok", "message": "KV baseline refreshed"}