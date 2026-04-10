// Package pipeline implements the config-reload and jar-rebuild pipelines.
// Both pipelines are orchestration logic wired on top of:
//   - pkg/mcimage  — image generation (jar pipeline)
//   - pkg/rcon     — in-server reload (config pipeline)
//   - internal/docker — container lifecycle
package pipeline

import (
	"context"
	"fmt"

	"github.com/devvelvet/mc-operator/internal/docker"
	"github.com/devvelvet/mc-operator/pkg/mcimage"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

// Notifier receives human-readable status lines emitted by pipelines.
// A nil Notifier is safe to pass; emissions are dropped.
type Notifier interface {
	Emit(server, msg string)
}

type noopNotifier struct{}

func (noopNotifier) Emit(string, string) {}

// Runner executes config and JAR pipelines against a Docker host.
type Runner struct {
	Docker   *docker.Client
	Builder  mcimage.Builder
	Notifier Notifier
	// Network is the Docker network all server containers join.
	Network string
	// RepoDir is the local filesystem path of the Git working copy; config
	// files are copied from here into target containers.
	RepoDir string
	// ImageTagFn generates the image reference for a server given the spec
	// and an optional revision (commit sha, build id). Nil = default mc-{name}:{rev}.
	ImageTagFn func(spec mctypes.ServerSpec, rev string) string
}

// New returns a Runner with the given dependencies.
func New(dk *docker.Client, builder mcimage.Builder, n Notifier) *Runner {
	if n == nil {
		n = noopNotifier{}
	}
	return &Runner{Docker: dk, Builder: builder, Notifier: n, Network: "mc-network"}
}

func (r *Runner) emit(server, msg string) {
	if r.Notifier != nil {
		r.Notifier.Emit(server, msg)
	}
}

func (r *Runner) imageTag(spec mctypes.ServerSpec, rev string) string {
	if r.ImageTagFn != nil {
		return r.ImageTagFn(spec, rev)
	}
	if rev == "" {
		rev = "latest"
	}
	return fmt.Sprintf("mc-%s:%s", spec.Name, rev)
}

// HealthCheck is a function that returns nil when a server is considered healthy.
// Runner callers provide this to decouple pipelines from RCON/TCP specifics.
type HealthCheck func(ctx context.Context, spec mctypes.ServerSpec) error
