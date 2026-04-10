// Package gitops implements the GitOps reconciliation loop for mc-operator.
// It is the orchestrator that connects:
//   - the manifest (desired state),
//   - the state store (last-known deploy state),
//   - the pipeline runner (mutations),
//   - an Observer (actual state on the Docker host),
//   - and the velocity proxy generator.
//
// The reconciler itself holds no Docker/Git-specific types — those are
// injected via interfaces so unit tests can stub them out.
package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/devvelvet/mc-operator/internal/pipeline"
	"github.com/devvelvet/mc-operator/internal/state"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
	"github.com/devvelvet/mc-operator/pkg/proxy"
)

// PipelineExecutor is the subset of pipeline.Runner the reconciler needs.
// RunJAR returns a JARResult so the reconciler can persist the deployed
// image tag for rollback and surface drift information to the dashboard.
type PipelineExecutor interface {
	RunConfig(ctx context.Context, in pipeline.ConfigPipelineInput) error
	RunJAR(ctx context.Context, in pipeline.JARPipelineInput) (pipeline.JARResult, error)
	FullConfigSync(ctx context.Context, spec mctypes.ServerSpec, repoDir, rconAddr, rconPass string) error
}

// Observer returns the actual running state of a named container.
type Observer interface {
	Running(ctx context.Context, name string) (image string, running bool, err error)
}

// HistoryRecorder receives deploy events for the dashboard history view.
type HistoryRecorder interface {
	Add(rec HistoryEntry)
}

// HistoryEntry is the structured deploy record handed to a HistoryRecorder.
// Defined here (not in internal/api) so internal/gitops doesn't depend on
// the api package.
type HistoryEntry struct {
	Server  string
	Kind    string
	Status  string
	Message string
	Image   string
	Source  string
	Trigger string
	Drift   []string
}

// SyncOptions controls a single SyncServer invocation. Fields are nil-safe;
// the zero value behaves like a manual dashboard click.
type SyncOptions struct {
	// PrebuiltImage, if set, causes the jar pipeline to pull this image
	// instead of building from local jars. Used by Jenkins triggers.
	PrebuiltImage string

	// Revision is recorded in state and used as the image tag suffix when
	// building locally. For Jenkins this is typically a git commit sha.
	Revision string

	// Source identifies the trigger ("manual" / "jenkins" / "git").
	Source string

	// Trigger is human-readable trigger info ("build #42 (mc-lobby-build)").
	Trigger string

	// Strict instructs the pipeline to fail with a DriftError if the prebuilt
	// image disagrees with the manifest spec on type/version labels.
	Strict bool

	// ConfigOverlay enables a post-deploy config sync: after the new container
	// is healthy, all files under spec.ConfigDir are copied in and a reload is
	// issued. This keeps Jenkins-built images aligned with the repo's configs.
	ConfigOverlay bool
}

// BuildInputs are the host paths feeding an image build.
type BuildInputs struct {
	ServerJAR  string
	PluginJARs []string
}

// BuildInputsResolver materializes build inputs (server jar + plugin jars)
// for a given server, possibly downloading URL-sourced plugins.
type BuildInputsResolver func(ctx context.Context, spec mctypes.ServerSpec, repoDir, revision string) (BuildInputs, error)

// Reconciler orchestrates manifest -> pipeline -> state transitions.
type Reconciler struct {
	Store    *state.Store
	Pipeline PipelineExecutor
	Observer Observer
	History  HistoryRecorder
	Events   chan<- string

	RepoDir         string
	Resolver        BuildInputsResolver
	HealthCheck     pipeline.HealthCheck
	RconAddrFor     func(spec mctypes.ServerSpec) string
	RconPasswordFor func(spec mctypes.ServerSpec) string

	// ProxyConfigPath, when set, causes velocity.toml to be written on every
	// reconcile pass where the manifest enables the proxy.
	ProxyConfigPath string
}

