// Package health provides HealthCheck implementations for the jar pipeline.
// Checks are kept deliberately simple: a Minecraft server that accepts TCP
// connections on its game port is considered "ready enough" for the pipeline
// to commit the rollover. Deeper protocol handshakes can be added later.
package health

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

// TCPCheck returns a pipeline.HealthCheck that dials the server's TCP port.
// host is the hostname to dial (typically "localhost" when mc-operator runs
// on the Docker host, or a container DNS name when running in-cluster).
func TCPCheck(host string) func(ctx context.Context, spec mctypes.ServerSpec) error {
	if host == "" {
		host = "localhost"
	}
	return func(ctx context.Context, spec mctypes.ServerSpec) error {
		if spec.Port == 0 {
			return fmt.Errorf("server %q has no port", spec.Name)
		}
		addr := net.JoinHostPort(host, fmt.Sprint(spec.Port))
		d := net.Dialer{Timeout: 2 * time.Second}
		conn, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return err
		}
		_ = conn.Close()
		return nil
	}
}
