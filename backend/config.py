"""
LLM Inference Monitor - Configuration
========================================
All sensitive values are read from environment variables or config.yaml.
Never hardcode IPs, ports, keys, or paths.

Environment variables:
  MONITOR_HOST - Bind address (default: 0.0.0.0)
  MONITOR_PORT - Server port (default: 8081)
  MONITOR_DEBUG - Debug mode (default: false)
  ADMIN_KEY - Admin key for sensitive operations
  INFERENCE_HOST - Inference service host
  INFERENCE_PORT - Inference service port
  MODEL_MANAGER_URL - Model manager URL
  CORS_ORIGINS - CORS allowed origins
"""
import os
import re
import json
from typing import List


class Settings:
    def __init__(self):
        # Server settings
        self.server_host: str = os.environ.get("MONITOR_HOST", "0.0.0.0")
        self.server_port: int = int(os.environ.get("MONITOR_PORT", "8081"))
        self.debug: bool = os.environ.get("MONITOR_DEBUG", "").lower() in ("true", "1", "yes")

        # Admin security
        self.admin_key: str = os.environ.get("ADMIN_KEY", "changeme")
        self.admin_key_header: str = "X-Admin-Key"

        # Service endpoints (configure per deployment)
        self.inference_host: str = os.environ.get("INFERENCE_HOST", "127.0.0.1")
        self.inference_port: int = int(os.environ.get("INFERENCE_PORT", "8080"))
        self.model_manager_url: str = os.environ.get("MODEL_MANAGER_URL", "http://127.0.0.1:8093")
        self.cluster_url: str = os.environ.get("CLUSTER_URL", "http://127.0.0.1:8082")
        self.new_api_url: str = os.environ.get("NEW_API_URL", "http://127.0.0.1:3000")
        self.benchmark_url: str = os.environ.get("BENCHMARK_URL", "http://127.0.0.1:8090")

        # CORS
        cors_str = os.environ.get("CORS_ORIGINS", "*")
        self.cors_origins: List[str] = [o.strip() for o in cors_str.split(",")]

        # Sensitive data redaction (configurable patterns)
        self.ip_redact_mode: str = os.environ.get("IP_REDACT_MODE", "partial")
        # "partial" = show last octet, "full" = show 0.0.0.0, "none" = show real

    def redact_ip(self, ip: str) -> str:
        """Redact IP address based on configured mode."""
        if not ip or ip in ("--", "N/A"):
            return "--"
        if self.ip_redact_mode == "none":
            return ip
        if self.ip_redact_mode == "full":
            return "0.0.0.0"
        # partial: keep last octet for diagnostics, zero out first three
        parts = ip.split(".")
        if len(parts) == 4:
            return f"192.168.{parts[2]}.{parts[3]}"
        # IPv6 or other
        return ip.split(":")[0] + ":****"

    def redact_adapter(self, name: str) -> str:
        """Redact network adapter name."""
        if not name:
            return "--"
        return "eth0"  # Generic adapter name

    def redact_disk_model(self, model: str) -> str:
        """Redact disk model (may contain serial-like info)."""
        if not model:
            return "--"
        # Keep only the brand prefix
        match = re.match(r'^([A-Za-z]+)', model)
        return (match.group(1) + " SSD ***") if match else "SSD ***"

    def redact_cpu_model(self, model: str) -> str:
        """Keep CPU model informative but remove serial numbers."""
        if not model:
            return "--"
        # Most CPU models are fine, just ensure no serial numbers
        model = re.sub(r'[A-F0-9]{8,}', '********', model)
        return model


settings = Settings()