// Reconcile performs observation-only reconciliation: it updates the state
// store to reflect observed reality but does not run pipelines. It is safe
// to call on a schedule (e.g. every 30s) because it never mutates containers.
func (r *Reconciler) Reconcile(ctx context.Context, m *mctypes.Manifest) error {
	if m == nil {
		return fmt.Errorf("nil manifest")
	}
	for _, spec := range m.Servers {
		cur, _ := r.Store.Get(spec.Name)
		cur.Name = spec.Name
		if cur.Port == 0 && spec.Port != 0 {
			cur.Port = spec.Port
		}
		r.observeOne(ctx, &cur)
		if err := r.Store.Upsert(cur); err != nil {
			return err
		}
	}
	r.emit(fmt.Sprintf("observed %d servers", len(m.Servers)))
	r.writeProxyConfig(m)
	return nil
}

// observeOne fills sync/health fields on `cur` based on the Observer (if set).
// Without an Observer the reconciler runs in pure state-store mode and leaves
// existing sync/health values in place.
func (r *Reconciler) observeOne(ctx context.Context, cur *state.ServerState) {
	if r.Observer == nil {
		if cur.Sync == "" {
			cur.Sync = mctypes.SyncStatusUnknown
		}
		if cur.Health == "" {
			cur.Health = mctypes.HealthUnknown
		}
		return
	}
	img, running, err := r.Observer.Running(ctx, cur.Name)
	if err != nil {
		cur.Sync = mctypes.SyncStatusUnknown
		cur.Health = mctypes.HealthUnknown
		cur.Message = err.Error()
		return
	}
	if !running {
		cur.Health = mctypes.HealthMissing
		cur.Sync = mctypes.SyncStatusOutOfSync
		return
	}
	cur.Health = mctypes.HealthHealthy
	if cur.CurrentImage != "" && img != cur.CurrentImage {
		cur.Sync = mctypes.SyncStatusOutOfSync
	} else {
		cur.Sync = mctypes.SyncStatusSynced
		if cur.CurrentImage == "" {
			cur.CurrentImage = img
		}
	}
	cur.Message = ""
}

// HandleChanges is called by the git watcher when HEAD advances. It picks
// the right pipeline for the diff and executes it for every affected server.
func (r *Reconciler) HandleChanges(ctx context.Context, m *mctypes.Manifest, summary DiffSummary, revision string) error {
	if m == nil {
		return fmt.Errorf("nil manifest")
	}
	if r.Pipeline == nil {
		// Observe-only mode.
		return r.Reconcile(ctx, m)
	}
	if summary.RequiresJARPipeline() {
		// Manifest or jar changes affect the image — rebuild all gameplay
		// servers. The proxy is handled separately via ProxyConfigPath.
		for _, spec := range m.Servers {
			if spec.Type.IsProxy() {
				continue
			}
			_ = r.runJAR(ctx, spec, revision)
		}
	} else if summary.RequiresConfigPipeline() {
		for _, spec := range MapServersByConfig(m, summary.ConfigPaths) {
			_ = r.runConfig(ctx, spec, summary.ConfigPaths)
		}
	}
	r.writeProxyConfig(m)
	return r.Reconcile(ctx, m)
}

// SyncServer runs the jar pipeline for a single named server with default
// options (manual dashboard sync). Equivalent to SyncServerOpts with
// SyncOptions{Source: "manual", Revision: "manual"}.
func (r *Reconciler) SyncServer(ctx context.Context, m *mctypes.Manifest, name string) error {
	return r.SyncServerOpts(ctx, m, name, SyncOptions{Source: "manual", Revision: "manual"})
}

// SyncServerOpts is the parameterized form used by Jenkins triggers and other
// non-manual sync sources.
func (r *Reconciler) SyncServerOpts(ctx context.Context, m *mctypes.Manifest, name string, opts SyncOptions) error {
	if r.Pipeline == nil {
		return fmt.Errorf("pipeline not configured")
	}
	if opts.Source == "" {
		opts.Source = "manual"
	}
	for _, spec := range m.Servers {
		if spec.Name == name {
			return r.runJARWithOpts(ctx, spec, opts)
		}
	}
	return fmt.Errorf("server %q not in manifest", name)
}

