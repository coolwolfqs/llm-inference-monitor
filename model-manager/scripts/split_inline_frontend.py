#!/usr/bin/env python3
"""Idempotently extract the model-manager inline application script."""

from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
INDEX = ROOT / "index.html"
TARGET = ROOT / "static" / "js" / "model-manager.js"
OPEN = "<script>\n"
CLOSE = "</script>"
TAG = '<script src="static/js/model-manager.js?v=3.2.0"></script>'


def main() -> None:
    html = INDEX.read_text(encoding="utf-8")
    if TAG in html:
        return
    start = html.find(OPEN)
    if start < 0:
        raise SystemExit("inline application script not found")
    body_start = start + len(OPEN)
    end = html.find(CLOSE, body_start)
    if end < 0:
        raise SystemExit("inline application script is unterminated")
    script = html[body_start:end].rstrip() + "\n"
    if "function refresh()" not in script or "function showDeploy" not in script:
        raise SystemExit("refusing to extract an unexpected script block")
    TARGET.parent.mkdir(parents=True, exist_ok=True)
    TARGET.write_text(script, encoding="utf-8")
    INDEX.write_text(html[:start] + TAG + html[end + len(CLOSE):], encoding="utf-8")


if __name__ == "__main__":
    main()
