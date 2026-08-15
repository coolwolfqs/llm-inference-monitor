"""Durable, secret-free control-plane operation history."""

from __future__ import annotations

import sqlite3
import threading
import time
import uuid
from pathlib import Path
from typing import Any


class OperationStore:
    def __init__(self, database: Path) -> None:
        self.database = database
        self._lock = threading.RLock()
        database.parent.mkdir(parents=True, exist_ok=True)
        with self._connect() as connection:
            connection.executescript(
                """
                PRAGMA journal_mode=WAL;
                PRAGMA synchronous=NORMAL;
                CREATE TABLE IF NOT EXISTS operations (
                    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
                    operation_id TEXT NOT NULL UNIQUE,
                    method TEXT NOT NULL,
                    path TEXT NOT NULL,
                    state TEXT NOT NULL,
                    status_code INTEGER,
                    client TEXT,
                    started_at REAL NOT NULL,
                    finished_at REAL,
                    duration_ms REAL,
                    error TEXT
                );
                CREATE INDEX IF NOT EXISTS operations_started_at
                    ON operations(started_at DESC);
                """
            )

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.database, timeout=5.0)
        connection.row_factory = sqlite3.Row
        return connection

    def start(self, method: str, path: str, client: str) -> str:
        operation_id = f"op_{uuid.uuid4().hex}"
        with self._lock, self._connect() as connection:
            connection.execute(
                "INSERT INTO operations(operation_id,method,path,state,client,started_at) VALUES(?,?,?,?,?,?)",
                (operation_id, method, path, "running", client, time.time()),
            )
        return operation_id

    def finish(self, operation_id: str, status_code: int, error: str = "") -> None:
        finished = time.time()
        state = "succeeded" if 200 <= status_code < 400 else "failed"
        safe_error = str(error or "")[:500]
        with self._lock, self._connect() as connection:
            connection.execute(
                """UPDATE operations
                   SET state=?, status_code=?, finished_at=?,
                       duration_ms=ROUND((?-started_at)*1000,2), error=?
                   WHERE operation_id=?""",
                (state, status_code, finished, finished, safe_error, operation_id),
            )

    def list(self, limit: int = 50) -> list[dict[str, Any]]:
        with self._lock, self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM operations ORDER BY sequence DESC LIMIT ?",
                (max(1, min(int(limit), 200)),),
            ).fetchall()
        return [dict(row) for row in rows]

    def latest_sequence(self) -> int:
        with self._lock, self._connect() as connection:
            row = connection.execute(
                "SELECT COALESCE(MAX(sequence),0) FROM operations"
            ).fetchone()
        return int(row[0])
