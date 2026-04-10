package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devvelvet/mc-operator/internal/docker"
	"github.com/devvelvet/mc-operator/pkg/mcimage"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

// JARPipelineInput describes a single invocation of the jar (rebuild) pipeline.
type JARPipelineInput struct {
	Spec     mctypes.ServerSpec
	Revision string // commit sha or build id used as the image tag

	// PrebuiltImage, if set, causes the pipeline to skip its own image build
	// and instead pull the named image from a registry. Used by Jenkins-style
	// triggers where CI builds and pushes the image, and mc-operator only
	// performs the rollover + healthcheck + rollback dance.
	PrebuiltImage string

	// ServerJAR + PluginJARs are used only when PrebuiltImage is empty.
	ServerJAR  string
	PluginJARs []string

	PreviousImg string // previous image reference, used for rollback

	HealthCheck  HealthCheck
	HealthBudget time.Duration // how long to wait for the new container to become healthy
	StopTimeout  time.Duration // graceful stop window for the old container

	// Strict, when true, causes the pipeline to fail with a DriftError before
	// touching the running container if the prebuilt image's labels disagree
	// with the manifest spec (e.g. version mismatch).
	Strict bool
}

// JARResult is what the jar pipeline returns on completion.
type JARResult struct {
	Tag   string   // image tag actually deployed
	Drift []string // any spec/image label inconsistencies observed
}

// DriftError is returned by RunJAR when Strict mode is enabled and the
// prebuilt image disagrees with the manifest spec.
type DriftError struct {
	Drift []string
}

func (e *DriftError) Error() string {
	return fmt.Sprintf("manifest/image drift: %v", e.Drift)
}

