package mcimage

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Builder produces OCI/Docker images from a BuildSpec.
// The Docker SDK implementation lives in dockerbuilder.go (v0 stub),
// but the interface lets tests and alternate drivers (buildkit, kaniko) plug in.
type Builder interface {
	// Build creates the image and returns the full image reference (name:tag).
	Build(ctx context.Context, spec BuildSpec, tag string) (string, error)
}

// BuildContext is a writable in-memory tar archive used as the Docker build context.
// Callers add the Dockerfile and any referenced files, then call Bytes() to obtain
// the raw tar to pass to Docker's ImageBuild API.
type BuildContext struct {
	buf *bytes.Buffer
	tw  *tar.Writer
}

// NewBuildContext creates an empty build context.
func NewBuildContext() *BuildContext {
	buf := &bytes.Buffer{}
	return &BuildContext{buf: buf, tw: tar.NewWriter(buf)}
}

// AddBytes writes raw content to the tar at the given path.
func (c *BuildContext) AddBytes(path string, content []byte) error {
	hdr := &tar.Header{
		Name: filepath.ToSlash(path),
		Mode: 0o644,
		Size: int64(len(content)),
	}
	if err := c.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := c.tw.Write(content)
	return err
}

// AddFile copies a host file into the tar at the given destination path.
func (c *BuildContext) AddFile(dst, src string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if info.IsDir() {
		return fmt.Errorf("AddFile does not support directories: %s", src)
	}
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	hdr := &tar.Header{
		Name: filepath.ToSlash(dst),
		Mode: 0o644,
		Size: info.Size(),
	}
	if err := c.tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = io.Copy(c.tw, f)
	return err
}

// Bytes finalizes and returns the complete tar archive.
func (c *BuildContext) Bytes() ([]byte, error) {
	if err := c.tw.Close(); err != nil {
		return nil, err
	}
	return c.buf.Bytes(), nil
}

// PrepareContext builds a tar suitable for `docker build -`. It writes the
// Dockerfile plus the server jar and all plugin jars at their spec-declared paths.
func PrepareContext(spec BuildSpec) ([]byte, error) {
	df, err := RenderDockerfile(spec)
	if err != nil {
		return nil, err
	}
	bc := NewBuildContext()
	if err := bc.AddBytes("Dockerfile", df); err != nil {
		return nil, err
	}
	if err := bc.AddFile(spec.ServerJAR, spec.ServerJAR); err != nil {
		return nil, err
	}
	for _, p := range spec.PluginJARs {
		if err := bc.AddFile(p, p); err != nil {
			return nil, err
		}
	}
	for dst, src := range spec.ExtraFiles {
		if err := bc.AddFile(dst, src); err != nil {
			return nil, err
		}
	}
	return bc.Bytes()
}
