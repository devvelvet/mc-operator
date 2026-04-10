# Pipelines

mc-operator has two deploy pipelines, both built on the same rollover /
healthcheck / rollback machinery. Which one runs depends on what changed.

## Diff classifier

When the git watcher sees a new commit, it computes the changed file paths
and classifies each one (`internal/gitops/differ.go`):

| File extension | Class | Pipeline |
|---|---|---|
| `.yml`, `.yaml`, `.properties`, `.toml`, `.json`, `.conf`, `.cfg` | config | Config pipeline |
| `.jar` | jar | JAR pipeline |
| Path matches `servers.yaml` | manifest | JAR pipeline (manifest may change image inputs) |
| Anything under `.git/`, `.github/`, `docs/`, `examples/` | ignored | none |
| Anything else | unknown | none |

If a single commit touches both classes, the **JAR pipeline wins** because
restarting the container also picks up the config changes.

## Config pipeline

Used when only config files change. The goal is **zero-downtime in-game
config reload** without restarting the JVM.

```
1. Identify which server's ConfigDir contains each changed file
2. For each affected server:
   a. docker cp the changed files into the running container's /server/config
   b. Open RCON connection to the server
   c. Send the configured reload command (default `reload confirm` for paper)
   d. Record reload output in the SSE event stream
3. Mark each server Synced + Healthy in state.json
```

The config pipeline does **not** restart the container. If RCON is down or
the reload command fails, the file copy still happened, but the running
container hasn't picked it up. mc-operator surfaces the failure as a
`failed` history entry; the operator can then trigger a manual restart via
the dashboard.

### What if RCON isn't configured?

If `--rcon-host` is unset, mc-operator skips the reload step and only copies
the files. This is useful for plugin configs that the plugin itself
hot-reloads (e.g. via filesystem watch).

### What about `server.properties`?

Paper reads `server.properties` once at startup. The config pipeline can
copy a new `server.properties` in, but Paper won't pick it up until the
container restarts. If you want strict server.properties reconciliation,
trigger a sync via the dashboard or commit a manifest change to take the
JAR pipeline path.

## JAR pipeline

Used when a jar, plugin, or the manifest itself changes. Also used by manual
syncs and Jenkins triggers.

```
1. Obtain the image
   ├── If PrebuiltImage is set (Jenkins flow):
   │     EnsureImage(image) — inspect locally first, pull from registry if absent
   └── Otherwise:
         Resolve build inputs (server jar + plugins) via DefaultResolver
         Build via Docker SDK ImageBuild

2. Inspect image labels (mc-operator.type, mc-operator.version)
   Compare with manifest spec → compute drift list
   If Strict mode AND drift detected → DriftError, abort BEFORE touching containers

3. EnsureNetwork(mc-network)

4. Stop & remove the existing container (if any), with StopTimeout grace

5. Run the new container with:
   - Same name as the server
   - Image from step 1
   - Port published to host (spec.Port → 25565)
   - Memory limit from spec.Resource.MemoryMB
   - mc-network attached
   - Labels: mc-operator.server, mc-operator.revision

6. Healthcheck loop (default budget: 60s)
   - Call HealthCheck(spec) every 2s
   - On success: emit "healthy", return JARResult{Tag, Drift}
   - On budget exceeded: rollback

7. Rollback (only on healthcheck or start failure)
   - If PreviousImg is empty, log "rollback skipped" and surface the failure
   - Otherwise, stop+remove the failing container
   - Start a fresh container with PreviousImg
   - The rollback container is NOT healthchecked (we trust it because it
     was Healthy before the failed deploy)
```

### Image labels and drift

mc-operator's `pkg/mcimage` Dockerfile template emits two labels on every
build:

```dockerfile
LABEL mc-operator.type="paper"
LABEL mc-operator.version="1.20.4"
```

When the JAR pipeline is given a prebuilt image (e.g. from Jenkins), it
inspects these labels and compares them with the manifest spec. Mismatches
become drift entries:

```
type: image="paper" manifest="spigot"
version: image="1.21" manifest="1.20.4"
```

In **lenient mode** (default), the deploy proceeds and the drift list is
attached to the deploy history record. The dashboard surfaces drift to
operators visually.

In **strict mode** (`"strict": true` in the Jenkins payload), the deploy
is aborted **before** the container is touched. The DriftError surfaces as
HTTP 409 Conflict to the Jenkins job and as a `drift` SSE event on the
dashboard.

### Health check

The default health check (`internal/health/health.go`) is a TCP dial to
`{rconHost}:{spec.Port}` with a 2-second connect timeout. Paper opens its
listening socket as the very last step of startup, so a successful TCP
dial means the server is accepting connections.

This is a lightweight check; for stricter readiness verification you could
plug in an RCON ping or a Server List Ping. The pipeline accepts any
function with the signature `func(ctx, spec) error`.

### Rollback semantics

The "previous image" is whatever `state.currentImage` was when the deploy
**started**. After a successful sync, the freshly-deployed image becomes
the new `currentImage` and the old one becomes `previousImage`.

If you sync with the same image tag (common when the tag is `manual` or
when you re-run the same Jenkins build), the rollback target is the same as
the deploy target. The pipeline still goes through the rollback motions
(stop, remove, run with PreviousImg) — the container ends up in the same
state but the operator gets a clear "rollback executed" history record.

## Post-deploy config overlay

Jenkins-built images carry baked-in defaults. The `configOverlay` flag in
the Jenkins payload causes mc-operator to do an **additional** config sync
after the JAR pipeline succeeds:

```
1. JAR pipeline: pull image → drift check → start container → healthy
2. (configOverlay enabled and spec.ConfigDir set and --repo set):
   3. List every file under {repoDir}/{spec.ConfigDir}
   4. docker cp them into /server/config inside the new container
   5. RCON reload command
```

This means Jenkins images can ship with safe defaults while environment-
specific configs (server.properties overrides, plugin configs) live in the
mc-operator repo and stay in sync with the deployed reality.

The overlay is best-effort: if the RCON reload fails, the deploy itself is
still considered successful (the image is healthy and running). The overlay
failure surfaces as an `error` SSE event for visibility.

## When pipelines run

| Trigger | Pipeline |
|---|---|
| Periodic observe loop (every 30s) | Neither — observation only |
| Git commit touching `*.yml` only | Config pipeline (per affected server) |
| Git commit touching `*.jar` or manifest | JAR pipeline (all gameplay servers) |
| Dashboard sync button | JAR pipeline (one server, build from local files) |
| Jenkins webhook with `image` set | JAR pipeline (one server, pull prebuilt) |
| Jenkins webhook without `image` | JAR pipeline (one server, build from local files) |
