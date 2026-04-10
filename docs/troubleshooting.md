# Troubleshooting

Common problems and how to fix them.

## Daemon won't start

### `open state store: ... permission denied`

The state file path isn't writable. Default is `~/.mc-operator/state.json`.
Fix: pass `--state /path/you/own/state.json` or fix permissions on
`~/.mc-operator/`.

### `manifest load warning: parse manifest: ...`

Invalid YAML in `servers.yaml`. The error includes a line number — fix the
YAML and the daemon will reload it on the next interval (or restart it).

### `manifest load warning: at least one server must be declared`

You need to declare at least one entry under `servers:`. See
[manifest-reference.md](manifest-reference.md).

### `docker: ping failed, observe-only mode`

Docker is unreachable. Causes:

- Docker Desktop not running
- `DOCKER_HOST` env points to a non-existent socket
- Permissions on `/var/run/docker.sock` (Linux)

The daemon will continue in observe-only mode — the dashboard works but no
deploys are possible.

## Sync fails immediately

### `image build: prepare build context: stat jars/paper-1.20.4.jar: no such file`

Default resolver looks for the server jar at `{--repo}/jars/{type}-{version}.jar`.
Either:

- Place a jar at that exact path
- Or pass `--repo /path/with/jars/dir`
- Or use a Jenkins trigger with `image:` set to a prebuilt image so the
  resolver isn't called

### `resolve inputs: download X: 404 Not Found`

A URL plugin in your manifest points at a dead URL. Update the URL or pin a
specific build. The example `examples/servers.yaml` deliberately uses fake
URLs to demonstrate the failure path — switch to `examples/demo.yaml` for a
working manifest.

### `image build: build failed: file not found in build context`

This was a real bug on Windows where backslashes in paths were eaten as
Dockerfile escape sequences. Fixed in [`pkg/mcimage/dockerfile.go`](../pkg/mcimage/dockerfile.go)
by normalizing all paths to forward slashes via `filepath.ToSlash`. If you
see this on a recent build, please open an issue with the exact path.

## Sync fails after starting the container

### `healthcheck timeout after 1m0s`

The new container started but didn't accept TCP connections on `spec.Port`
within 60 seconds. Diagnose by tailing logs:

```bash
docker logs <server-name>
```

Common causes:

- **Wrong Java base image for the Minecraft version.** Paper 1.20+ needs
  Java 21; mc-operator picks this automatically based on the version
  string. If you built the image manually, double-check the FROM line.
- **EULA not accepted.** mc-operator's Dockerfile template includes
  `RUN echo eula=true > /server/eula.txt` automatically. If you built
  the image with a different Dockerfile, add this.
- **Plugin crash on startup.** A bad plugin can crash Paper before it
  opens the listening socket.
- **Out of memory.** A 512MB allocation is too small for modern Paper.
  Bump `resource.memoryMB`.
- **Wrong port.** mc-operator publishes the container's `25565` to the
  host as `spec.Port`. If your image runs Paper on a different internal
  port, the publish mapping won't match.

### `start new container: container create: ... port already in use`

`spec.Port` collides with another process on the host. Either:

- Pick a different port in the manifest
- Stop whatever else is binding that port
- Omit `port` from the spec to let mc-operator allocate one

## Web dashboard issues

### Dashboard loads but server cards never appear

The state store has no entries yet. Either:

- The reconciler hasn't run yet (wait `--interval` seconds)
- The manifest didn't load (check the daemon log on startup)
- The reconciler errored (check `/api/v1/healthz` and the daemon log)

### Live events pill stays "disconnected"

The SSE endpoint is unreachable from the browser. Causes:

- A reverse proxy in front of mc-operator is buffering responses (look for
  `proxy_buffering off` for nginx, or `flush_interval -1` for Traefik)
- A request-timeout middleware in front of mc-operator is killing the
  long-lived connection. The daemon's own router has no timeout middleware
  on `/events` (this was fixed after we noticed chi's `middleware.Timeout`
  was killing the stream)

### Sync button does nothing visible for ages

The sync handler is asynchronous: it returns 202 immediately and the deploy
runs in the background. Watch the live events panel for progress messages
or refresh the card after ~15 seconds.

If you see no events at all, the SSE connection may be broken — check the
status pill.

## Git watcher issues

### `watcher: pull: remote not found`

This was a bug in early versions where the watcher rejected repos without
an `origin` remote. Fixed: the watcher now skips `wt.Pull()` if no remote
is configured and falls back to local HEAD comparison. If you still see
this on a recent build, the remote is configured but unreachable — check
your network or remove the remote.

### Watcher detects commits but pipeline never runs

The diff classifier may be marking your changes as `KindIgnored` or
`KindUnknown`. The classifier reacts to:

- `*.yml`, `*.yaml`, `*.properties`, `*.toml`, `*.json`, `*.conf`, `*.cfg` → config
- `*.jar` → jar
- `servers.yaml` → manifest (forces jar pipeline)

Anything under `.git/`, `.github/`, `docs/`, `examples/` is ignored. Add
your file to one of the recognized extensions or restructure the path.

## Jenkins integration issues

### `401 unauthorized` from `/api/v1/triggers/jenkins`

Token mismatch. Triple-check:

- `Authorization: Bearer <token>` (with `Bearer ` prefix and exact value)
- mc-operator started with `--jenkins-token` set to the same string
- No leading/trailing whitespace anywhere

### `pull image: ... server gave HTTP response to HTTPS client`

Your registry is HTTP-only and Docker is trying HTTPS. Add the registry to
the daemon's `insecure-registries` (Docker Desktop settings or
`/etc/docker/daemon.json`):

```json
{
  "insecure-registries": ["registry.internal:5000"]
}
```

Restart Docker after editing.

### `pull access denied for X`

You're trying to pull a local-only image as if it were a registry image.
Either:

- Push it to a registry first (`docker tag X localhost:5000/X && docker push localhost:5000/X`)
- Or pre-load it onto the mc-operator host so `EnsureImage` finds it locally

### Drift detected even though versions match

The image was built without mc-operator's labels. Use `mc-imagegen render`
to produce the Dockerfile, or add the labels manually:

```dockerfile
LABEL mc-operator.type="paper"
LABEL mc-operator.version="1.20.4"
```

## Container drifts back to "Missing" right after a sync

The container started, the healthcheck passed, but then it died. Check:

- `docker ps -a` to see if the container is `Exited` with what code
- `docker logs <name>` to see why it died

If Paper crashed, the issue is in the image, not in mc-operator. The
periodic observe loop is correctly reflecting reality.

## State file got corrupted

If `state.json` becomes invalid JSON (manual edit gone wrong, disk full
during write, etc.), the daemon will refuse to start with a parse error.

Recovery options:

1. **Restore from backup.** State files are small; backing them up is cheap.
2. **Delete and resync.** `rm ~/.mc-operator/state.json` then restart the
   daemon. The reconciler will rebuild state from the manifest and observed
   container reality. You lose `previousImage` (no rollback target) until
   the next deploy.

The store writes atomically (`rename` from a `.tmp` file), so corruption
should only happen with manual editing or hardware failures.

## Still stuck?

- Run with verbose Docker output: the daemon logs Docker SDK errors verbatim
- Check the Live events panel — most failures emit a clear message there
- Check `/api/v1/healthz` and `/api/v1/servers` to see what state mc-operator
  actually has
- File a bug at <https://github.com/devvelvet/mc-operator/issues> with the
  daemon log, the manifest, and what you expected vs. what happened
