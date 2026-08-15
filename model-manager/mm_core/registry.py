"""Persistent artifact identity registry.

The filesystem path is mutable product data; an artifact UID is not.  The
registry keys files by their filesystem identity when possible, allowing a
rename or move inside the model root without invalidating preferences, audit
records, or API links.
"""

from __future__ import annotations

import sqlite3
import threading
import time
import uuid
from pathlib import Path


class ArtifactRegistry:
    def __init__(self, database: Path) -> None:
        self.database = database
        self._lock = threading.RLock()
        database.parent.mkdir(parents=True, exist_ok=True)
        with self._connect() as connection:
            connection.executescript(
                """
                PRAGMA journal_mode=WAL;
                PRAGMA synchronous=NORMAL;
                CREATE TABLE IF NOT EXISTS artifacts (
                    uid TEXT PRIMARY KEY,
                    device INTEGER NOT NULL,
                    inode INTEGER NOT NULL,
                    relative_path TEXT NOT NULL,
                    kind TEXT NOT NULL DEFAULT 'unknown',
                    first_seen REAL NOT NULL,
                    last_seen REAL NOT NULL,
                    missing_since REAL
                );
                CREATE UNIQUE INDEX IF NOT EXISTS artifacts_fs_identity
                    ON artifacts(device, inode);
                CREATE INDEX IF NOT EXISTS artifacts_relative_path
                    ON artifacts(relative_path);
                """
            )

    def _connect(self) -> sqlite3.Connection:
        return sqlite3.connect(self.database, timeout=5.0)

    def identify(self, path: Path, relative_path: str, kind: str) -> str:
        stat = path.stat()
        now = time.time()
        with self._lock, self._connect() as connection:
            row = connection.execute(
                "SELECT uid FROM artifacts WHERE device=? AND inode=?",
                (stat.st_dev, stat.st_ino),
            ).fetchone()
            if row:
                uid = str(row[0])
                connection.execute(
                    "UPDATE artifacts SET relative_path=?, kind=?, last_seen=?, missing_since=NULL WHERE uid=?",
                    (relative_path, kind, now, uid),
                )
                return uid
            uid = f"mdl_{uuid.uuid4().hex}"
            connection.execute(
                "INSERT INTO artifacts(uid,device,inode,relative_path,kind,first_seen,last_seen) VALUES(?,?,?,?,?,?,?)",
                (uid, stat.st_dev, stat.st_ino, relative_path, kind, now, now),
            )
            return uid

    def mark_missing(self, seen_uids: set[str]) -> None:
        now = time.time()
        with self._lock, self._connect() as connection:
            if seen_uids:
                placeholders = ",".join("?" for _ in seen_uids)
                connection.execute(
                    f"UPDATE artifacts SET missing_since=COALESCE(missing_since, ?) WHERE uid NOT IN ({placeholders})",
                    (now, *sorted(seen_uids)),
                )
            else:
                connection.execute(
                    "UPDATE artifacts SET missing_since=COALESCE(missing_since, ?)",
                    (now,),
                )