// RunJAR rebuilds (or pulls) the server image and rolls the container over.
// It returns the deployed image tag plus any drift information observed.
//
// Stages:
//  1. obtain image (build OR pull)
//  2. inspect image labels and compute drift vs manifest spec
//  3. ensure shared network exists
//  4. stop & remove old container
//  5. start new container
//  6. healthcheck loop within HealthBudget
//  7. rollback to PreviousImg on failure
func (r *Runner) RunJAR(ctx context.Context, in JARPipelineInput) (JARResult, error) {
	if r.Docker == nil {
		return JARResult{}, fmt.Errorf("jar pipeline: docker client required")
	}
	if in.PrebuiltImage == "" && r.Builder == nil {
		return JARResult{}, fmt.Errorf("jar pipeline: builder required when PrebuiltImage is empty")
	}
	if in.HealthBudget <= 0 {
		in.HealthBudget = 60 * time.Second
	}
	if in.StopTimeout <= 0 {
		in.StopTimeout = 15 * time.Second
	}

	name := in.Spec.Name

	// 1. Obtain image: either build it locally or pull a prebuilt one.
	var tag string
	if in.PrebuiltImage != "" {
		tag = in.PrebuiltImage
		r.emit(name, "jar pipeline: ensuring prebuilt image "+tag)
		if err := r.Docker.EnsureImage(ctx, tag); err != nil {
			return JARResult{}, fmt.Errorf("ensure image: %w", err)
		}
		r.emit(name, "image ready: "+tag)
	} else {
		r.emit(name, "jar pipeline: building image")
		tag = r.imageTag(in.Spec, in.Revision)
		buildSpec := mcimage.BuildSpec{
			Type:       in.Spec.Type,
			Version:    in.Spec.Version,
			MemoryMB:   in.Spec.Resource.MemoryMB,
			ServerJAR:  in.ServerJAR,
			PluginJARs: in.PluginJARs,
		}
		if _, err := r.Builder.Build(ctx, buildSpec, tag); err != nil {
			return JARResult{}, fmt.Errorf("image build: %w", err)
		}
		r.emit(name, "image built: "+tag)
	}

	// 2. Inspect image labels for drift. Locally-built images carry the labels
	//    we put in the Dockerfile so they should never drift; this matters
	//    primarily for prebuilt Jenkins images.
	drift, err := r.computeDrift(ctx, tag, in.Spec)
	if err != nil {
		// Non-fatal: drift detection failure shouldn't block deploys.
		r.emit(name, "drift inspect failed: "+err.Error())
	}
	if len(drift) > 0 {
		for _, d := range drift {
			r.emit(name, "DRIFT: "+d)
		}
		if in.Strict {
			return JARResult{Tag: tag, Drift: drift}, &DriftError{Drift: drift}
		}
	}

	// 3. Ensure the shared network exists.
	if r.Network != "" {
		if err := r.Docker.EnsureNetwork(ctx, r.Network); err != nil {
			return JARResult{Drift: drift}, fmt.Errorf("ensure network: %w", err)
		}
	}

	// 4. Stop & remove old container.
	r.emit(name, "stopping old container")
	if oldID, _ := r.Docker.FindByName(ctx, name); oldID != "" {
		_ = r.Docker.Stop(ctx, oldID, in.StopTimeout)
		_ = r.Docker.Remove(ctx, oldID)
	}

	// 5. Start the new container.
	cspec := docker.ContainerSpec{
		Name:     name,
		Image:    tag,
		Network:  r.Network,
		HostPort: in.Spec.Port,
		MemoryMB: in.Spec.Resource.MemoryMB,
		Env:      in.Spec.Env,
		Labels: map[string]string{
			"mc-operator.server":   name,
			"mc-operator.revision": in.Revision,
		},
	}
	if _, err := r.Docker.Run(ctx, cspec); err != nil {
		r.rollback(ctx, in, "start failed: "+err.Error())
		return JARResult{Drift: drift}, fmt.Errorf("start new container: %w", err)
	}
	r.emit(name, "new container started, awaiting health")

	// 6. Health check with deadline.
	if in.HealthCheck != nil {
		deadline := time.Now().Add(in.HealthBudget)
		var lastErr error
		for time.Now().Before(deadline) {
			if err := in.HealthCheck(ctx, in.Spec); err == nil {
				r.emit(name, "healthy: "+tag)
				return JARResult{Tag: tag, Drift: drift}, nil
			} else {
				lastErr = err
			}
			select {
			case <-ctx.Done():
				return JARResult{Drift: drift}, ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		r.rollback(ctx, in, fmt.Sprintf("healthcheck failed: %v", lastErr))
		return JARResult{Drift: drift}, fmt.Errorf("healthcheck timeout after %s", in.HealthBudget)
	}

	r.emit(name, "deployed (no healthcheck): "+tag)
	return JARResult{Tag: tag, Drift: drift}, nil
}

// computeDrift inspects the freshly-pulled/built image and compares its
// mc-operator.* labels with the manifest spec. Mismatches are returned as
// human-readable strings ("version: image=1.21 manifest=1.20.4").
func (r *Runner) computeDrift(ctx context.Context, tag string, spec mctypes.ServerSpec) ([]string, error) {
	labels, err := r.Docker.ImageLabels(ctx, tag)
	if err != nil {
		return nil, err
	}
	var drift []string
	if v := labels["mc-operator.type"]; v != "" && v != string(spec.Type) {
		drift = append(drift, fmt.Sprintf("type: image=%q manifest=%q", v, spec.Type))
	}
	if v := labels["mc-operator.version"]; v != "" && v != spec.Version {
		drift = append(drift, fmt.Sprintf("version: image=%q manifest=%q", v, spec.Version))
	}
	return drift, nil
}

// IsDriftError reports whether err is a DriftError (so callers can decide
// whether to surface it as 409 Conflict instead of 500).
func IsDriftError(err error) bool {
	var de *DriftError
	return errors.As(err, &de)
}

// rollback replaces the failing container with the previous image.
// Errors during rollback are logged but not returned; the original failure
// is what the caller needs to see.
func (r *Runner) rollback(ctx context.Context, in JARPipelineInput, reason string) {
	name := in.Spec.Name
	r.emit(name, "ROLLBACK: "+reason)
	if in.PreviousImg == "" {
		r.emit(name, "rollback skipped: no previous image recorded")
		return
	}
	if id, _ := r.Docker.FindByName(ctx, name); id != "" {
		_ = r.Docker.Stop(ctx, id, 10*time.Second)
		_ = r.Docker.Remove(ctx, id)
	}
	cspec := docker.ContainerSpec{
		Name:     name,
		Image:    in.PreviousImg,
		Network:  r.Network,
		HostPort: in.Spec.Port,
		MemoryMB: in.Spec.Resource.MemoryMB,
		Env:      in.Spec.Env,
		Labels: map[string]string{
			"mc-operator.server":   name,
			"mc-operator.rollback": "true",
		},
	}
	if _, err := r.Docker.Run(ctx, cspec); err != nil {
		r.emit(name, "ROLLBACK FAILED: "+err.Error())
		return
	}
	r.emit(name, "rollback complete: "+in.PreviousImg)
}
