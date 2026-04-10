# Development guide

For contributors and anyone wanting to dig into the code.

## Setup

```bash
git clone https://github.com/devvelvet/mc-operator.git
cd mc-operator
go mod download
go build ./...
go test ./...
```

Required:
- Go 1.23 or later
- Docker (for the integration paths; `go test ./...` runs without Docker)

Optional:
- A working Minecraft server jar (for full end-to-end testing)
- A local Docker registry (for Jenkins integration testing)

## Repository layout

See [architecture.md](architecture.md) for the full breakdown. The short
version:

```
cmd/         ← CLI entry points (mc-operator daemon, mc-imagegen)
pkg/         ← public libraries (importable from third-party projects)
internal/    ← daemon-private packages
examples/    ← sample manifests + Jenkinsfile
docs/        ← this wiki
```

The rule: `pkg/*` may not import `internal/*`. The opposite is fine.
`pkg/*` packages should be self-contained enough that someone could
`go get github.com/devvelvet/mc-operator/pkg/mcimage` and use it without
pulling in the daemon.

## Build

```bash
go build -o mc-operator ./cmd/mc-operator
go build -o mc-imagegen ./cmd/mc-imagegen
```

For cross-compiling (e.g. building Linux binaries from Windows for the
Jenkins agent):

```bash
GOOS=linux GOARCH=amd64 go build -o mc-imagegen-linux ./cmd/mc-imagegen
```

## Test

### Unit tests

```bash
go test ./...
```

What's covered today:

| Package | Tests |
|---|---|
| `internal/gitops` | Diff classifier, summary → pipeline selection, MapServersByConfig |
| `pkg/manifest` | Parse, validate (apiVersion, unknown type, duplicate ports, missing memory) |
| `pkg/mcimage` | Dockerfile rendering for paper 1.20 / 1.18, velocity, validation failures, version → JRE major mapping |
| `pkg/proxy` | FromManifest, TOML serialization, proxy server exclusion, GenerateForwardingSecret randomness |

What's NOT covered by unit tests (yet):

- `internal/state` (state store)
- `internal/api` (HTTP handlers, history ring buffer, SSE broker)
- `internal/pipeline` (config + jar pipeline orchestration)
- `internal/docker` (Docker SDK wrappers — these are tested integration-style)
- `internal/download` (URL cache)
- `internal/health` (TCP health check)

### Integration tests

There are no automated integration tests yet — the integration paths are
covered manually via the test scripts in this conversation. See
[test-coverage.md](test-coverage.md) for the honest list.

If you want to run an end-to-end smoke test by hand, the rough recipe is:

```bash
# 1. Get a Paper jar
mkdir -p jars
curl -sLo jars/paper-1.20.4.jar \
  https://api.papermc.io/v2/projects/paper/versions/1.20.4/builds/499/downloads/paper-1.20.4-499.jar

# 2. Build and run the daemon
go build -o mc-operator ./cmd/mc-operator
./mc-operator serve --manifest examples/demo.yaml --addr :8080

# 3. In another terminal, trigger the sync
curl -X POST http://localhost:8080/api/v1/servers/mc-demo/sync

# 4. Verify
docker ps --filter name=mc-demo
curl http://localhost:8080/api/v1/servers/mc-demo
```

## Code style

- **No commits without `go vet ./...` and `go test ./...` clean.**
- **No new dependencies without justification.** mc-operator's go.mod is
  intentionally short. The big deps (`docker/docker`, `go-git`, `chi`,
  `cobra`, `gorcon`) each pull their weight.
- **Comment the *why*, not the *what*.** The current codebase has comments
  on non-obvious decisions (port translation between daemon-side and
  container-side hostnames; embed.FS path layout; SSE event type
  separation). Match that style.
- **Don't import `internal/*` from `pkg/*`.** Use an interface in `pkg/*`
  and let the daemon wire the implementation in `cmd/mc-operator/main.go`.

## Adding a new pipeline

If you wanted to add, say, a backup pipeline (snapshot the world data
before deploy), the rough plan would be:

1. Add a new `internal/pipeline/backup.go` with a `RunBackup` method on
   `*Runner`. Define a `BackupPipelineInput` struct.

2. Add a new method to `gitops.PipelineExecutor` interface in
   `internal/gitops/reconciler.go`:
   ```go
   RunBackup(ctx context.Context, in pipeline.BackupPipelineInput) error
   ```

3. Call it from `runJARWithOpts` before stopping the old container.

4. Add a `BackupBeforeDeploy bool` field to `gitops.SyncOptions` and to
   `api.JenkinsRequest` so callers can opt in.

5. Wire the new option in `cmd/mc-operator/main.go` if it needs flags.

This is the same shape as the existing `ConfigOverlay` flow.

## Adding a new server type

If you want to support, say, BungeeCord:

1. Add `ServerTypeBungeeCord ServerType = "bungeecord"` in
   `pkg/mctypes/types.go`.

2. Update `ServerType.Known()` and (if it's a proxy) `ServerType.IsProxy()`.

3. Update `pkg/mcimage/dockerfile.go`:
   - The `dockerfileTmpl` template if BungeeCord needs a different EXPOSE / EULA / etc.
   - The version-to-JRE mapping if needed.

4. Update `pkg/rcon/rcon.go` `DefaultReloadCommand` to return the right
   reload command for the new type.

5. Update `pkg/proxy/velocity.go` if BungeeCord should be excluded from
   the Velocity `[servers]` section (it would be).

6. Add a test in `pkg/mcimage/dockerfile_test.go` to lock the new behavior.

## Releasing

There's no release process yet. When there is, it will be tag-driven via
GitHub Actions and produce binaries for the common OS/arch matrix.

## Useful tooling

```bash
# Tidy go.mod (remove unused, add missing)
go mod tidy

# Find dead code
go vet ./...

# Run tests with race detector
go test -race ./...

# Run a single package's tests with verbose output
go test -v ./pkg/proxy/...

# Quick run without building first
go run ./cmd/mc-imagegen render --type paper --version 1.20.4
go run ./cmd/mc-operator validate examples/demo.yaml
```
