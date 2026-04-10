package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
)

// ContainerSpec is a minimal description of a container to run.
type ContainerSpec struct {
	Name        string
	Image       string
	Network     string
	HostPort    int    // host:25565 mapping; 0 = expose only
	ExposedPort int    // internal port (default 25565)
	MemoryMB    int
	Env         map[string]string
	Labels      map[string]string
}

// FindByName returns the container ID for a given name, or "" if not found.
func (c *Client) FindByName(ctx context.Context, name string) (string, error) {
	args := filters.NewArgs(filters.Arg("name", "^/"+name+"$"))
	list, err := c.api.ContainerList(ctx, container.ListOptions{All: true, Filters: args})
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "", nil
	}
	return list[0].ID, nil
}

// Run creates and starts a container described by spec. If a container with
// the same name already exists, it is removed first (force).
func (c *Client) Run(ctx context.Context, spec ContainerSpec) (string, error) {
	if spec.ExposedPort == 0 {
		spec.ExposedPort = 25565
	}
	if existing, err := c.FindByName(ctx, spec.Name); err != nil {
		return "", err
	} else if existing != "" {
		if err := c.Remove(ctx, existing); err != nil {
			return "", fmt.Errorf("remove existing %s: %w", spec.Name, err)
		}
	}

	portKey := nat.Port(fmt.Sprintf("%d/tcp", spec.ExposedPort))
	cfg := &container.Config{
		Image:        spec.Image,
		Env:          envMapToList(spec.Env),
		Labels:       spec.Labels,
		ExposedPorts: nat.PortSet{portKey: struct{}{}},
	}
	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyUnlessStopped},
		Resources:     container.Resources{Memory: int64(spec.MemoryMB) * 1024 * 1024},
	}
	if spec.HostPort > 0 {
		hostCfg.PortBindings = nat.PortMap{
			portKey: []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: fmt.Sprint(spec.HostPort)}},
		}
	}
	var netCfg *network.NetworkingConfig
	if spec.Network != "" {
		netCfg = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				spec.Network: {},
			},
		}
	}

	resp, err := c.api.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, spec.Name)
	if err != nil {
		return "", fmt.Errorf("container create: %w", err)
	}
	if err := c.api.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = c.api.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("container start: %w", err)
	}
	return resp.ID, nil
}

// Stop gracefully stops a container, waiting up to timeout before killing.
func (c *Client) Stop(ctx context.Context, id string, timeout time.Duration) error {
	secs := int(timeout.Seconds())
	return c.api.ContainerStop(ctx, id, container.StopOptions{Timeout: &secs})
}

// Remove force-removes a container (stopping first if needed).
func (c *Client) Remove(ctx context.Context, id string) error {
	return c.api.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// IsRunning reports whether a container ID is currently in the running state.
func (c *Client) IsRunning(ctx context.Context, id string) (bool, error) {
	info, err := c.api.ContainerInspect(ctx, id)
	if err != nil {
		return false, err
	}
	return info.State != nil && info.State.Running, nil
}

// CopyFileToContainer writes a single file into dstPath inside the container.
// The destination directory must exist; this function does not mkdir -p.
func (c *Client) CopyFileToContainer(ctx context.Context, id, dstPath, srcPath string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}
	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	hdr := &tar.Header{
		Name:    filepath.Base(dstPath),
		Mode:    0o644,
		Size:    info.Size(),
		ModTime: info.ModTime(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	dstDir := filepath.ToSlash(filepath.Dir(dstPath))
	return c.api.CopyToContainer(ctx, id, dstDir, buf, types.CopyToContainerOptions{})
}

// Logs streams the last N log lines for a container into w.
func (c *Client) Logs(ctx context.Context, id string, tail int, w io.Writer) error {
	rc, err := c.api.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprint(tail),
	})
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = io.Copy(w, rc)
	return err
}

func envMapToList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// EnsureNetwork creates the named Docker network if it does not exist.
// The network is always created as a user-defined bridge.
func (c *Client) EnsureNetwork(ctx context.Context, name string) error {
	nets, err := c.api.NetworkList(ctx, types.NetworkListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return err
	}
	for _, n := range nets {
		if n.Name == name {
			return nil
		}
	}
	_, err = c.api.NetworkCreate(ctx, name, types.NetworkCreate{Driver: "bridge"})
	return err
}

// ImageRef returns a "name:tag" reference, defaulting tag to "latest".
func ImageRef(name, tag string) string {
	if tag == "" {
		tag = "latest"
	}
	if strings.Contains(name, ":") {
		return name
	}
	return name + ":" + tag
}
