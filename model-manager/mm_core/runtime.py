"""Bounded-cost runtime discovery primitives.

Hot request paths must never enumerate every process on the host.  systemd's
cgroup is the authoritative ownership boundary for the inference service and
normally contains only the launcher/server processes for that unit.
"""

from __future__ import annotations

import copy
import threading
import time
from pathlib import Path
from typing import Callable, Generic, TypeVar


T = TypeVar("T")


def service_pids(cgroup_procs: Path) -> list[int] | None:
    """Return service-owned PIDs, or None only when cgroup data is unavailable.

    An existing empty file means the service is stopped and must not trigger a
    host-wide fallback scan.
    """
    try:
        raw = cgroup_procs.read_text(encoding="ascii")
    except (FileNotFoundError, PermissionError, OSError):
        return None
    result: list[int] = []
    for line in raw.splitlines():
        value = line.strip()
        if value.isdigit():
            result.append(int(value))
    return result


class SingleFlightTTLCache(Generic[T]):
    """Thread-safe TTL cache that coalesces concurrent misses into one load."""

    def __init__(self, loader: Callable[[], T], ttl_seconds: float) -> None:
        self.loader = loader
        self.ttl_seconds = max(0.05, float(ttl_seconds))
        self._condition = threading.Condition()
        self._value: T | None = None
        self._has_value = False
        self._loaded_at = 0.0
        self._refreshing = False
        self._generation = 0
        self._loads = 0
        self._hits = 0
        self._waits = 0

    def get(self, *, force: bool = False) -> T:
        with self._condition:
            now = time.monotonic()
            if self._has_value and not force and now - self._loaded_at < self.ttl_seconds:
                self._hits += 1
                return copy.deepcopy(self._value)
            if self._refreshing:
                # Stale-while-revalidate: ordinary readers must not queue behind
                # the observer's forced refresh. Runtime state is sampled every
                # second, so bounded staleness beats a periodic latency cliff.
                if self._has_value and not force:
                    self._hits += 1
                    return copy.deepcopy(self._value)
                observed_generation = self._generation
                self._waits += 1
                while self._refreshing and observed_generation == self._generation:
                    self._condition.wait(timeout=5.0)
                if self._has_value:
                    return copy.deepcopy(self._value)
            self._refreshing = True
        try:
            value = self.loader()
        except Exception:
            with self._condition:
                self._refreshing = False
                self._condition.notify_all()
            raise
        with self._condition:
            self._value = value
            self._has_value = True
            self._loaded_at = time.monotonic()
            self._refreshing = False
            self._generation += 1
            self._loads += 1
            self._condition.notify_all()
            return copy.deepcopy(value)

    def invalidate(self) -> None:
        with self._condition:
            self._has_value = False
            self._loaded_at = 0.0

    def peek(self) -> tuple[bool, T | None]:
        """Read the last published snapshot without scheduling or waiting.

        The background observer owns refresh work. HTTP read paths consume the
        immutable published value so request concurrency cannot exhaust the
        executor that performs collection.
        """
        with self._condition:
            return self._has_value, copy.deepcopy(self._value)

    def metadata(self) -> dict[str, int | float]:
        with self._condition:
            age = time.monotonic() - self._loaded_at if self._has_value else -1.0
            return {
                "generation": self._generation,
                "loads": self._loads,
                "hits": self._hits,
                "waits": self._waits,
                "age_seconds": round(max(0.0, age), 3) if age >= 0 else -1.0,
                "ttl_seconds": self.ttl_seconds,
            }
