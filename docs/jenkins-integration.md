# Jenkins integration

mc-operator supports CI-driven deployments via a webhook endpoint. This guide
walks through a working end-to-end setup with a local Docker registry, a
Jenkins container, and a freestyle job that builds, pushes, and deploys.

## Architecture

```
┌────────────────┐                    ┌──────────────────┐
│  Jenkins job   │                    │  mc-operator     │
│  (freestyle    │                    │  daemon          │
│   or pipeline) │                    │  :8080           │
└───────┬────────┘                    └────────┬─────────┘
        │                                       │
        │  1. mc-imagegen render                │
        │  2. docker build -t REGISTRY/X:N      │
        │  3. docker push                       │
        │                                       │
        │  4. POST /api/v1/triggers/jenkins ───►│
        │     Authorization: Bearer <token>     │
        │     { server, image, ... }            │
        │                                       │
        │           ◄────────── 202 Accepted ──┤
        │                                       │
        │                                       │  5. EnsureImage(X:N)
        │                                       │     → docker pull
        │                                       │  6. Inspect labels →
        │                                       │     drift check
        │                                       │  7. Stop old container
        │                                       │  8. Start new container
        │                                       │  9. TCP healthcheck
        │                                       │ 10. (optional) config overlay
        │                                       │
        │                                       │  → success / rollback
        │                                       │  → SSE event published
        │                                       │  → history record stored
```

## 1. Configure mc-operator

Generate a strong shared token and pass it to the daemon:

```bash
TOKEN=$(openssl rand -hex 32)
./mc-operator serve \
  --manifest /etc/mc-operator/servers.yaml \
  --jenkins-token "$TOKEN" \
  --addr :8080
```

Or set `MC_JENKINS_TOKEN` in the environment instead of the flag.

When the token is empty, the endpoint returns 503 — there is no anonymous
mode by design.

## 2. Set up a registry (optional but recommended)

Most CI flows push to a shared registry rather than building on every host.
For a local test you can run a registry on the same host:

```bash
docker run -d --name mc-registry -p 5000:5000 --restart unless-stopped registry:2
```

Verify:

```bash
curl http://localhost:5000/v2/_catalog
# {"repositories":[]}
```

If you use a registry on a non-`localhost` address, configure your Docker
daemon's `insecure-registries` (in Docker Desktop settings or
`/etc/docker/daemon.json`) so HTTP-only registries don't require TLS:

```json
{
  "insecure-registries": ["registry.internal:5000"]
}
```

## 3. Set up a Jenkins job

There are two flavors. Both work; pick whichever fits your team.

### Option A: Declarative pipeline (Jenkinsfile)

Use the [`examples/Jenkinsfile`](../examples/Jenkinsfile) as a starting
point. The pipeline:

1. Fetches a Paper jar
2. Renders a Dockerfile via `mc-imagegen`
3. Builds and pushes the image
4. POSTs to `/api/v1/triggers/jenkins`

You'll need:

- Jenkins LTS with the Pipeline plugin (installed by default)
- A `Secret text` credential named `mc-operator-token` containing the value
  of `--jenkins-token`
- A `Username with password` credential named `registry-creds` for `docker push`
- The `mc-imagegen` binary installed inside the agent (or pull it from a
  build image)

Create a new pipeline job in the Jenkins UI, point it at your repo's
`Jenkinsfile`, and you're done.

### Option B: Freestyle job (no plugins required)

Useful when you don't have the pipeline plugin or want to drop into a quick
shell script. Create a freestyle project and add this as the build step:

