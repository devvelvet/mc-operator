# Test coverage

An honest accounting of what's verified and what's not. This page exists so
that anyone trying to ship mc-operator into production knows where the
sharp edges are.

## Verified end-to-end

These have been run against real Docker / real Minecraft / real Jenkins and
observed working.

### Core daemon

- Daemon boots, attaches to Docker, falls back to observe-only when Docker
  is unreachable
- Manifest parsing + validation (`mc-operator validate`)
- State store load/save with mutex protection (no JSON corruption under
  concurrent writes)
- State persistence across daemon restarts: containers stay running, state
  loaded back unchanged

### Image generation and build

- `mc-imagegen render` for paper 1.20.4 (Java 21), 1.18.2 (Java 17), vanilla
  1.8.8 (Java 8), velocity 3.3.0 (extra EXPOSE 25577)
- Validation errors for missing version, missing memory, missing serverJAR,
  unknown type
- Multi-plugin flag (`--plugin a --plugin b`)
- Output to stdout or file (`-o`)
- Docker SDK image build with the same Dockerfile path mc-imagegen produces
- Image labels (`mc-operator.type`, `mc-operator.version`) are preserved
  through `docker build → docker push → docker pull` round-trip

### Reconciler / observation

- 30s reconcile loop updates state from observed container reality
- External drift detection: `docker stop mc-demo` → reconciler reports
  `OutOfSync / Missing` within one interval
- Drift recovery: a sync trigger after external stop recreates the container

### JAR pipeline (build path)

- First build: ~50s (downloads JRE base, builds layers)
- Cached build: ~6s (Docker layer cache fully reused, image ID unchanged)
- TCP healthcheck waits for Paper to fully boot (~7s) and verifies a real
  Minecraft Server List Ping response

### JAR pipeline (prebuilt path / Jenkins)

- Pull from local registry (`localhost:5000`) succeeds, container deploys
- `EnsureImage` correctly skips pull when image already exists locally
- Jenkins-built image with mc-imagegen's Dockerfile preserves the labels
  needed for drift detection

### Rollback

- Healthcheck timeout (60s) triggers rollback in lenient mode
- Container is replaced with `state.previousImage`
- Failure reason recorded in history with the original error message

### Git watcher

- Polls local repo at configured interval (5s in tests)
- Detects new commits via HEAD diff
- Classifies changed files correctly:
  - `configs/lobby/server.properties` → KindConfig → config pipeline
  - `servers.yaml` → KindManifest → jar pipeline
- Auto-reloads manifest when manifest file is in the changed set
- Handles repos with no `origin` remote (fixed bug — was rejecting them)

### Web dashboard

- Embedded `index.html`, `style.css`, `app.js` served with correct MIME types
- SPA fallback works (random `/some/path` serves `index.html`, but `/api/*`
  returns 404 normally)
- Server cards render with Sync/Health badges, image, commit
- Click-to-open detail drawer with state, spec, history sections
- Manual sync button triggers async deploy
- SSE event stream:
  - `: connected` initial comment
  - `event: <type>` + `data: <json>` per emission
  - `: ping` heartbeats every 20s
  - Multiple concurrent SSE clients each receive the full event stream
    (broker fan-out works)
- 4 pipeline stages from a single sync are visible in the live events panel:
  building → built → starting → healthy

### Jenkins integration

- Real Jenkins LTS container running at `:18080`
- Freestyle job created via REST API + CSRF crumb
- Job script:
  - Renders Dockerfile via `mc-imagegen`
  - Builds image with host's Docker daemon (via mounted `/var/run/docker.sock`)
  - Pushes to `localhost:5000` registry
  - Calls `POST /api/v1/triggers/jenkins` with bearer token
- Build #1 SUCCESS in ~12 seconds
- mc-operator pulls the image, performs drift check (passes), starts
  container, healthcheck passes
- History record correctly attributes `source: jenkins`,
  `trigger: build #1 (mc-deploy-test)`

### API

