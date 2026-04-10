// Package docker wraps the Docker Engine API for use by mc-operator.
// It centralizes client construction so the pipeline, image builder,
// and reconciler all share the same configuration and lifecycle.
package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/client"
)

// Client is the shared Docker Engine API client wrapper.
type Client struct {
	api *client.Client
}

// New constructs a Docker client. If host is empty, environment defaults
// (DOCKER_HOST, DOCKER_TLS_VERIFY, etc.) are used.
func New(host string) (*Client, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host != "" {
		opts = append(opts, client.WithHost(host))
	} else {
		opts = append(opts, client.FromEnv)
	}
	api, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{api: api}, nil
}

// API returns the underlying Docker client. Prefer the higher-level
// methods on this package; use API() only when a capability is not yet wrapped.
func (c *Client) API() *client.Client { return c.api }

// Ping verifies the Docker daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.api.Ping(ctx)
	return err
}

// Close releases the underlying client resources.
func (c *Client) Close() error {
	if c.api == nil {
		return nil
	}
	return c.api.Close()
}