// runJAR is the legacy entry point used by the watcher pipeline branch
// (manifest/jar diff). It performs a build-from-local-files deploy.
func (r *Reconciler) runJAR(ctx context.Context, spec mctypes.ServerSpec, revision string) error {
	return r.runJARWithOpts(ctx, spec, SyncOptions{Source: "git", Revision: revision})
}

// runJARWithOpts is the unified jar pipeline entry point. It handles both
// build-from-source (Resolver) and pull-prebuilt-image (PrebuiltImage) flows,
// records source/trigger/drift in history, and runs the optional config
// overlay step on success.
func (r *Reconciler) runJARWithOpts(ctx context.Context, spec mctypes.ServerSpec, opts SyncOptions) error {
	r.markProgressing(spec)

	in := pipeline.JARPipelineInput{
		Spec:        spec,
		Revision:    shortRev(opts.Revision),
		HealthCheck: r.HealthCheck,
		Strict:      opts.Strict,
	}
	prev, _ := r.Store.Get(spec.Name)
	in.PreviousImg = prev.CurrentImage

	if opts.PrebuiltImage != "" {
		in.PrebuiltImage = opts.PrebuiltImage
	} else {
		if r.Resolver == nil {
			return r.markFailedWithSource(spec, opts, "build inputs resolver not configured", nil)
		}
		inputs, err := r.Resolver(ctx, spec, r.RepoDir, opts.Revision)
		if err != nil {
			return r.markFailedWithSource(spec, opts, "resolve inputs: "+err.Error(), nil)
		}
		in.ServerJAR = inputs.ServerJAR
		in.PluginJARs = inputs.PluginJARs
	}

	result, err := r.Pipeline.RunJAR(ctx, in)
	if err != nil {
		return r.markFailedWithSource(spec, opts, err.Error(), result.Drift)
	}

	// Optional post-deploy config overlay (Jenkins drift handling).
	if opts.ConfigOverlay && spec.ConfigDir != "" && r.RepoDir != "" {
		rconAddr := ""
		rconPass := ""
		if r.RconAddrFor != nil {
			rconAddr = r.RconAddrFor(spec)
		}
		if r.RconPasswordFor != nil {
			rconPass = r.RconPasswordFor(spec)
		}
		if err := r.Pipeline.FullConfigSync(ctx, spec, r.RepoDir, rconAddr, rconPass); err != nil {
			r.emit("config overlay failed for " + spec.Name + ": " + err.Error())
			// Non-fatal: the image is deployed, the overlay is best-effort.
		}
	}

	return r.markSyncedWithSource(spec, opts, result.Tag, result.Drift)
}

func (r *Reconciler) runConfig(ctx context.Context, spec mctypes.ServerSpec, changed []string) error {
	r.markProgressing(spec)
	in := pipeline.ConfigPipelineInput{
		Spec:    spec,
		Changed: changed,
	}
	if r.RconAddrFor != nil {
		in.RconAddr = r.RconAddrFor(spec)
	}
	if r.RconPasswordFor != nil {
		in.RconPass = r.RconPasswordFor(spec)
	}
	if err := r.Pipeline.RunConfig(ctx, in); err != nil {
		return r.markFailed(spec, err.Error())
	}
	return r.markSynced(spec, "", "")
}

func (r *Reconciler) markProgressing(spec mctypes.ServerSpec) {
	cur, _ := r.Store.Get(spec.Name)
	cur.Name = spec.Name
	cur.Sync = mctypes.SyncStatusProgressng
	cur.Health = mctypes.HealthProgressing
	cur.Message = ""
	_ = r.Store.Upsert(cur)
	r.emit("progressing: " + spec.Name)
}

