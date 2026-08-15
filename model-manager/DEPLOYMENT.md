# Model Manager deployment

The catalog uses `/data/model-manager/catalog.sqlite3` for immutable artifact
UIDs. Keep this file across releases and host rebuilds. Moving or renaming an
artifact on the same filesystem preserves its UID; API clients should send the
opaque `id`, never a basename or path. Runtime and catalog changes are exposed
through `GET /api/events` as server-sent events, with normal GET endpoints as
the source of truth.

Authenticated mutations are recorded in the same database without request
bodies, API keys, model prompts, or generated content. `GET /api/operations`
returns the most recent sanitized records and each mutation response includes
`X-Operation-Id` for correlation.

Interactive deployments use `POST /api/deployments`. The endpoint returns a
durable task immediately; a loopback worker then invokes the existing checked
`/api/models/deploy` path so validation, systemd readiness, serialization and
rollback remain single-sourced. Running atomic switch operations cannot be
cancelled; this is intentional because terminating systemd changes mid-flight
is less safe than allowing the bounded operation to finish or roll back.

The primary UI is a Vue 3 + TypeScript application in `frontend/`. Production
assets are built into `static/vue/`; the FastAPI root automatically serves that
entry point when present and falls back to the legacy `index.html` otherwise.
The legacy shell and `static/js/model-manager.js` remain available during the
migration window but are no longer the production entry point.

Build the UI before packaging a release:

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm build
```

The service is a node-local control plane and must run as one API worker. The
dashboard is its authentication gateway and reverse proxy; port 8093 binds to
loopback only.

## Release layout

For a new node, install immutable releases below
`/opt/model-manager/releases/<release-id>` and point
`/opt/model-manager/current` at the selected release. The current server keeps
`/home/draco/model-manager` as `MODEL_MANAGER_APP_DIR` for compatibility.

Persistent data is outside the release:

- `/data/models`: model artifacts and `.trash`
- `/data/model-manager/model-metadata-overrides.json`: reviewed taxonomy overrides
- `/data/inference-hub/state`: desired engine and quick-switch state
- `/data/inference-control-plane/.env`: admin credentials

## Install or upgrade

1. Create a Python 3.12 virtual environment and install `requirements.txt`.
2. Build the Vue frontend and copy the release to a new immutable release directory.
3. Run `python -m unittest discover -s tests -v`.
4. Validate and install `deploy/model-manager.sudoers` with mode `0440`; it
   deliberately replaces the legacy rules that allowed generic `rm`/`mv`.
5. Install `deploy/model-manager.service`, run `systemctl daemon-reload`, then
   restart `model-manager.service`.
6. Verify `http://127.0.0.1:8093/api/health` before switching traffic.

Do not configure multiple Uvicorn workers: deployment mutations are serialized
inside this node-local process and the catalog cache is intentionally local.

## Taxonomy overrides

The optional overrides file uses stable paths relative to `/data/models`:

```json
{
  "models": {
    "vendor/model.gguf": {
      "family": "Reviewed family",
      "capabilities": ["Reasoning"],
      "tags": ["Production-approved"]
    }
  }
}
```

Embedded GGUF/HF facts are preserved in the API together with confidence,
evidence and conflicts. Filename parsing is only a fallback.
