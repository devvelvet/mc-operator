# mc-operator wiki

Welcome to the mc-operator documentation. mc-operator is a declarative,
GitOps-style deployment tool for Minecraft server infrastructure with an
ArgoCD-inspired web dashboard, image builder, and Jenkins integration.

## Index

| Page | What it covers |
|------|----------------|
| [getting-started.md](getting-started.md) | Install Go + Docker, build the binaries, run your first server |
| [architecture.md](architecture.md) | Component layout, data flow, dependency direction |
| [manifest-reference.md](manifest-reference.md) | Full `servers.yaml` schema with examples |
| [pipelines.md](pipelines.md) | How the config-reload and jar-rebuild pipelines work, including rollback |
| [api-reference.md](api-reference.md) | Every HTTP endpoint with curl examples |
| [dashboard.md](dashboard.md) | Web dashboard tour: server cards, drawer, history, SSE events |
| [jenkins-integration.md](jenkins-integration.md) | End-to-end Jenkins setup with registry, drift detection, config overlay |
| [development.md](development.md) | Build, test, package layout for contributors |
| [troubleshooting.md](troubleshooting.md) | Common errors and fixes |
| [test-coverage.md](test-coverage.md) | Honest list of what is and isn't covered by tests |
| [roadmap.md](roadmap.md) | Known limitations and planned work |

## At a glance

```
                    ┌─────────────────┐
                    │   servers.yaml  │  (desired state, in Git)
                    └────────┬────────┘
                             │
                             ▼
┌──────────────┐    ┌─────────────────┐    ┌──────────────┐
│  Git watcher │───►│   reconciler    │◄───│   Jenkins    │
└──────────────┘    └────────┬────────┘    │   webhook    │
                             │              └──────────────┘
                             ▼
                    ┌─────────────────┐
                    │  pipeline.Runner│
                    │  (config / jar) │
                    └────────┬────────┘
                             │
                ┌────────────┴────────────┐
                ▼                         ▼
        ┌──────────────┐          ┌──────────────┐
        │ Docker host  │          │  RCON to MC  │
        │ (containers) │          │   server     │
        └──────────────┘          └──────────────┘

                ┌─────────────────┐
                │ Web dashboard   │  (embedded HTML/CSS/JS, SSE live)
                │ http://:8080/   │
                └─────────────────┘
```

## Quick links

- Source: [github.com/devvelvet/mc-operator](https://github.com/devvelvet/mc-operator)
- Sample manifest: [`examples/servers.yaml`](../examples/servers.yaml)
- Demo manifest: [`examples/demo.yaml`](../examples/demo.yaml)
- Example Jenkinsfile: [`examples/Jenkinsfile`](../examples/Jenkinsfile)
