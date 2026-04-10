# Manifest reference

The manifest file (`servers.yaml`) declares the desired state of your
Minecraft infrastructure. It is the single source of truth — everything
mc-operator does is in service of converging the running containers with
this file.

## Top-level schema

```yaml
apiVersion: mc-operator/v1   # required
proxy:                        # optional, single proxy per network
  enabled: bool
  version: string
  externalPort: int
  forwardingMode: string
servers:                      # required, at least one
  - name: string
    type: paper|spigot|vanilla|fabric|forge|velocity
    version: string
    port: int
    resource:
      memoryMB: int
      cpus: int               # optional
    plugins: []               # optional
    configDir: string         # optional
    reloadCommand: string     # optional
    env: {}                   # optional
```

## `apiVersion`

Required. Must be the literal string `mc-operator/v1`. Future versions may
add a `v2` schema; the parser uses this to refuse unknown formats.

## `proxy`

Optional but recommended for multi-server setups. When `enabled: true`,
mc-operator can auto-generate a `velocity.toml` from the manifest (use
`--proxy-config /path/to/velocity.toml` on the daemon).

| Field | Type | Notes |
|---|---|---|
| `enabled` | bool | Master switch |
| `version` | string | Velocity version (e.g. `3.3.0`) |
| `externalPort` | int | Public-facing port that players connect to |
| `forwardingMode` | string | `modern` (default) / `legacy` / `bungeeguard` / `none` |

The generated toml lists every non-proxy server from `servers[]` under the
`[servers]` section, alphabetically sorted for reproducibility. The first
non-proxy server becomes the default landing point in `try`.

## `servers[]`

A list of gameplay (and optionally proxy) servers.

### `name` *(required)*

A short, DNS-safe identifier (`lobby`, `survival-1`, `mc-demo`). Used as the
Docker container name and as the URL component in API calls
(`/api/v1/servers/{name}`).

### `type` *(required)*

One of: `paper`, `spigot`, `vanilla`, `fabric`, `forge`, `velocity`.

The type drives:
- Default reload command (`reload confirm` for paper/spigot, `velocity reload`
  for velocity, `reload` for vanilla)
- The `mc-operator.type` image label that drift detection compares against
- Whether the server is excluded from Velocity `[servers]` (`velocity` itself
  is excluded — it's the proxy)

### `version` *(required)*

The Minecraft version string (`1.20.4`, `1.18.2`, `1.8.8`). Used to:
- Pick the JRE base image — Java 21 for 1.20+, Java 17 for 1.17–1.19,
  Java 8 for older
- Set the `mc-operator.version` image label for drift detection

### `port` *(optional)*

The host port the container will be published on. If omitted, mc-operator
allocates a port from 25566 onward and persists the assignment in
`state.json`.

### `resource` *(required)*

```yaml
resource:
  memoryMB: 2048    # required, used as -Xmx and as Docker memory limit
  cpus: 2           # optional
```

### `plugins[]` *(optional)*

Plugin jars to bake into the image when mc-operator builds locally.

```yaml
plugins:
  - name: LuckPerms
    source: url
    path: https://download.luckperms.net/.../LuckPerms-Bukkit-5.4.jar
    sha256: abc123...   # optional integrity check
  - name: HubPlugin
    source: local
    path: plugins/hub/HubPlugin.jar
```

| Field | Notes |
|---|---|
| `name` | Display name (used in error messages) |
| `source` | `local` (relative to repo dir) or `url` (HTTP/HTTPS) |
| `path` | File path or URL depending on source |
| `sha256` | Optional. When set, verified after download; mismatch invalidates the cache entry |

URL plugins are cached on-disk so the same URL is downloaded once across
many reconciles. The cache lives next to `state.json` by default.

### `configDir` *(optional)*

A repository-relative directory containing config files (server.properties,
plugin yamls, etc.) that should be synced into the container. The config
pipeline reacts to changes under this directory and copies them in.

```yaml
configDir: configs/lobby
```

When the watcher sees an edit to `configs/lobby/paper.yml`, only the `lobby`
server is affected (other servers' config dirs are independent).

### `reloadCommand` *(optional)*

Override the in-game RCON command issued after a config sync. Useful when
you want to run a plugin-specific reload (`luckperms reload` etc.) instead
of the bukkit default. Defaults are picked per server type.

### `env` *(optional)*

Extra environment variables injected into the container.

```yaml
env:
  DIFFICULTY: hard
  ONLINE_MODE: "true"
```

## Validation

`mc-operator validate path/to/servers.yaml` runs the same checks the daemon
runs at startup:

- `apiVersion` is present
- At least one server is declared
- Each server has a unique `name`
- Each server has a known `type`
- Each server has a non-empty `version`
- Each server has `resource.memoryMB > 0`
- No two servers share the same fixed `port`
- If `proxy.enabled: true`, both `proxy.version` and `proxy.externalPort` are set

Validation errors include the offending server name and field path so you
can find the problem quickly.

## Examples

### Minimal single-server

```yaml
apiVersion: mc-operator/v1
proxy:
  enabled: false
servers:
  - name: mc-demo
    type: paper
    version: "1.20.4"
    port: 25600
    resource:
      memoryMB: 1024
```

### Proxy + multiple gameplay servers

```yaml
apiVersion: mc-operator/v1

proxy:
  enabled: true
  version: "3.3.0"
  externalPort: 25565
  forwardingMode: modern

servers:
  - name: lobby
    type: paper
    version: "1.20.4"
    port: 25566
    resource:
      memoryMB: 2048
    configDir: configs/lobby
    plugins:
      - name: HubPlugin
        source: local
        path: plugins/hub/HubPlugin.jar

  - name: survival
    type: paper
    version: "1.20.4"
    port: 25567
    resource:
      memoryMB: 4096
    configDir: configs/survival
    env:
      DIFFICULTY: hard

  - name: creative
    type: paper
    version: "1.20.4"
    port: 25568
    resource:
      memoryMB: 3072
```

See [`examples/`](../examples/) for working manifests you can run today.
