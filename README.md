# mc-operator

Declarative Minecraft server infrastructure. Git-backed desired state, Docker-based runtime, ArgoCD-style web dashboard.

## Layout

```
mc-operator/
├── cmd/
│   ├── mc-operator/      # GitOps daemon + web dashboard
│   └── mc-imagegen/      # Standalone Dockerfile/image generator
├── pkg/                  # Public libraries (importable by other projects)
│   ├── mctypes/          # Shared types (ServerSpec, Manifest, SyncStatus)
│   ├── mcimage/          # Dockerfile templates + build context + Docker SDK builder
│   ├── manifest/         # servers.yaml parser + validator
│   ├── rcon/             # Context-aware RCON client wrapper
│   └── proxy/            # Velocity velocity.toml generator
├── internal/             # Daemon-private code
│   ├── api/              # HTTP API + embedded web dashboard (chi + SSE + history)
│   │   └── static/       # Embedded dashboard assets (HTML/CSS/JS)
│   ├── docker/           # Engine API client wrapper (build, run, observe, copy)
│   ├── download/         # On-disk plugin URL cache (sha256-verified)
│   ├── gitops/           # Watcher + differ + reconciler orchestrator
│   ├── health/           # TCP health checks for the jar pipeline
│   ├── pipeline/         # Config-reload + jar-rebuild pipelines
│   └── state/            # state.json store
└── examples/
    └── servers.yaml      # Sample manifest (proxy + 3 paper servers)
```

## Quick start

```bash
# 1. Render a Dockerfile for a Paper 1.20.4 server
go run ./cmd/mc-imagegen render --type paper --version 1.20.4 --memory 2048

# 2. Validate a manifest
go run ./cmd/mc-operator validate examples/servers.yaml

# 3. Run the daemon (observe-only mode if Docker is unreachable)
go run ./cmd/mc-operator serve \
  --manifest examples/servers.yaml \
  --addr :8080
# → open http://localhost:8080

# 4. Run with full GitOps loop
go run ./cmd/mc-operator serve \
  --manifest ./servers.yaml \
  --repo .                       \
  --proxy-config /opt/velocity/velocity.toml \
  --rcon-host localhost          \
  --rcon-password "$MC_RCON_PASSWORD" \
  --interval 30s
```

## Daemon flags

| Flag | Purpose |
|------|---------|
| `--addr` | HTTP listen address (default `:8080`) |
| `--manifest` | Path to `servers.yaml` |
| `--repo` | Local Git working copy to watch (enables auto-deploy on push) |
| `--state` | Where to persist runtime state (default `~/.mc-operator/state.json`) |
| `--docker-host` | Docker daemon override (default uses env / OS default) |
| `--rcon-host` | Host to dial for RCON connections |
| `--rcon-password` | RCON password (or env `MC_RCON_PASSWORD`) |
| `--proxy-config` | Where to write the auto-generated `velocity.toml` |
| `--cache-dir` | Plugin URL download cache (default next to state file) |
| `--interval` | Reconcile / Git poll interval |

## Pipelines

- **Config pipeline**: when only config files change, mc-operator copies them
  into the running container's `/server/config` and triggers an in-game
  `reload` over RCON. No restart, no downtime.

- **JAR pipeline**: when a jar or the manifest itself changes, mc-operator
  builds a new image (Docker SDK), stops the old container, starts a fresh
  one on the shared `mc-network`, runs a TCP health check, and rolls back
  to the previous image if the new one fails to come healthy.

- **Proxy reconciliation**: every observe pass regenerates `velocity.toml`
  from the manifest's enabled servers. The generated file is fully
  reproducible (sorted server list).

## Web dashboard

ArgoCD-inspired single-page UI served from the embedded `internal/api/static`
filesystem. Features:

- Server card grid with sync/health badges
- Click-through detail drawer (state, spec, history)
- Manual `sync` button per server
- Live event stream over Server-Sent Events
- Deploy history (in-memory ring buffer)

## API

