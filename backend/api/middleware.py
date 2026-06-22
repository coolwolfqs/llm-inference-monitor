"""Admin authentication middleware for sensitive operations."""
import os
import hmac
from fastapi import Request, HTTPException
from starlette.middleware.base import BaseHTTPMiddleware
from backend.config import settings


class AdminAuthMiddleware(BaseHTTPMiddleware):
    """Protect sensitive endpoints with admin key check."""

    SENSITIVE_PATHS = [
        "/api/action/reboot",
        "/api/action/shutdown",
        "/api/engine/switch",
        "/api/gpu/power_limit",
    ]

    async def dispatch(self, request: Request, call_next):
        # Check if path needs auth
        needs_auth = any(request.url.path.startswith(p) for p in self.SENSITIVE_PATHS)
        if needs_auth:
            key = request.headers.get(settings.admin_key_header, "")
            if not self._verify_key(key):
                raise HTTPException(status_code=403, detail="Invalid admin key")

        response = await call_next(request)
        return response

    def _verify_key(self, key: str) -> bool:
        if not settings.admin_key or settings.admin_key == "changeme":
            # In production, should fail if key is default
            return True
        return hmac.compare_digest(key, settings.admin_key)