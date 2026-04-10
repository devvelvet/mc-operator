# Getting started

## Prerequisites

- **Go 1.23+** — required to build mc-operator
- **Docker Engine** (or Docker Desktop) — required to actually deploy servers; the
  daemon falls back to "observe-only" mode without Docker so you can still play
  with the dashboard on a laptop
- **A Minecraft server jar** — Paper / Spigot / Vanilla / etc. mc-operator does
  not bundle one

## Build the binaries

```bash
git clone https://github.com/devvelvet/mc-operator.git
cd mc-operator
go build ./...
```

This produces nothing visible because Go's `./...` doesn't write binaries to
the working directory. Build the two CLIs explicitly:

```bash
go build -o mc-operator ./cmd/mc-operator
go build -o mc-imagegen ./cmd/mc-imagegen
```

On Windows append `.exe`:

```powershell
go build -o mc-operator.exe ./cmd/mc-operator
go build -o mc-imagegen.exe ./cmd/mc-imagegen
```

## Render a Dockerfile (no daemon needed)

```bash
./mc-imagegen render --type paper --version 1.20.4 --memory 2048
```

This is useful as a sanity check that your Go install works and as a building
block when you want to use mc-operator's image format from your own CI scripts.

## Validate a manifest

```bash
./mc-operator validate examples/demo.yaml
# → ok: 1 servers, proxy.enabled=false
```

## Run the daemon (observe-only)

```bash
./mc-operator serve --manifest examples/demo.yaml --addr :8080
# → open http://localhost:8080 in your browser
```

If Docker is not reachable, the daemon logs `docker: ping failed,
observe-only mode` and continues. Server cards will show `Unknown / Unknown`
because there is no Docker host to inspect.

## Run a real Minecraft server end-to-end

This is the full happy path. It requires Docker and ~50 MB of bandwidth for
the Paper jar.

```bash
# 1. Download a Paper server jar to the convention path
mkdir -p jars
curl -sLo jars/paper-1.20.4.jar \
  https://api.papermc.io/v2/projects/paper/versions/1.20.4/builds/499/downloads/paper-1.20.4-499.jar

# 2. Start the daemon with the demo manifest (single Paper server, port 25600)
./mc-operator serve --manifest examples/demo.yaml --addr :8080

# 3. In another terminal, click the "sync" button on the dashboard
#    OR trigger the manual sync via curl:
curl -X POST http://localhost:8080/api/v1/servers/mc-demo/sync
```

Within ~15 seconds you should see:

- A new Docker container `mc-demo` running on port 25600
- The dashboard card flip from `OutOfSync / Missing` to `Synced / Healthy`
- A success entry in the deploy history (`source: manual`)

Connect with a Minecraft client to `localhost:25600` and you're in.

## Where state is kept

mc-operator persists runtime state to `~/.mc-operator/state.json` by default.
Override with `--state /custom/path/state.json`. The state file holds:

- Per-server `currentImage` / `previousImage` (for rollback)
- Last deployed timestamp
- Sync and health status
- Allocated ports (for servers that don't pin a port in the manifest)

This file must NOT be committed to Git — the included `.gitignore` covers it.

## Next steps

- Read [manifest-reference.md](manifest-reference.md) to learn the full
  `servers.yaml` schema
- Read [jenkins-integration.md](jenkins-integration.md) if you want CI to drive
  deployments
- Read [pipelines.md](pipelines.md) to understand what happens during a deploy
