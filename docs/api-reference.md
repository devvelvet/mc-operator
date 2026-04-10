# HTTP API reference

mc-operator's API is rooted at `/api/v1`. The dashboard at `/` is a
single-page app that calls these endpoints. The same endpoints are intended
to be called from CI systems (Jenkins / GitHub Actions / etc).

## Authentication

| Endpoint group | Auth |
|---|---|
| `GET /api/v1/healthz`, `/servers*`, `/manifest`, `/history*`, `/events` | None — assumes the dashboard is gated by external means (reverse proxy, VPN, etc.) |
| `POST /api/v1/servers/{name}/sync` | None — same assumption |
| `POST /api/v1/triggers/jenkins` | **Bearer token** (`Authorization: Bearer <token>`) — required when `--jenkins-token` is set, endpoint returns 503 otherwise |

## Endpoints

### `GET /api/v1/healthz`

Liveness probe. Always returns 200 if the daemon is running.

```json
{
  "status": "ok",
  "time": "2026-04-11T01:23:45.678Z"
}
```

### `GET /api/v1/servers`

Snapshot of every server in the state store.

```bash
curl http://localhost:8080/api/v1/servers
```

```json
{
  "count": 2,
  "servers": [
    {
      "name": "lobby",
      "currentImage": "registry.example.com/mc-lobby:42",
      "previousImage": "registry.example.com/mc-lobby:41",
      "lastCommit": "abc123def",
      "lastDeployedAt": "2026-04-11T01:00:00Z",
      "port": 25566,
      "sync": "Synced",
      "health": "Healthy"
    },
    { "name": "survival", "...": "..." }
  ]
}
```

### `GET /api/v1/servers/{name}`

Combined `{state, spec}` view for a single server. The `spec` is pulled
from the currently-loaded manifest.

```json
{
  "state": {
    "name": "lobby",
    "currentImage": "...",
    "sync": "Synced",
    "health": "Healthy"
  },
  "spec": {
    "name": "lobby",
    "type": "paper",
    "version": "1.20.4",
    "port": 25566,
    "resource": { "memoryMB": 2048 },
    "plugins": [ ... ]
  }
}
```

Returns 404 if the server is not in the state store.

### `GET /api/v1/servers/{name}/history`

Per-server deploy history (newest first), drawn from the in-memory ring
buffer.

```json
[
  {
    "server": "lobby",
    "kind": "deploy",
    "status": "success",
    "image": "registry.example.com/mc-lobby:42",
    "source": "jenkins",
    "trigger": "build #42 (mc-lobby-build)",
    "drift": null,
    "startedAt": "2026-04-11T01:00:00Z"
  }
]
```

### `GET /api/v1/history`

Full history ring buffer across all servers (oldest first by insertion).
Default capacity is 200; configure via the api package internals if needed.

### `POST /api/v1/servers/{name}/sync`

Trigger a manual sync of a single server. Equivalent to clicking the
"sync" button on the dashboard card.

The handler is **asynchronous** — it spawns a goroutine with
`context.Background()` and returns 202 Accepted immediately. Result and
errors surface via the SSE stream and history.

```bash
curl -X POST http://localhost:8080/api/v1/servers/lobby/sync
```

```json
{ "server": "lobby", "status": "queued" }
```

| Status | Meaning |
|---|---|
| 202 | Sync queued; watch SSE for progress |
| 404 | Server not found in state store |
| 503 | Manual sync not configured (shouldn't happen in normal operation) |

### `GET /api/v1/manifest`

The currently-loaded manifest as JSON.

### `GET /api/v1/events` (Server-Sent Events)

Live event stream. The connection stays open and emits events as they
happen. Use an `EventSource` in the browser or `curl -N` to consume.

```bash
curl -N http://localhost:8080/api/v1/events
```

```
: connected

event: reconcile
data: {"type":"reconcile","timestamp":"...","message":"observed 3 servers"}

event: deploy
data: {"type":"deploy","timestamp":"...","server":"lobby","message":"jar pipeline: building image"}

event: reconcile
data: {"type":"reconcile","timestamp":"...","message":"synced: lobby"}

: ping
```

#### Event types

| Type | Meaning | Source |
|---|---|---|
| `info` | Informational message | Daemon startup, manifest reload, etc. |
| `reconcile` | Reconciler state transition | `markProgressing`, `markSynced`, `markFailed` |
| `deploy` | Pipeline operation | `pipeline.Runner.emit` (build/pull/start/healthcheck) |
| `sync` | Manual or external trigger received | Dashboard sync button, Jenkins webhook |
| `drift` | Strict-mode drift rejection | Jenkins handler when `pipeline.DriftError` is returned |
| `error` | Anything that needs operator attention | Pipeline failures, watcher errors |

#### Heartbeats

The server emits `: ping` (an SSE comment) every 20 seconds to prevent
intermediaries from timing out idle connections. Comments are ignored by
EventSource consumers.

### `POST /api/v1/triggers/jenkins`

Jenkins webhook entry point. See [jenkins-integration.md](jenkins-integration.md)
for the full guide.

```bash
curl -X POST http://localhost:8080/api/v1/triggers/jenkins \
  -H "Authorization: Bearer $MC_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "server":   "lobby",
    "image":    "registry.example.com/mc-lobby:42",
    "revision": "abc123",
    "buildId":  "42",
    "jobName":  "mc-lobby-build",
    "strict":   true,
    "configOverlay": true
  }'
```

| Status | Meaning |
|---|---|
| 202 | Deploy queued; watch SSE for progress |
| 400 | Body invalid or `server` field missing |
| 401 | Bearer token missing or wrong |
| 404 | Server name not in manifest |
| 503 | Endpoint disabled (no `--jenkins-token` configured) |

Drift rejections in strict mode do **not** return 409 from this endpoint —
because the deploy runs in a goroutine, the HTTP response has already been
sent (202) by the time drift is detected. The drift instead surfaces as a
`drift` SSE event and a `failed` history record. Jenkins jobs can poll
`/api/v1/servers/{name}/history` to see the result.

## CORS

mc-operator does not enable CORS by default. The dashboard is served from
the same origin as the API, so cross-origin requests aren't needed for the
intended use case. If you want to call the API from a separate frontend,
front mc-operator with a reverse proxy that adds the headers.

## Rate limiting

Not implemented. The expected access pattern is a small number of operators
+ a small number of CI jobs, all internal. If you expose mc-operator to the
public internet, put it behind something that rate-limits.
