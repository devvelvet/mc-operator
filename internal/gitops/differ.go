package gitops

import (
	"path"
	"path/filepath"
	"strings"
)

// ChangeKind classifies a changed file into a pipeline category.
type ChangeKind int

const (
	KindUnknown ChangeKind = iota
	KindConfig             // config file — config pipeline (RCON reload)
	KindJAR                // jar or manifest field change — jar pipeline (rebuild)
	KindManifest           // servers.yaml itself changed
	KindIgnored            // not relevant (docs, .gitignore, .github, etc.)
)

// Change is a single classified file change from a git diff.
type Change struct {
	Path string
	Kind ChangeKind
}

// DiffClassifier decides whether a changed file should trigger the config
// pipeline, the jar pipeline, or be ignored. It is configuration-driven so
// projects can extend the recognized config extensions without forking.
type DiffClassifier struct {
	// ConfigExts are file extensions considered config files (with leading dot).
	ConfigExts []string
	// ManifestName is the path (relative to repo root) of the manifest file.
	ManifestName string
	// IgnoredDirs are top-level directories whose changes never trigger pipelines.
	IgnoredDirs []string
}

// DefaultClassifier returns the standard mc-operator file classifier.
func DefaultClassifier() *DiffClassifier {
	return &DiffClassifier{
		ConfigExts:   []string{".yml", ".yaml", ".properties", ".toml", ".json", ".conf", ".cfg"},
		ManifestName: "servers.yaml",
		IgnoredDirs:  []string{".git", ".github", "docs", "examples"},
	}
}

// Classify returns the ChangeKind for a single file path.
func (c *DiffClassifier) Classify(p string) ChangeKind {
	p = filepath.ToSlash(p)

	// Manifest takes precedence — a manifest edit can imply version/type change
	// and therefore a jar rebuild.
	if p == c.ManifestName || path.Base(p) == c.ManifestName {
		return KindManifest
	}

	// Ignored top-level directories.
	for _, d := range c.IgnoredDirs {
		if strings.HasPrefix(p, d+"/") || p == d {
			return KindIgnored
		}
	}

	ext := strings.ToLower(path.Ext(p))
	if ext == ".jar" {
		return KindJAR
	}
	for _, ce := range c.ConfigExts {
		if ext == ce {
			return KindConfig
		}
	}
	return KindUnknown
}

// ClassifyAll classifies a set of file paths at once.
func (c *DiffClassifier) ClassifyAll(paths []string) []Change {
	out := make([]Change, 0, len(paths))
	for _, p := range paths {
		out = append(out, Change{Path: p, Kind: c.Classify(p)})
	}
	return out
}

// DiffSummary aggregates the outcome of classifying many file changes and
// answers the one question the reconciler actually cares about: which pipeline
// should run?
type DiffSummary struct {
	Changes        []Change
	HasJAR         bool
	HasConfig      bool
	HasManifest    bool
	ConfigPaths    []string
	JARPaths       []string
}

// Summarize reduces a list of changes into a DiffSummary.
func Summarize(changes []Change) DiffSummary {
	s := DiffSummary{Changes: changes}
	for _, c := range changes {
		switch c.Kind {
		case KindJAR:
			s.HasJAR = true
			s.JARPaths = append(s.JARPaths, c.Path)
		case KindConfig:
			s.HasConfig = true
			s.ConfigPaths = append(s.ConfigPaths, c.Path)
		case KindManifest:
			s.HasManifest = true
		}
	}
	return s
}

// RequiresJARPipeline is true when the summary implies a full image rebuild.
// A manifest change also triggers the jar pipeline because version/type may
// have changed, which cannot be applied via in-server reload.
func (s DiffSummary) RequiresJARPipeline() bool {
	return s.HasJAR || s.HasManifest
}

// RequiresConfigPipeline is true when only config files changed.
func (s DiffSummary) RequiresConfigPipeline() bool {
	return s.HasConfig && !s.RequiresJARPipeline()
}