- Bearer token validation (constant-time comparison)
- 401 on missing or wrong token
- 400 on missing required field (`server`)
- 404 on unknown server name
- 202 on accepted async deploy
- 503 when Jenkins endpoint is disabled (`--jenkins-token` not set)

## Verified by unit tests

Run with `go test ./...`. Each line is one or more `*_test.go` cases.

| Package | What it tests |
|---|---|
| `internal/gitops/differ_test.go` | Classify(): manifest, jar, config, ignored, unknown — 10 cases. Summary → pipeline selection: config-only, jar-only, mixed (jar wins), manifest-only. MapServersByConfig with 3 servers and overlapping paths |
| `pkg/manifest/manifest_test.go` | Parse valid manifest. Reject unknown type. Reject duplicate ports. Reject zero memory. Reject missing apiVersion |
| `pkg/mcimage/dockerfile_test.go` | Render paper 1.20.4 → Java 21, COPY paths, labels, ENV, EXPOSE 25565. Render paper 1.18.2 → Java 17. Render velocity 3.3.0 → Java 21 + EXPOSE 25577. Validation: 5 invalid BuildSpec cases. parseMCMajor: 6 cases including weird inputs |
| `pkg/proxy/velocity_test.go` | FromManifest builds correct VelocityConfig. TOML output contains expected lines (bind, [servers], forwarding mode). Proxy server is excluded from `[servers]`. FromManifest fails when proxy.enabled=false. GenerateForwardingSecret produces 64 hex chars and is non-deterministic |

## Verified by V-series tests (added later)

A second test pass closed most of the gaps from the original "NOT verified"
list. These are real, observed behaviors — not "should work according to
the code".

### V1: state.json corruption recovery

| Case | Behavior |
|---|---|
| Garbled file (`this is not json`) | Daemon refuses to start with `parse state` error — prevents silent data loss |
| Valid JSON, missing schema fields | Daemon starts, reconciler rebuilds the missing maps from the manifest |
| File missing entirely | Parent directory auto-created, empty state initialized |

### V2: Server removed from manifest

State entries for removed servers stay in `state.json`. Confirmed as a
known limitation, not a regression. Listed as a soon-to-fix item in
[roadmap.md](roadmap.md).

### V3: Plugin URL download cache (`internal/download/cache_test.go`)

8 unit tests covering:

- Single fetch caches the file on disk with the correct content
- 3 fetches against the same URL → exactly 1 upstream hit (cache hit path proven)
- SHA-256 validation: passes when correct hash provided
- SHA-256 mismatch: error returned AND cached file removed
- Hash invalidation: re-requesting with a new hash forces a refetch
- HTTP 404 from upstream surfaces as a fetch error
- URL with no basename (`http://host/`) is rejected
- New() creates the cache directory tree

The basename test surfaced a real bug: `filepath.Base` on Windows didn't
match URL semantics. Fixed by switching to `net/url` + `path.Base`.

### V4: Real label-mismatch drift detection

Built an image with `mc-operator.version="1.18.2"` labels using `mc-imagegen`
and pointed a manifest at `version: "1.20.4"` at the same image:

| Mode | Result | Container created? |
|---|---|---|
| `strict: true` | Failed history record with `drift=['version: image="1.18.2" manifest="1.20.4"']`, error message `manifest/image drift: [...]` | **No** — DriftError fires before container ops |
| `strict: false` | Success history record with `synced with drift (1)`, drift list preserved | Yes — deploy proceeds, drift just recorded |

Both code paths exercised against a real Docker daemon with real label
inspection.

### V5: Config pipeline real RCON reload

Set up a Paper server with RCON enabled, mapped 25575 to host port 25610,
ran mc-operator with `--rcon-host localhost --rcon-password mcrconpass`.
Then committed a config file change to the watched git repo:

- Watcher detected the new commit (`new commit: 755d863`)
- Differ classified the change as `config sync: 1 files`
- Config pipeline copied the file into the container via docker cp
- Connected to RCON 25610 with the password
- Sent the default `reload confirm` command
- **Paper's actual response came back through the SSE event stream**:
  `reload: §cPlease note that this command is not supported and may cause issues...`
