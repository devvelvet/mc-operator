# Web dashboard

mc-operator ships with an embedded ArgoCD-inspired dashboard. The HTML, CSS,
and JS are baked into the Go binary via `embed.FS`, so the daemon is fully
self-contained — no external file dependencies, no separate frontend server.

Open it at the address you passed to `--addr` (default `:8080`):

```
http://localhost:8080
```

## Layout

```
┌──────────────────────────────────────────────────────────────┐
│  ▣ MC   mc-operator                          ●live          │  ← topbar
│         Minecraft GitOps control plane                        │
├──────────────────────────────────────────────────────────────┤
│  Servers                                          [refresh]   │
│  ┌──────────────┐ ┌──────────────┐ ┌──────────────┐          │
│  │ lobby :25566 │ │ survival     │ │ creative     │          │
│  │ ●Synced      │ │ ●OutOfSync   │ │ ●Progressing │          │  ← server cards
│  │ ●Healthy     │ │ ●Missing     │ │ ●Progressing │          │
│  │ image: …     │ │ image: …     │ │ image: …     │          │
│  │ commit: abc  │ │ commit: -    │ │ commit: def  │          │
│  │       [sync] │ │       [sync] │ │       [sync] │          │
│  └──────────────┘ └──────────────┘ └──────────────┘          │
├──────────────────────────────────────────────────────────────┤
│  Live events                                    [clear]       │
│  01:23:45  reconcile   observed 3 servers                     │
│  01:23:46  deploy      [lobby] jar pipeline: building image   │  ← SSE log
│  01:23:50  deploy      [lobby] image built: mc-lobby:abc      │
│  01:23:51  deploy      [lobby] new container started          │
│  01:23:58  reconcile   synced: lobby                          │
└──────────────────────────────────────────────────────────────┘
```

## Server cards

Each card shows one server:

- **Name** — from the manifest
- **Port** — published host port (`:25566`)
- **Sync badge** — `Synced` / `OutOfSync` / `Progressing` / `Failed` /
  `Unknown`. Color-coded green/yellow/blue/red/gray
- **Health badge** — `Healthy` / `Degraded` / `Missing` / `Progressing` /
  `Unknown`. Color-coded the same way
- **Image** — current image reference
- **Commit** — first 7 chars of the last deployed git sha (or `-`)
- **sync button** — triggers a manual JAR pipeline run for that server

Click anywhere on the card (except the sync button) to open the **detail
drawer**.

### Sync button

Calls `POST /api/v1/servers/{name}/sync` and immediately fetches the
updated state. The request itself returns 202 in milliseconds; the actual
deploy runs in a background goroutine and progress streams back via SSE.

## Detail drawer

Slides in from the right when you click a card. Sections:

### State

- `sync` — current sync status (with badge)
- `health` — current health status (with badge)
- `current image` — image reference of the running container
- `previous image` — previously-deployed image (used for rollback target)
- `last commit` — last deployed git revision
- `port` — published host port

### Spec

- `type` — server flavor (paper / spigot / etc.)
- `version` — Minecraft version
- `memory` — `resource.memoryMB`
- `plugins` — count of plugins declared in manifest

### History

Per-server deploy history (newest first). Each row:

- Timestamp
- Kind (`deploy` / `config` / `rollback` / `sync`)
- Status (`success` / `failed` / `in_progress`) — color-coded
- Message — brief description or error reason

If the entry has `source: jenkins`, the trigger label (`build #42`) is
visible in the row. If the entry has drift entries, they appear inline.

## Live events panel

Bottom panel shows the SSE stream from `/api/v1/events`. New events appear
at the top; events are kept up to a client-side cap (default 100). Each
line:

```
HH:MM:SS  type    [server] message
```

The connection state is shown in the topbar pill:

- **`live`** (green) — SSE connected, events streaming
- **`disconnected`** (red) — connection lost; the page will auto-reconnect
- **`connecting...`** (gray) — initial state before first message

The dashboard polls `/api/v1/servers` every 30 seconds as a safety net in
case it misses an SSE event.

## Manual refresh

The `refresh` button forces an immediate fetch of `/api/v1/servers` and
`/api/v1/history`. Useful when you want to confirm the latest state without
waiting for the next periodic poll.

## Theming

The dashboard uses a single dark CSS file with CSS variables at the top of
[`internal/api/static/style.css`](../internal/api/static/style.css):

```css
:root {
  --bg: #0b0f14;
  --panel: #111820;
  --accent: #4ea1ff;
  --synced: #3fb950;
  --outofsync: #f0b72f;
  --failed: #f85149;
  ...
}
```

Override these in a fork or with a custom CSS file served at `/style.css`
to retheme.

## What the dashboard does NOT have (yet)

- **Authentication** — anyone with network access to the dashboard can use it.
  Front it with a reverse proxy (nginx, Caddy, Traefik) or a VPN if it's
  exposed beyond your laptop.
- **Edit-in-place** — you can't edit the manifest from the UI. The manifest
  is the source of truth; edit it in Git.
- **Realtime container metrics** — CPU/memory/players per server is not
  shown. Pair with cAdvisor or a Minecraft-aware exporter for that.
- **Multi-host** — the dashboard shows one mc-operator's worth of servers.
  Multi-host deployments need a per-host instance.

These are tracked in [roadmap.md](roadmap.md).
