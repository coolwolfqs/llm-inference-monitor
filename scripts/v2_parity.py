#!/usr/bin/env python3
"""Read-only contract probe for the Python and Go dashboard authorities."""
import json
import sys
import urllib.request


def fetch(url):
    with urllib.request.urlopen(url, timeout=5) as response:
        return json.load(response)


def get(obj, *keys):
    for key in keys:
        if isinstance(obj, dict):
            obj = obj.get(key)
        elif isinstance(obj, list) and isinstance(key, int) and 0 <= key < len(obj):
            obj = obj[key]
        else:
            return None
    return obj


def main():
    host = sys.argv[1] if len(sys.argv) > 1 else "127.0.0.1"
    py = fetch(f"http://{host}:8081/api/v2/snapshot")
    go = fetch(f"http://{host}:9092/api/v2/snapshot")
    required = [
        ("sections.system", ("sections", "system")),
        ("sections.gpus.items", ("sections", "gpus", "items")),
        ("sections.inference.runtime", ("sections", "inference", "runtime")),
        ("sections.inference.llm", ("sections", "inference", "llm")),
    ]
    failures = []
    for label, path in required:
        if get(py, *path) is None:
            failures.append(f"python missing {label}")
        if get(go, *path) is None:
            failures.append(f"go missing {label}")
    py_gpu = get(py, "sections", "gpus", "items", 0) or {}
    go_gpu = get(go, "sections", "gpus", "items", 0) or {}
    comparable = ("vendor", "encoder_name", "decoder_name", "fan_speed", "enc_util", "dec_util")
    values = {key: (py_gpu.get(key), go_gpu.get(key)) for key in comparable}
    print(json.dumps({"status": "ok" if not failures else "fail", "missing": failures, "gpu_fields": values}, ensure_ascii=False, indent=2))
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