- State marked Synced

This is the full end-to-end loop: edit → git → watcher → differ → pipeline
→ docker cp → RCON → in-game reload → response → SSE → dashboard.

### V6: Velocity proxy real container

Built Velocity 3.3.0-SNAPSHOT (build 436) image, mounted a `velocity.toml`
generated by `pkg/proxy.FromManifest()`, ran on `mc-network` alongside a
Paper backend named `lobby`:

- Velocity boots in 0.67s, loads our toml without complaint
- Listens on `0.0.0.0:25565` as configured
- DNS resolves the backend container by name (`getent hosts lobby` →
  `172.24.0.3`)
- TCP from Velocity to `lobby:25565` succeeds (`nc -z`)
- External Server List Ping to `:25565` returns Velocity's response with
  the `show-max-players: 100` value from our toml and the MOTD text
  mc-operator generated

The full player handshake (Paper-side velocity-modern forwarding-secret
matching) was not exercised — that's a Paper config requirement, not a
mc-operator behavior. The toml generation, mount, and Velocity bootstrap
are confirmed end-to-end.

### V7: Manifest hot-reload during in-flight deploy

Started a deploy that would hang in healthcheck (broken `--rcon-host`),
then mid-flight committed a manifest change (`memoryMB: 1024 → 4096`):

- Daemon survived without panic
- New manifest immediately reflected in `/api/v1/manifest` (`memoryMB=4096`)
- **In-flight deploy used the OLD spec snapshot** — verified by inspecting
  the deployed container's `HostConfig.Memory = 1073741824` (= 1024 MB,
  not 4096 MB)
- state.json stayed valid

This confirms the code's safe semantics: each deploy captures its `spec`
value at start. Subsequent deploys see the new manifest; in-flight ones
don't.

### V8: Config overlay (Jenkins) end-to-end

Built an image with `motd=image-default-motd, max-players=20` baked in.
Set up a repo with `configs/mc-demo/server.properties` containing
`motd=overlay-test-motd, max-players=99`. Triggered a Jenkins deploy with
`configOverlay: true`:

| File inside container | Content | Why |
|---|---|---|
| `/server/server.properties` | `motd=image-default-motd, max-players=20` | Jenkins image's bake-in defaults, untouched |
| `/server/config/server.properties` | `motd=overlay-test-motd, max-players=99` | mc-operator's overlay copied repo configs on top after deploy |

History record: `success / jenkins / build #1 (overlay-test)`. The overlay
file copy is observable; the RCON reload step couldn't be verified
end-to-end on this container (RCON port 25575 wasn't published to host) but
the file copy is the load-bearing part.

## NOT verified (still)

A small honest list of remaining gaps after V-pass.

### Concurrent syncs against the same server

T10 fired 5 simultaneous syncs at one container. State store stayed
consistent (mutex), but the Docker operations raced — 2 succeeded, 3
failed with "container already removed" errors. Known limitation; no
per-server lock yet. Tracked in [roadmap.md](roadmap.md).

### Disk full during state.Save

The atomic-rename approach should handle this gracefully (the old file
stays valid), but reproducing a disk-full scenario reliably needs
artificial filesystem mocks. Not exercised.

### Multi-host

Not implemented. mc-operator targets a single Docker host. The roadmap
has thoughts on extending to multiple hosts via per-server Docker
contexts.

### Velocity full-handshake forwarding

Velocity loads the toml and listens, and Server List Ping through the
proxy works. We did not verify that a real player handshake routes from
Velocity through to the backend Paper server, because that requires
matching the velocity-modern forwarding secret on the Paper side via
`paper-global.yml` — a Paper concern, not a mc-operator concern.

## How to read this page

If a feature is in **"Verified end-to-end"**, you can rely on it for a
small/medium production deployment.

If it's in **"Verified by unit tests"**, the logic is correct but you
should still test the integration in your environment.

If it's in **"NOT verified"**, you're potentially the first person to find
out what happens. PRs (or bug reports) for any of these gaps would be
welcome.
