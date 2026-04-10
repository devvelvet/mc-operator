package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
	"github.com/devvelvet/mc-operator/pkg/rcon"
)

// ConfigPipelineInput describes a single invocation of the config pipeline.
type ConfigPipelineInput struct {
	Spec     mctypes.ServerSpec
	Changed  []string // repo-relative config file paths that were modified
	RconAddr string   // host:port (e.g. "localhost:25575")
	RconPass string
}

// RunConfig copies the changed config files into the running container's
// bind-mount directory and triggers an in-game reload over RCON. It is a
// no-op (successful) if the change list is empty.
//
// The container is NOT restarted. If reload fails, the caller may escalate
// to a container restart via the JAR pipeline or a direct Docker.Stop.
func (r *Runner) RunConfig(ctx context.Context, in ConfigPipelineInput) error {
	if len(in.Changed) == 0 {
		return nil
	}
	if in.Spec.Name == "" {
		return fmt.Errorf("config pipeline: spec.Name required")
	}
	if r.Docker == nil {
		return fmt.Errorf("config pipeline: docker client not configured")
	}

	r.emit(in.Spec.Name, fmt.Sprintf("config sync: %d files", len(in.Changed)))

	// Locate the running container.
	id, err := r.Docker.FindByName(ctx, in.Spec.Name)
	if err != nil {
		return fmt.Errorf("find container: %w", err)
	}
	if id == "" {
		return fmt.Errorf("container %q not found", in.Spec.Name)
	}

	// Copy each changed file into the container. The destination path is
	// derived from the repo-relative path under spec.ConfigDir.
	for _, rel := range in.Changed {
		src := filepath.Join(r.RepoDir, rel)
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("stat %s: %w", src, err)
		}
		dst, ok := mapConfigPath(rel, in.Spec.ConfigDir)
		if !ok {
			// File is not part of this server's config dir; skip silently.
			continue
		}
		if err := r.Docker.CopyFileToContainer(ctx, id, dst, src); err != nil {
			return fmt.Errorf("copy %s -> %s: %w", rel, dst, err)
		}
	}

	// Trigger in-server reload.
	if in.RconAddr == "" {
		r.emit(in.Spec.Name, "config synced; no rcon address configured, skipping reload")
		return nil
	}
	client, err := rcon.Dial(rcon.Options{Addr: in.RconAddr, Password: in.RconPass})
	if err != nil {
		return fmt.Errorf("rcon dial: %w", err)
	}
	defer client.Close()

	out, err := rcon.Reload(ctx, client, in.Spec)
	if err != nil {
		return fmt.Errorf("rcon reload: %w", err)
	}
	r.emit(in.Spec.Name, "reload: "+trimOutput(out))
	return nil
}

// mapConfigPath converts a repo-relative path (e.g. "configs/lobby/paper.yml")
// into a container-absolute path (e.g. "/server/config/paper.yml"), based on
// the server's declared ConfigDir. Returns false if the file is not inside
// the config dir.
func mapConfigPath(repoRel, configDir string) (string, bool) {
	if configDir == "" {
		return "", false
	}
	prefix := filepath.ToSlash(configDir)
	if !strings.HasPrefix(strings.TrimSuffix(prefix, "/")+"/", prefix+"/") {
		prefix = prefix + "/"
	}
	rel := filepath.ToSlash(repoRel)
	if !strings.HasPrefix(rel, prefix) {
		return "", false
	}
	inner := strings.TrimPrefix(rel, prefix)
	return "/server/config/" + inner, true
}

// FullConfigSync copies every file under spec.ConfigDir into a freshly-deployed
// container, then triggers an in-game reload. It is the "config overlay" used
// after a Jenkins deploy: the image carries baked-in defaults, then the repo's
// current configs are layered on top so the live container reflects whatever
// is in version control.
//
// repoDir is the host path of the working copy. The function silently no-ops
// if the server has no ConfigDir or repoDir is unset.
func (r *Runner) FullConfigSync(ctx context.Context, spec mctypes.ServerSpec, repoDir, rconAddr, rconPass string) error {
	if spec.ConfigDir == "" || repoDir == "" {
		return nil
	}
	root := filepath.Join(repoDir, spec.ConfigDir)
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat config dir %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("config dir is not a directory: %s", root)
	}

	var changed []string
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(repoDir, path)
		if err != nil {
			return err
		}
		changed = append(changed, filepath.ToSlash(rel))
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("walk config dir: %w", walkErr)
	}
	if len(changed) == 0 {
		return nil
	}

	r.emit(spec.Name, fmt.Sprintf("config overlay: %d files", len(changed)))
	saved := r.RepoDir
	r.RepoDir = repoDir
	defer func() { r.RepoDir = saved }()
	return r.RunConfig(ctx, ConfigPipelineInput{
		Spec:     spec,
		Changed:  changed,
		RconAddr: rconAddr,
		RconPass: rconPass,
	})
}

func trimOutput(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}
