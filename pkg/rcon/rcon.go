// Package rcon is a thin, context-aware wrapper around the Minecraft RCON
// protocol for use by mc-operator's config pipeline. It adds retries,
// timeouts, and server-type-aware reload command selection on top of the
// gorcon/rcon transport library.
package rcon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
	"github.com/gorcon/rcon"
)

// Options configures a Client.
type Options struct {
	Addr     string        // host:port of the RCON endpoint
	Password string        // RCON password
	Timeout  time.Duration // connect + request timeout (default 5s)
	Retries  int           // additional retries on transient failure (default 2)
}

func (o *Options) defaults() {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.Retries < 0 {
		o.Retries = 0
	}
}

// Client is a short-lived RCON connection. Callers should Execute once and
// let Close run via defer; gorcon sessions are not designed for long holding.
type Client struct {
	opts Options
	conn *rcon.Conn
}

// Dial establishes a new RCON connection with the given options.
func Dial(opts Options) (*Client, error) {
	opts.defaults()
	conn, err := rcon.Dial(
		opts.Addr,
		opts.Password,
		rcon.SetDialTimeout(opts.Timeout),
		rcon.SetDeadline(opts.Timeout),
	)
	if err != nil {
		return nil, fmt.Errorf("rcon dial %s: %w", opts.Addr, err)
	}
	return &Client{opts: opts, conn: conn}, nil
}

// Close releases the underlying TCP connection.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

// Execute sends a single command and returns the server response. It respects
// ctx cancellation by racing against a goroutine that calls gorcon's blocking
// Execute (gorcon has no context-native API).
func (c *Client) Execute(ctx context.Context, cmd string) (string, error) {
	if c.conn == nil {
		return "", errors.New("rcon: client not connected")
	}
	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := c.conn.Execute(cmd)
		done <- result{out, err}
	}()
	select {
	case <-ctx.Done():
		// Close forces the goroutine's Execute to unblock with an error.
		_ = c.conn.Close()
		return "", ctx.Err()
	case r := <-done:
		return r.out, r.err
	}
}

// ExecuteWithRetry runs Execute up to 1+Retries times, returning the first success.
func (c *Client) ExecuteWithRetry(ctx context.Context, cmd string) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= c.opts.Retries; attempt++ {
		out, err := c.Execute(ctx, cmd)
		if err == nil {
			return out, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", lastErr
}

// DefaultReloadCommand returns the in-game command that reloads configuration
// files without restarting the JVM, for the given server type.
func DefaultReloadCommand(t mctypes.ServerType) string {
	switch t {
	case mctypes.ServerTypePaper, mctypes.ServerTypeSpigot:
		// `reload confirm` is Bukkit's full plugin-aware reload.
		return "reload confirm"
	case mctypes.ServerTypeVanilla:
		// Vanilla only reloads datapacks/functions.
		return "reload"
	case mctypes.ServerTypeVelocity:
		// Velocity: `velocity reload` reloads velocity.toml at runtime.
		return "velocity reload"
	default:
		return "reload"
	}
}

// Reload runs the appropriate reload command for spec, honoring a custom
// spec.ReloadCommand when set. It returns the server's response text.
func Reload(ctx context.Context, c *Client, spec mctypes.ServerSpec) (string, error) {
	cmd := spec.ReloadCommand
	if cmd == "" {
		cmd = DefaultReloadCommand(spec.Type)
	}
	return c.ExecuteWithRetry(ctx, cmd)
}