```bash
#!/bin/bash
set -eux

# Resolve image tag DAEMON-side. If your Jenkins agent shares the host's
# docker socket, "localhost:5000" correctly points to the host's registry —
# don't use the container-side hostname here.
REGISTRY=localhost:5000
IMAGE=$REGISTRY/mc-demo:$BUILD_NUMBER

# CONTAINER-side hostname for the HTTP call out to mc-operator.
MC_OPERATOR=http://host.docker.internal:8080
MC_TOKEN=$(cat /var/jenkins_home/mc-token)

mkdir -p ctx
cp /var/jenkins_home/paper-1.20.4.jar ctx/server.jar
mc-imagegen render --type paper --version 1.20.4 --memory 1024 \
  --jar server.jar -o ctx/Dockerfile

docker build -t "$IMAGE" ctx/
docker push "$IMAGE"

curl -fsSL -X POST "$MC_OPERATOR/api/v1/triggers/jenkins" \
  -H "Authorization: Bearer $MC_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{
        \"server\":   \"mc-demo\",
        \"image\":    \"$IMAGE\",
        \"revision\": \"jenkins-build-$BUILD_NUMBER\",
        \"buildId\":  \"$BUILD_NUMBER\",
        \"jobName\":  \"$JOB_NAME\",
        \"strict\":   true
      }"
```

## 4. Webhook payload reference

```http
POST /api/v1/triggers/jenkins
Authorization: Bearer <token>
Content-Type: application/json

{
  "server":        "lobby",
  "image":         "registry.example.com/mc-lobby:42",
  "revision":      "abc123def",
  "buildId":       "42",
  "jobName":       "mc-lobby-build",
  "strict":        true,
  "configOverlay": true
}
```

| Field | Required | Notes |
|---|---|---|
| `server` | yes | Must match a server name in the loaded manifest |
| `image` | no | Prebuilt image to pull. Empty → mc-operator builds from local files using its standard sync path |
| `revision` | no | Source commit, persisted to `state.lastCommit` |
| `buildId` | no | Used in dashboard history label |
| `jobName` | no | Used in dashboard history label |
| `strict` | no | Reject deploy with `drift` event if image labels disagree with manifest spec |
| `configOverlay` | no | After deploy succeeds, copy current repo configs into the container and trigger an RCON reload |

## 5. Drift handling

mc-operator embeds two labels in every image it (or `mc-imagegen`) builds:

```dockerfile
LABEL mc-operator.type="paper"
LABEL mc-operator.version="1.20.4"
```

When a Jenkins-pushed image is deployed, mc-operator inspects these labels
and compares them with the manifest spec. Three outcomes:

| Image labels | Manifest | Strict | Result |
|---|---|---|---|
| Match | — | — | Deploy proceeds, no drift |
| Mismatch (e.g. version) | paper 1.20.4 | false | Deploy proceeds, drift recorded in history |
| Mismatch | paper 1.20.4 | true | Deploy aborted, `drift` SSE event, failed history record |
| No labels (e.g. third-party image) | — | — | No drift detected, deploy proceeds. The user vouches for the image |

The drift detection is intentionally lenient — it's meant to catch
"oh, Jenkins built the wrong version" mistakes, not to be a security boundary.

## 6. Config overlay (drift between Jenkins workspace and mc-operator repo)

This is the trickier kind of drift: Jenkins's source checkout has different
config files than mc-operator's `--repo`. The image was built with one set
of files; the operator repo has another.

`configOverlay: true` makes mc-operator perform a second pass after the
container becomes healthy:

1. Walk every file under `{repoDir}/{spec.ConfigDir}`
2. `docker cp` each file into `/server/config/...` inside the container
3. Send the configured RCON reload command

This means Jenkins images can ship with safe defaults (`server.properties`
template, plugin config defaults) while environment-specific overrides live
in the mc-operator repo and stay in sync with the deployed reality. The
image is the *artifact*; the repo is the *configuration*.

The overlay is best-effort: if RCON reload fails, the deploy is still
considered successful. The overlay failure surfaces as an `error` SSE event
so operators see it on the dashboard.

## 7. Troubleshooting

### `401 unauthorized`

The bearer token is missing or doesn't match. Check:

