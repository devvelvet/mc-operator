package mcimage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
)

// DockerBuilder is a Builder backed by the Docker Engine API.
// It is safe for concurrent use if the underlying *client.Client is.
type DockerBuilder struct {
	api *client.Client
	// Out receives the streaming build log lines (optional).
	Out io.Writer
}

// NewDockerBuilder wraps an existing Docker client.
func NewDockerBuilder(api *client.Client) *DockerBuilder {
	return &DockerBuilder{api: api}
}

// Build prepares the tar context, streams it to the daemon, and returns the
// full image reference on success. The build log is streamed to b.Out when set.
func (b *DockerBuilder) Build(ctx context.Context, spec BuildSpec, tag string) (string, error) {
	if b.api == nil {
		return "", errors.New("mcimage: DockerBuilder has no Docker client")
	}
	if tag == "" {
		return "", errors.New("mcimage: tag is required")
	}
	tarBytes, err := PrepareContext(spec)
	if err != nil {
		return "", fmt.Errorf("prepare build context: %w", err)
	}

	opts := types.ImageBuildOptions{
		Tags:       []string{tag},
		Dockerfile: "Dockerfile",
		Remove:     true,
		PullParent: true,
		Labels: map[string]string{
			"mc-operator.type":    string(spec.Type),
			"mc-operator.version": spec.Version,
		},
	}

	resp, err := b.api.ImageBuild(ctx, bytes.NewReader(tarBytes), opts)
	if err != nil {
		return "", fmt.Errorf("image build: %w", err)
	}
	defer resp.Body.Close()

	if err := streamBuildLog(resp.Body, b.Out); err != nil {
		return "", err
	}
	return tag, nil
}

// streamBuildLog consumes the docker build output stream. The daemon returns a
// jsonl stream where each line is a JSON object with "stream" or "errorDetail".
func streamBuildLog(body io.Reader, out io.Writer) error {
	dec := json.NewDecoder(body)
	for {
		var msg struct {
			Stream      string `json:"stream,omitempty"`
			ErrorDetail *struct {
				Message string `json:"message"`
			} `json:"errorDetail,omitempty"`
			Error string `json:"error,omitempty"`
		}
		if err := dec.Decode(&msg); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode build stream: %w", err)
		}
		if msg.ErrorDetail != nil && msg.ErrorDetail.Message != "" {
			return fmt.Errorf("build failed: %s", msg.ErrorDetail.Message)
		}
		if msg.Error != "" {
			return fmt.Errorf("build failed: %s", msg.Error)
		}
		if out != nil && msg.Stream != "" {
			_, _ = io.WriteString(out, msg.Stream)
		}
	}
}

// DryRunBuilder renders the Dockerfile and prepares the build context in memory
// without contacting a Docker daemon. Useful for tests and `mc-imagegen --dry-run`.
type DryRunBuilder struct {
	LastDockerfile []byte
	LastContext    []byte
}

// NewDryRunBuilder returns an empty DryRunBuilder.
func NewDryRunBuilder() *DryRunBuilder { return &DryRunBuilder{} }

// Build renders artifacts into the receiver and returns the tag unchanged.
// Unlike DockerBuilder, this does not require the referenced jars to exist on disk
// (it only renders the Dockerfile; PrepareContext would read files).
func (b *DryRunBuilder) Build(ctx context.Context, spec BuildSpec, tag string) (string, error) {
	df, err := RenderDockerfile(spec)
	if err != nil {
		return "", err
	}
	b.LastDockerfile = df
	return tag, nil
}
