package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
)

// PullImage pulls a (possibly remote) image reference from a registry. The
// progress stream is drained but not surfaced to the caller.
func (c *Client) PullImage(ctx context.Context, ref string) error {
	rc, err := c.api.ImagePull(ctx, ref, types.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("image pull %s: %w", ref, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("image pull stream %s: %w", ref, err)
	}
	return nil
}

// EnsureImage makes sure ref is available locally. If the image already exists
// it is a no-op; otherwise it is pulled. This is the right entry point for the
// jar pipeline because Jenkins-built images may exist locally on a single-host
// setup (no registry round-trip needed) or only remotely.
func (c *Client) EnsureImage(ctx context.Context, ref string) error {
	if _, _, err := c.api.ImageInspectWithRaw(ctx, ref); err == nil {
		return nil
	}
	return c.PullImage(ctx, ref)
}

// ImageLabels inspects an image (which must already be present locally) and
// returns its label map. Used by the jar pipeline to detect drift between a
// prebuilt Jenkins image and the manifest spec.
func (c *Client) ImageLabels(ctx context.Context, ref string) (map[string]string, error) {
	info, _, err := c.api.ImageInspectWithRaw(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", ref, err)
	}
	if info.Config == nil {
		return nil, nil
	}
	return info.Config.Labels, nil
}