| Method | Path | Notes |
|--------|------|-------|
| GET | `/api/v1/healthz` | Liveness |
| GET | `/api/v1/servers` | Snapshot of all server states |
| GET | `/api/v1/servers/{name}` | Combined `{state, spec}` view |
| GET | `/api/v1/servers/{name}/history` | Per-server deploy history |
| POST | `/api/v1/servers/{name}/sync` | Trigger a manual jar pipeline run (async) |
| GET | `/api/v1/history` | Full deploy history ring buffer |
| GET | `/api/v1/manifest` | Currently-loaded manifest |
| GET | `/api/v1/events` | Server-Sent Events stream |
| POST | `/api/v1/triggers/jenkins` | Jenkins webhook (Bearer auth, see below) |

## Jenkins integration

mc-operator can be driven from a Jenkins pipeline. The flow is:

1. Jenkins builds the server image (using `mc-imagegen render` to produce a
   Dockerfile, then `docker build` + `docker push`).
2. Jenkins POSTs to `/api/v1/triggers/jenkins` with the image reference and
   build metadata.
3. mc-operator pulls the image, inspects its labels for drift against the
   manifest spec, performs the rollover + healthcheck, and rolls back to the
   previous image on failure.

### Daemon flag

```bash
./mc-operator serve \
  --manifest /etc/mc-operator/servers.yaml \
  --jenkins-token "$(openssl rand -hex 32)"
```

The token can also be supplied via `MC_JENKINS_TOKEN`. When unset the endpoint
returns 503 — there is no anonymous mode.

### Webhook payload

```json
POST /api/v1/triggers/jenkins
Authorization: Bearer <token>
Content-Type: application/json

{
  "server":        "lobby",
  "image":         "registry.example.com/mc-lobby:42",
  "revision":      "abc123def",
  "buildId":       "42",
  "jobName":       "mc-lobby-build",
  "strict":        true,
  "configOverlay": true
}
```

| Field | Type | Notes |
|---|---|---|
| `server` | string | Required. Manifest server name. |
| `image`  | string | Optional. Prebuilt image to pull. Empty → mc-operator builds from local files. |
| `revision` | string | Optional. Source commit; recorded as `lastCommit`. |
| `buildId` / `jobName` | string | Optional. Shown in dashboard history as `build #42 (mc-lobby-build)`. |
| `strict` | bool | Optional. Reject deploy with 409 if image labels disagree with manifest spec. |
| `configOverlay` | bool | Optional. After deploy, apply repo configs (`spec.ConfigDir`) on top of the image and trigger an in-server reload. |

### Drift handling

When mc-operator pulls a Jenkins image it inspects the `mc-operator.type`
and `mc-operator.version` labels (set automatically by `mc-imagegen`). If they
disagree with the manifest:

- **Lenient mode (default)** — deploy proceeds, drift is recorded in the
  history record (`drift` field) and shown in the dashboard.
- **Strict mode** (`"strict": true`) — deploy is aborted with 409 Conflict
  before any container is touched. The history record marks the failure with
  the specific drift list.

### Config overlay

`"configOverlay": true` makes mc-operator perform a second pass after the
container is healthy: every file under `spec.ConfigDir` is copied into the
container and an RCON `reload` is issued. This means Jenkins-built images can
ship with default configs while environment-specific configs (server.properties
overrides, plugin configs) live in the mc-operator repo and stay in sync.

### Example Jenkinsfile

See [`examples/Jenkinsfile`](examples/Jenkinsfile) for a complete declarative
pipeline that fetches Paper, builds the image with `mc-imagegen`, pushes it,
and triggers an mc-operator deploy with strict drift checking + config overlay.

## Tests

```bash
go test ./...
```

Covers: differ classification, manifest validation, velocity TOML generation,
Dockerfile template rendering / version-based JRE selection.

## Status

Working pieces:
- Type system, manifest parser, image generator, Dockerfile templates
- Docker SDK image builder + container manager + observer
- RCON client (gorcon-backed, context-aware)
- Git watcher + diff classifier
- Reconciler orchestrating config + jar pipelines
- Velocity proxy auto-generation
- HTTP API with SSE + embedded dashboard (sync trigger, history, detail view)
- Plugin URL download cache with SHA-256 verification
- TCP health checks + automatic rollback on failure

Future:
- Smarter "which servers are affected by this manifest change" diffing
  (currently a manifest edit conservatively rebuilds all gameplay servers)
- RCON-based deep health checks
- Multi-host support via per-server Docker contexts
- World data backups before deploy
- Web dashboard auth

## License

MIT — see [LICENSE](LICENSE).
