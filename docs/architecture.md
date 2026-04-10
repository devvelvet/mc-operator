# Architecture

mc-operator is a single Go binary that orchestrates Docker containers running
Minecraft servers, driven by a declarative manifest. This page explains how
the pieces fit together.

## High-level layout

```
mc-operator/
├── cmd/                  ← thin CLI wrappers
│   ├── mc-operator/      ← daemon (HTTP API + reconciler + watcher)
│   └── mc-imagegen/      ← standalone Dockerfile/image generator CLI
├── pkg/                  ← public libraries (importable from other projects)
│   ├── mctypes/          ← shared types: ServerSpec, Manifest, SyncStatus
│   ├── mcimage/          ← Dockerfile templates + Docker SDK builder
│   ├── manifest/         ← servers.yaml parser + validator
│   ├── rcon/             ← context-aware RCON client wrapper
│   └── proxy/            ← Velocity velocity.toml generator
└── internal/             ← daemon-private code
    ├── api/              ← HTTP API + embedded web dashboard (chi + SSE)
    ├── docker/           ← Docker Engine API wrapper
    ├── download/         ← URL plugin download cache (sha256 verified)
    ├── gitops/           ← reconciler + git watcher + diff classifier
    ├── health/           ← TCP health checks
    ├── pipeline/         ← config + jar pipelines
    └── state/            ← state.json store
```

The package boundary is deliberate:

- **`pkg/*` is public surface area.** It has no dependency on `internal/*`,
  so a third-party project could import `pkg/mcimage` to build Minecraft
  Dockerfiles in their own tooling without pulling in the GitOps runtime.
- **`internal/*` is the daemon's private wiring.** It depends on `pkg/*`
  freely but nothing else inside the project should import from `internal/*`.

## Dependency direction

```
cmd/mc-operator
    │
    ├──► internal/api ──────────────► internal/state
    │                                       ▲
    ├──► internal/gitops ──────────────────┤
    │       │                              │
    │       └──► internal/pipeline ────────┤
    │               │                      │
    │               ├──► internal/docker   │
    │               ├──► pkg/mcimage       │
    │               └──► pkg/rcon          │
    │
    ├──► internal/download
    └──► internal/health ──► pkg/mctypes
```

There are no cycles. Every internal package depends only on packages "above"
it in this diagram.

## The reconcile loop

mc-operator's reconciler is what makes it ArgoCD-like. The loop:

```
                    ┌──────────────────────┐
                    │ servers.yaml in Git  │  ← source of truth
                    └──────────┬───────────┘
                               │
                ┌──────────────┴───────────────┐
                │                              │
                ▼                              ▼
        ┌──────────────┐              ┌──────────────┐
        │  Git watcher │              │  Manual sync │
        │  (5s poll)   │              │  (dashboard) │
        └──────┬───────┘              └──────┬───────┘
               │                             │
               │   ┌──────────────┐          │
               └──►│  Reconciler  │◄─────────┤
                   └──────┬───────┘          │
                          │              ┌───┴──────────┐
                          │              │  Jenkins     │
                          │              │  webhook     │
                          │              └──────────────┘
                          │
                  ┌───────┴───────┐
                  ▼               ▼
            ┌──────────┐    ┌──────────┐
            │  Config  │    │   JAR    │
            │ pipeline │    │ pipeline │
            └────┬─────┘    └────┬─────┘
                 │               │
                 ▼               ▼
         ┌──────────────────────────┐
         │       Docker host        │
         │  (mc-demo, mc-lobby, …)  │
         └──────────────────────────┘
```

The reconciler has three entry points:

1. **`Reconcile(ctx, manifest)`** — observation only. Walks every server in
   the manifest and updates the state store with the actual container state
   it observes via Docker. Never starts/stops anything. Called on a timer
   (default every 30s) so the dashboard reflects external changes.

2. **`HandleChanges(ctx, manifest, summary, revision)`** — called by the git
   watcher when HEAD advances. The `DiffSummary` tells it whether the
   change requires the config pipeline (file edits only) or the jar pipeline
   (jar / manifest edits).

3. **`SyncServerOpts(ctx, manifest, name, opts)`** — called by manual dashboard
   syncs and Jenkins webhooks. `opts.Source` records who triggered the sync;
   `opts.PrebuiltImage` causes the jar pipeline to pull instead of build.

## Pipelines

Two pipelines, sharing the rollover/healthcheck/rollback machinery:

- **Config pipeline** — when only config files change. Copies the new files
  into the running container's `/server/config` via `docker cp`, then
  triggers an in-game `reload` via RCON. No restart, no downtime.

- **JAR pipeline** — when a jar or the manifest itself changes (or Jenkins
  pushes a new image). Builds (or pulls) the image, stops the old container,
  starts a fresh one, runs the TCP healthcheck, and rolls back to the
  previous image if the new one fails to come healthy in time.

See [pipelines.md](pipelines.md) for the gritty details.

## State store

State lives in `~/.mc-operator/state.json` (or `--state /custom/path`). The
store is protected by a `sync.RWMutex` and writes are atomic (write to
`.tmp` then `Rename`). It survives daemon restarts.

The state file is **not** the source of truth — `servers.yaml` is. The state
file is a cache of the last-known reality so the daemon can pick up where it
left off and so rollback can find the previous image.

## Web dashboard

The web UI is a single-page app served from an `embed.FS` baked into the Go
binary. There are no external file dependencies — the binary is fully
self-contained.

- HTML/CSS/JS in `internal/api/static/`
- HTTP routing via `chi`
- Live updates via Server-Sent Events (`/api/v1/events`)
- Manual sync trigger spawns a goroutine with `context.Background()` so the
  HTTP request returns 202 Accepted immediately while the deploy continues
  in the background — same pattern Jenkins/ArgoCD use

See [dashboard.md](dashboard.md) for the user-facing tour.

## Why no Kubernetes?

Minecraft servers are stateful, not horizontally scalable, and run as a
single JVM process per gameplay world. The Kubernetes pod model fits
poorly. mc-operator targets single-host Docker setups (the common
hobbyist / small-server-network case) and lets the operator focus on the
specific concerns of Minecraft hosting: in-game RCON reloads, plugin jars,
EULA acceptance, version-specific JRE selection, and proxy network topology.

A multi-host extension is on the [roadmap](roadmap.md) but would use Docker
contexts rather than Kubernetes.