- `Authorization: Bearer <token>` header is exactly that format
- The token contains no leading/trailing whitespace
- mc-operator was started with `--jenkins-token` (or `MC_JENKINS_TOKEN`)
- The values match exactly (constant-time comparison; whitespace counts)

### `503 jenkins endpoint disabled`

mc-operator is running without `--jenkins-token`. The endpoint is
disabled by default to avoid an anonymous remote-execution surface.

### `404 server not found`

The `server` field doesn't match any server name in the currently-loaded
manifest. Check `/api/v1/manifest` to see what's loaded.

### `pull image: ... pull access denied`

mc-operator's Docker daemon can't reach your registry. Common causes:

- The registry hostname doesn't resolve from the daemon's perspective
- The registry uses HTTP but Docker is enforcing HTTPS — add it to
  `insecure-registries`
- Authentication is required and the Docker daemon doesn't have credentials

### Image pulled but deploy fails healthcheck

The container started but no process is listening on `spec.Port` within 60
seconds. Check the container logs:

```bash
docker logs <server-name>
```

Common cases:
- Wrong base image (Java version mismatch with the Minecraft version)
- EULA not accepted (mc-imagegen's Dockerfile auto-accepts; if you built
  the image differently you need to handle this)
- Plugin compatibility crash on startup
- Out of memory (`memoryMB` too small)

The container will be rolled back to `previousImage` if one is recorded.

### Drift detected on every deploy

If your Jenkins job overrides the image labels (or builds without labels),
mc-operator will see drift on every deploy. Solutions:

- Use `mc-imagegen render` to produce the Dockerfile (it always sets the
  labels correctly)
- Or manually `LABEL mc-operator.type="..."` and `LABEL mc-operator.version="..."`
  in your Dockerfile
- Or set `strict: false` and accept the drift records (lenient mode)

## 8. Worked example: end-to-end on a single host

This is the exact setup we use to test the integration locally.

```bash
# 1. Local registry
docker run -d --name mc-registry -p 5000:5000 registry:2

# 2. mc-operator with Jenkins token
TOKEN=test-token
./mc-operator serve \
  --manifest examples/demo.yaml \
  --jenkins-token "$TOKEN" \
  --addr :8092

# 3. Jenkins with wizard skipped (so REST API works without setup)
docker run -d --name mc-jenkins \
  -p 18080:8080 \
  -e JAVA_OPTS="-Djenkins.install.runSetupWizard=false" \
  -v mc-jenkins-data:/var/jenkins_home \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --add-host=host.docker.internal:host-gateway \
  jenkins/jenkins:lts-jdk17

# 4. Install docker CLI inside the Jenkins container
docker exec -u 0 mc-jenkins apt-get update
docker exec -u 0 mc-jenkins apt-get install -y docker.io
docker exec -u 0 mc-jenkins chmod 666 /var/run/docker.sock

# 5. Copy the Paper jar and mc-imagegen into the Jenkins workspace
docker cp jars/paper-1.20.4.jar mc-jenkins:/var/jenkins_home/
GOOS=linux GOARCH=amd64 go build -o /tmp/mc-imagegen ./cmd/mc-imagegen
docker cp /tmp/mc-imagegen mc-jenkins:/usr/local/bin/mc-imagegen

# 6. Create a freestyle job (the freestyle XML body from §3 above)
curl -c cookies -s 'http://localhost:18080/crumbIssuer/api/xml?xpath=concat(//crumbRequestField,":",//crumb)'
# … then POST to /createItem?name=mc-deploy-test with the cookie + crumb

# 7. Trigger the build
curl -b cookies -X POST http://localhost:18080/job/mc-deploy-test/build -H "$CRUMB"
```

We've run this exact sequence and verified the resulting deploy:

- Build #1 SUCCESS in ~12 seconds
- mc-operator pulled the image from `localhost:5000`
- Drift check passed (image labels match manifest)
- Container `mc-demo` running on port 25600
- History record: `source: jenkins, trigger: build #1 (mc-deploy-test)`
- Real Minecraft client connects successfully
