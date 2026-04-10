package docker

import "context"

// Running returns the image reference and running state of the container
// named `name`. If no such container exists, it returns ("", false, nil)
// rather than an error — a missing container is a normal state for the
// reconciler to observe.
//
// This method makes *docker.Client satisfy gitops.Observer.
func (c *Client) Running(ctx context.Context, name string) (string, bool, error) {
	id, err := c.FindByName(ctx, name)
	if err != nil {
		return "", false, err
	}
	if id == "" {
		return "", false, nil
	}
	info, err := c.api.ContainerInspect(ctx, id)
	if err != nil {
		return "", false, err
	}
	running := info.State != nil && info.State.Running
	img := ""
	if info.Config != nil {
		img = info.Config.Image
	}
	return img, running, nil
}
