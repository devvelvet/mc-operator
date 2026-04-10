# Roadmap

A list of known limitations and planned work. Issues / PRs welcome for any
of these.

## Soon (high impact, low effort)

### Per-server sync mutex

**Problem.** Concurrent sync requests against the same server race on
Docker container operations. T10 in our test log showed 5 simultaneous
syncs producing 2 successes and 3 failures with "container already
removed" errors. State stayed consistent (mutex on the store) but the
result is non-deterministic.

**Fix.** Add a per-server lock in `gitops.Reconciler` that serializes
SyncServer / SyncServerOpts / runJAR for the same name. Different servers
can still deploy in parallel.

### Config pipeline integration test

**Problem.** The config pipeline's success path has never been verified
end-to-end against a real running Paper server. We've covered file copy
+ RCON connect failures, but never observed a successful in-game reload.

**Fix.** Add a make target / shell script that:

1. Boots a Paper server via the JAR pipeline
2. Edits a config file under `spec.ConfigDir`
3. Triggers the config pipeline (or a git commit if running with --repo)
4. Verifies the running container's config file has the new content
5. Verifies (via RCON) that the in-game settings reflect the change

### Garbage collect orphaned state entries

**Problem.** When a server is removed from the manifest, its entry stays
in `state.json` forever. Harmless but messy.

**Fix.** At the end of every `Reconcile()`, walk the state store and
remove entries that aren't in the current manifest.

### Drift label parity (full)

**Problem.** Drift detection only checks `mc-operator.type` and
`mc-operator.version`. It does not check plugins, memory, or env vars.

**Fix.** Add `mc-operator.plugins-hash` to the Dockerfile template (sha256
of sorted plugin names+versions) and compare on deploy. Same for any
other fields you want enforced.

## Medium term (more design work)

### Multi-host support

**Problem.** mc-operator targets a single Docker host. Larger networks
(e.g. one host per gameplay server type) need a different topology.

**Approach.** Extend the manifest with a per-server `host` field pointing
at a named Docker context. The daemon would maintain one `docker.Client`
per context and route operations accordingly. The single state store and
single web dashboard would still cover all hosts.

Trade-off: state still lives on one machine, so that machine becomes a
single point of failure for the control plane. ArgoCD has the same shape
(controller is centralized, workloads are distributed).

### Web dashboard authentication

**Problem.** Anyone with network access to the dashboard can sync any
server, browse the manifest, and read the SSE event stream. Today the
expectation is that operators front mc-operator with a reverse proxy or
VPN.

**Approach.** Add optional basic-auth or OIDC. Keep the unauth path
available for local-only deployments.

### Config pipeline RCON-less mode

**Problem.** The config pipeline does file copy + RCON reload. If RCON
isn't enabled in `server.properties`, the reload step is skipped and the
running container doesn't pick up the changes.

**Approach.** Add a `restartOnConfigChange: true` flag on the server
spec. When set, config-only changes trigger a container restart instead
of an RCON reload. Sleeker than running the JAR pipeline since no image
rebuild is needed.

### Plugin URL download cache hits in tests

**Problem.** The download cache code is exercised in production but the
cache-hit path is not proven by automated tests.

**Approach.** Add a unit test in `internal/download/cache_test.go` that
serves a fake plugin via `httptest.Server`, fetches it twice, and verifies
the second fetch is served from disk without hitting the test server.

## Long term (substantial work)

### Backup pipeline

**Problem.** mc-operator can rebuild server *processes* on demand but
cannot recover *world data*. A bad deploy that corrupts a chunk database
is a permanent data loss.

**Approach.** Add a backup pipeline that snapshots `/server/world` (and
any plugin data dirs) before a JAR-pipeline deploy. Backups go to a
configurable local directory or S3-compatible bucket. Surface
`Restore` actions in the dashboard.

This is a meaningful feature — properly designing the snapshot+restore
workflow (especially around plugin databases that are open at snapshot
time) needs care.

### Real-time player metrics

**Problem.** The dashboard shows sync/health but not player count, TPS,
memory usage, or chat. The mc-imagegen Dockerfile doesn't expose Paper's
JMX or any metrics endpoint.

**Approach.** Two options:

1. Bake a small metrics agent into the image (Java agent or sidecar
   container) that exports Prometheus metrics or pushes to mc-operator.
2. Use Paper's `bstats` integration if it's compatible.

Either way it's a noticeable amount of glue and the dashboard would need
new chart components.

### Pipeline plugin system

**Problem.** Today the pipelines are hardcoded to "config" and "jar". A
real production setup might want pre-deploy hooks (notify Discord),
post-deploy hooks (run migration scripts), or cleanup hooks.

**Approach.** Add a `hooks` section to the manifest with paths to shell
scripts that run at specific lifecycle events. The reconciler executes
them with a controlled environment and surfaces failures in the SSE
stream.

This needs careful security thinking — hooks running as the daemon user
have full Docker access.

### Helm-style values + templating

**Problem.** Multiple environments (dev, staging, prod) with the same
shape but different values mean copy-pasting the manifest.

**Approach.** Support `servers.yaml.gotmpl` rendered with environment-
specific values from a separate file. Or just document that users can
render their own manifest before checking it in.

This is mostly out of scope for mc-operator's "single Git repo of
truth" model, but worth noting as a common request pattern.

## Won't do (unless someone makes a strong case)

### Kubernetes operator

mc-operator deliberately targets single-host Docker. Minecraft servers are
stateful, not horizontally scalable, and pin to one JVM per world. The
Kubernetes pod model fits poorly. Use mc-operator if you want a
purpose-built Minecraft tool; use plain Kubernetes if you want generic
container orchestration and are willing to deal with the impedance.

### Bedrock support

mc-operator is Java-edition only. Bedrock has a different protocol, a
different server flavor (`bedrock_server`), and different RCON support
(none, in most cases). It's a separate product, not a feature.

### Built-in registry

The Jenkins integration assumes you have a Docker registry available
(local or remote). mc-operator doesn't run one for you. Use `registry:2`
for local testing or any cloud registry for production. There's no value
in mc-operator reimplementing this.
