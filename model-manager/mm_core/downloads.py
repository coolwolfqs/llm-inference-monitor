"""Persistent state for model downloads."""

from __future__ import annotations

import sqlite3
import threading
import time
import uuid
from pathlib import Path
from typing import Any


class DownloadTaskStore:
    def __init__(self, database: Path) -> None:
        self.database = database
        self._lock = threading.RLock()
        database.parent.mkdir(parents=True, exist_ok=True)
        with self._connect() as connection:
            connection.executescript(
                """
                PRAGMA journal_mode=WAL;
                CREATE TABLE IF NOT EXISTS download_tasks (
                    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
                    task_id TEXT NOT NULL UNIQUE,
                    repo_id TEXT NOT NULL,
                    filename TEXT NOT NULL,
                    target_path TEXT NOT NULL,
                    state TEXT NOT NULL,
                    phase TEXT NOT NULL,
                    progress INTEGER NOT NULL DEFAULT 0,
                    downloaded_bytes INTEGER NOT NULL DEFAULT 0,
                    total_bytes INTEGER NOT NULL DEFAULT 0,
                    created_at REAL NOT NULL,
                    started_at REAL,
                    finished_at REAL,
                    error TEXT
                );
                """
            )
            connection.execute(
                """UPDATE download_tasks SET state='failed', phase='interrupted',
                   finished_at=?, error='控制面重启导致下载中断'
                   WHERE state IN ('queued','running')""",
                (time.time(),),
            )

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.database, timeout=5.0)
        connection.row_factory = sqlite3.Row
        return connection

    def create(self, repo_id: str, filename: str, target_path: str) -> dict[str, Any]:
        task_id = f"dl_{uuid.uuid4().hex}"
        with self._lock, self._connect() as connection:
            connection.execute(
                """INSERT INTO download_tasks
                   (task_id,repo_id,filename,target_path,state,phase,progress,created_at)
                   VALUES(?,?,?,?,'queued','queued',0,?)""",
                (task_id, repo_id, filename, target_path, time.time()),
            )
        return self.get(task_id) or {}

    def update(self, task_id: str, *, state: str, phase: str, progress: int,
               downloaded_bytes: int = 0, total_bytes: int = 0, error: str = "") -> None:
        now = time.time()
        terminal = state in {"succeeded", "failed", "cancelled"}
        with self._lock, self._connect() as connection:
            connection.execute(
                """UPDATE download_tasks SET state=?,phase=?,progress=?,
                   downloaded_bytes=?,total_bytes=?,started_at=COALESCE(started_at,?),
                   finished_at=?,error=? WHERE task_id=?""",
                (state, phase, max(0, min(int(progress), 100)), int(downloaded_bytes),
                 int(total_bytes), now, now if terminal else None, str(error)[:500], task_id),
            )

    def get(self, task_id: str) -> dict[str, Any] | None:
        with self._lock, self._connect() as connection:
            row = connection.execute("SELECT * FROM download_tasks WHERE task_id=?", (task_id,)).fetchone()
        return dict(row) if row else None