func (r *Reconciler) markSynced(spec mctypes.ServerSpec, revision, image string) error {
	return r.markSyncedWithSource(spec, SyncOptions{Source: "git", Revision: revision}, image, nil)
}

func (r *Reconciler) markSyncedWithSource(spec mctypes.ServerSpec, opts SyncOptions, image string, drift []string) error {
	cur, _ := r.Store.Get(spec.Name)
	cur.Name = spec.Name
	cur.Sync = mctypes.SyncStatusSynced
	cur.Health = mctypes.HealthHealthy
	cur.Message = ""
	if opts.Revision != "" && opts.Revision != "manual" {
		cur.LastCommit = opts.Revision
	}
	if image != "" && image != cur.CurrentImage {
		cur.PreviousImage = cur.CurrentImage
		cur.CurrentImage = image
	}
	cur.LastDeployedAt = time.Now().UTC()
	if err := r.Store.Upsert(cur); err != nil {
		return err
	}
	if r.History != nil {
		msg := "synced"
		if len(drift) > 0 {
			msg = fmt.Sprintf("synced with drift (%d)", len(drift))
		}
		r.History.Add(HistoryEntry{
			Server:  spec.Name,
			Kind:    "deploy",
			Status:  "success",
			Message: msg,
			Image:   image,
			Source:  opts.Source,
			Trigger: opts.Trigger,
			Drift:   drift,
		})
	}
	r.emit("synced: " + spec.Name)
	return nil
}

func (r *Reconciler) markFailed(spec mctypes.ServerSpec, msg string) error {
	return r.markFailedWithSource(spec, SyncOptions{Source: "git"}, msg, nil)
}

func (r *Reconciler) markFailedWithSource(spec mctypes.ServerSpec, opts SyncOptions, msg string, drift []string) error {
	cur, _ := r.Store.Get(spec.Name)
	cur.Name = spec.Name
	cur.Sync = mctypes.SyncStatusFailed
	cur.Health = mctypes.HealthDegraded
	cur.Message = msg
	_ = r.Store.Upsert(cur)
	if r.History != nil {
		r.History.Add(HistoryEntry{
			Server:  spec.Name,
			Kind:    "deploy",
			Status:  "failed",
			Message: msg,
			Source:  opts.Source,
			Trigger: opts.Trigger,
			Drift:   drift,
		})
	}
	r.emit("failed: " + spec.Name + ": " + msg)
	return fmt.Errorf("%s: %s", spec.Name, msg)
}

func (r *Reconciler) writeProxyConfig(m *mctypes.Manifest) {
	if r.ProxyConfigPath == "" || !m.Proxy.Enabled {
		return
	}
	cfg, err := proxy.FromManifest(m)
	if err != nil {
		r.emit("proxy config: " + err.Error())
		return
	}
	b, err := cfg.TOML()
	if err != nil {
		r.emit("proxy config: " + err.Error())
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.ProxyConfigPath), 0o755); err != nil {
		r.emit("proxy config mkdir: " + err.Error())
		return
	}
	if err := os.WriteFile(r.ProxyConfigPath, b, 0o644); err != nil {
		r.emit("proxy config write: " + err.Error())
		return
	}
}

func (r *Reconciler) emit(msg string) {
	if r.Events == nil {
		return
	}
	select {
	case r.Events <- msg:
	case <-time.After(50 * time.Millisecond):
	}
}

func shortRev(rev string) string {
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		return rev[:7]
	}
	return rev
}

// MapServersByConfig returns the servers whose declared ConfigDir is a
// prefix of any of the changed paths.
func MapServersByConfig(m *mctypes.Manifest, paths []string) []mctypes.ServerSpec {
	var out []mctypes.ServerSpec
	for _, s := range m.Servers {
		if s.ConfigDir == "" {
			continue
		}
		prefix := filepath.ToSlash(s.ConfigDir)
		if !strings.HasSuffix(prefix, "/") {
			prefix += "/"
		}
		for _, p := range paths {
			if strings.HasPrefix(filepath.ToSlash(p), prefix) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}
