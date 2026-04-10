package gitops

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/devvelvet/mc-operator/internal/download"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

// DefaultResolver returns a BuildInputsResolver with convention-based paths:
//
//   - Server jar: {repoDir}/jars/{type}-{version}.jar
//   - Local plugin: {repoDir}/{plugin.path}
//   - URL plugin: downloaded into `cache` (see internal/download).
//
// Pass a nil cache to reject URL-sourced plugins outright.
func DefaultResolver(cache *download.Cache) BuildInputsResolver {
	return func(ctx context.Context, spec mctypes.ServerSpec, repoDir, revision string) (BuildInputs, error) {
		out := BuildInputs{
			ServerJAR: filepath.Join(repoDir, "jars", fmt.Sprintf("%s-%s.jar", spec.Type, spec.Version)),
		}
		for _, p := range spec.Plugins {
			switch p.Source {
			case mctypes.PluginSourceLocal, "":
				out.PluginJARs = append(out.PluginJARs, filepath.Join(repoDir, p.Path))
			case mctypes.PluginSourceURL:
				if cache == nil {
					return BuildInputs{}, fmt.Errorf("plugin %q is URL-sourced but no download cache is configured", p.Name)
				}
				dst, err := cache.Fetch(ctx, p.Path, p.SHA256)
				if err != nil {
					return BuildInputs{}, fmt.Errorf("download %s: %w", p.Name, err)
				}
				out.PluginJARs = append(out.PluginJARs, dst)
			default:
				return BuildInputs{}, fmt.Errorf("plugin %q: unknown source %q", p.Name, p.Source)
			}
		}
		return out, nil
	}
}
