"""Persistent deployment task state.

Execution stays outside this module so the existing checked deployment path is
the only code allowed to mutate systemd state.  This store provides durable
state and marks work interrupted by a control-plane restart as failed.
"""

from __future__ import annotations

import json
import sqlite3
import threading
import time
import uuid
from pathlib import Path
from typing import Any


ACTIVE_STATES = ("queued", "running")


class DeploymentTaskStore:
    def __init__(self, database: Path) -> None:
        self.database = database
        self._lock = threading.RLock()
        database.parent.mkdir(parents=True, exist_ok=True)
        with self._connect() as connection:
            connection.executescript(
                """
                PRAGMA journal_mode=WAL;
                CREATE TABLE IF NOT EXISTS deployment_tasks (
                    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
                    task_id TEXT NOT NULL UNIQUE,
                    model_id TEXT NOT NULL,
                    engine TEXT NOT NULL,
                    state TEXT NOT NULL,
                    phase TEXT NOT NULL,
                    progress INTEGER NOT NULL DEFAULT 0,
                    created_at REAL NOT NULL,
                    started_at REAL,
                    finished_at REAL,
                    status_code INTEGER,
                    result_json TEXT,
                    error TEXT
                );
                CREATE INDEX IF NOT EXISTS deployment_tasks_created
                    ON deployment_tasks(created_at DESC);
                """
            )
            now = time.time()
            connection.execute(
                """UPDATE deployment_tasks
                   SET state='failed', phase='interrupted', progress=100,
                       finished_at=?, error='控制平面重启导致任务中断'
                   WHERE state IN ('queued','running')""",
                (now,),
            )

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.database, timeout=5.0)
        connection.row_factory = sqlite3.Row
        return connection

    def active(self) -> dict[str, Any] | None:
        with self._lock, self._connect() as connection:
            row = connection.execute(
                "SELECT * FROM deployment_tasks WHERE state IN ('queued','running') ORDER BY sequence DESC LIMIT 1"
            ).fetchone()
        return self._public(row) if row else None

    def create(self, model_id: str, engine: str) -> dict[str, Any]:
        task_id = f"dep_{uuid.uuid4().hex}"
        now = time.time()
        with self._lock, self._connect() as connection:
            connection.execute(
                """INSERT INTO deployment_tasks
                   (task_id,model_id,engine,state,phase,progress,created_at)
                   VALUES(?,?,?,'queued','queued',5,?)""",
                (task_id, model_id, engine, now),
            )
        return self.get(task_id) or {}

    def update(
        self,
        task_id: str,
        *,
        state: str,
        phase: str,
        progress: int,
        status_code: int | None = None,
        result: dict[str, Any] | None = None,
        error: str = "",
    ) -> None:
        now = time.time()
        terminal = state in {"succeeded", "failed", "cancelled"}
        with self._lock, self._connect() as connection:
            connection.execute(
                """UPDATE deployment_tasks SET state=?, phase=?, progress=?,
                   started_at=COALESCE(started_at,?), finished_at=?, status_code=?,
                   result_json=?, error=? WHERE task_id=?""",
                (
                    state, phase, max(0, min(int(progress), 100)), now,
                    now if terminal else None, status_code,
                    json.dumps(result, ensure_ascii=False) if result is not None else None,
                    str(error or "")[:500], task_id,
                ),
            )

    def get(self, task_id: str) -> dict[str, Any] | None:
        with self._lock, self._connect() as connection:
            row = connection.execute(
                "SELECT * FROM deployment_tasks WHERE task_id=?", (task_id,)
            ).fetchone()
        return self._public(row) if row else None

    def list(self, limit: int = 30) -> list[dict[str, Any]]:
        with self._lock, self._connect() as connection:
            rows = connection.execute(
                "SELECT * FROM deployment_tasks ORDER BY sequence DESC LIMIT ?",
                (max(1, min(int(limit), 100)),),
            ).fetchall()
        return [self._public(row) for row in rows]

    def latest_sequence(self) -> int:
        with self._lock, self._connect() as connection:
            row = connection.execute(
                "SELECT COALESCE(MAX(sequence),0) FROM deployment_tasks"
            ).fetchone()
        return int(row[0])

    def latest_signal(self) -> tuple[int, str, str, int]:
        with self._lock, self._connect() as connection:
            row = connection.execute(
                """SELECT sequence,state,phase,progress FROM deployment_tasks
                   ORDER BY sequence DESC LIMIT 1"""
            ).fetchone()
        return (
            int(row["sequence"]), str(row["state"]), str(row["phase"]), int(row["progress"])
        ) if row else (0, "", "", 0)

    @staticmethod
    def _public(row: sqlite3.Row) -> dict[str, Any]:
        item = dict(row)
        raw_result = item.pop("result_json", None)
        item["result"] = json.loads(raw_result) if raw_result else None
        return item
