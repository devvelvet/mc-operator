// Command mc-operator is the GitOps daemon and web dashboard for
// declaratively managing Minecraft server infrastructure.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/devvelvet/mc-operator/internal/api"
	"github.com/devvelvet/mc-operator/internal/docker"
	"github.com/devvelvet/mc-operator/internal/download"
	"github.com/devvelvet/mc-operator/internal/gitops"
	"github.com/devvelvet/mc-operator/internal/health"
	"github.com/devvelvet/mc-operator/internal/pipeline"
	"github.com/devvelvet/mc-operator/internal/state"
	"github.com/devvelvet/mc-operator/pkg/manifest"
	"github.com/devvelvet/mc-operator/pkg/mcimage"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mc-operator",
		Short: "Declarative Minecraft server management with GitOps + web dashboard",
	}
	cmd.AddCommand(serveCmd(), validateCmd(), versionCmd())
	return cmd
}

type serveOpts struct {
	addr         string
	statePath    string
	manifestPath string
	repoPath     string
	dockerHost   string
	rconHost     string
	rconPassword string
	proxyOutPath string
	cacheDir     string
	jenkinsToken string
	reconcileInt time.Duration
}

func serveCmd() *cobra.Command {
	opts := serveOpts{}
	c := &cobra.Command{
		Use:   "serve",
		Short: "Run the mc-operator daemon (HTTP API + web dashboard + reconcile loop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(opts)
		},
	}
	c.Flags().StringVar(&opts.addr, "addr", ":8080", "HTTP listen address")
	c.Flags().StringVar(&opts.statePath, "state", "", "state.json path (default: ~/.mc-operator/state.json)")
	c.Flags().StringVar(&opts.manifestPath, "manifest", "", "servers.yaml path")
	c.Flags().StringVar(&opts.repoPath, "repo", "", "local git working copy to watch (optional)")
	c.Flags().StringVar(&opts.dockerHost, "docker-host", "", "docker host override (default: env)")
	c.Flags().StringVar(&opts.rconHost, "rcon-host", "localhost", "host for RCON connections")
	c.Flags().StringVar(&opts.rconPassword, "rcon-password", "", "RCON password (defaults to env MC_RCON_PASSWORD)")
	c.Flags().StringVar(&opts.proxyOutPath, "proxy-config", "", "velocity.toml output path (auto-regenerated on manifest changes)")
	c.Flags().StringVar(&opts.cacheDir, "cache-dir", "", "plugin download cache directory")
	c.Flags().StringVar(&opts.jenkinsToken, "jenkins-token", "", "bearer token for /api/v1/triggers/jenkins (or env MC_JENKINS_TOKEN)")
	c.Flags().DurationVar(&opts.reconcileInt, "interval", 30*time.Second, "reconcile/observation interval")
	return c
}

func runServe(opts serveOpts) error {
	// 1. Resolve defaults that depend on the home directory.
	if opts.statePath == "" {
		home, _ := os.UserHomeDir()
		opts.statePath = filepath.Join(home, ".mc-operator", "state.json")
	}
	if opts.cacheDir == "" {
		opts.cacheDir = filepath.Join(filepath.Dir(opts.statePath), "cache")
	}
	if opts.rconPassword == "" {
		opts.rconPassword = os.Getenv("MC_RCON_PASSWORD")
	}
	if opts.jenkinsToken == "" {
		opts.jenkinsToken = os.Getenv("MC_JENKINS_TOKEN")
	}

	// 2. State store + event broker.
	store, err := state.Open(opts.statePath)
	if err != nil {
		return fmt.Errorf("open state store: %w", err)
	}
	broker := api.NewBroker()

	// 3. Manifest holder — atomic so the http handler and reconciler can both read.
	var current atomic.Pointer[mctypes.Manifest]
	loadManifest := func() error {
		if opts.manifestPath == "" {
			return nil
		}
		m, err := manifest.Load(opts.manifestPath)
		if err != nil {
			return err
		}
		current.Store(m)
		broker.PublishMessage("info", "", "manifest loaded: "+opts.manifestPath)
		return nil
	}
	if err := loadManifest(); err != nil {
		fmt.Fprintln(os.Stderr, "manifest load warning:", err)
	}

	// 4. Try to attach to Docker. Failure is non-fatal — the daemon falls back
	//    to observe-only mode so the dashboard can still be useful on a laptop
	//    without a running Docker daemon.
	var dk *docker.Client
	if d, err := docker.New(opts.dockerHost); err == nil {
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 2*time.Second)
		if pingErr := d.Ping(pingCtx); pingErr == nil {
			dk = d
			fmt.Fprintln(os.Stderr, "docker: connected")
		} else {
			fmt.Fprintln(os.Stderr, "docker: ping failed, observe-only mode:", pingErr)
			_ = d.Close()
		}
		pingCancel()
	} else {
		fmt.Fprintln(os.Stderr, "docker: client init failed, observe-only mode:", err)
	}

	// 5. Download cache + (optional) pipeline runner.
	cache, err := download.New(opts.cacheDir)
	if err != nil {
		return fmt.Errorf("init cache: %w", err)
	}
	var pipelineRunner *pipeline.Runner
	if dk != nil {
		builder := mcimage.NewDockerBuilder(dk.API())
		builder.Out = os.Stderr
		notifier := &brokerNotifier{b: broker}
		pipelineRunner = pipeline.New(dk, builder, notifier)
		pipelineRunner.RepoDir = opts.repoPath
	}

	// 6. API server + history adapter.
	srv := api.New(store, broker)
	srv.Manifest = func() *mctypes.Manifest { return current.Load() }

	// 7. Reconciler — wires everything together. Optional pieces (Pipeline,
	//    Observer, HealthCheck) are nil-safe so observe-only mode still works.
	events := make(chan string, 64)
	rec := &gitops.Reconciler{
		Store:           store,
		Events:          events,
		History:         &historyAdapter{h: srv.History},
		RepoDir:         opts.repoPath,
		Resolver:        gitops.DefaultResolver(cache),
		ProxyConfigPath: opts.proxyOutPath,
		RconAddrFor: func(spec mctypes.ServerSpec) string {
			if opts.rconHost == "" || spec.Port == 0 {
				return ""
			}
			// Convention: RCON listens on game-port + 10 (Paper default).
			return fmt.Sprintf("%s:%d", opts.rconHost, spec.Port+10)
		},
		RconPasswordFor: func(spec mctypes.ServerSpec) string { return opts.rconPassword },
	}
	if dk != nil {
		rec.Observer = dk
		rec.Pipeline = pipelineRunner
		rec.HealthCheck = health.TCPCheck(opts.rconHost)
	}

	srv.Sync = func(ctx context.Context, name string) error {
		m := current.Load()
		if m == nil {
			return fmt.Errorf("no manifest loaded")
		}
		return rec.SyncServer(ctx, m, name)
	}

	// Jenkins webhook trigger. Disabled unless --jenkins-token is configured.
	srv.JenkinsToken = opts.jenkinsToken
	srv.Jenkins = func(ctx context.Context, req api.JenkinsRequest) error {
		m := current.Load()
		if m == nil {
			return fmt.Errorf("no manifest loaded")
		}
		return rec.SyncServerOpts(ctx, m, req.Server, gitops.SyncOptions{
			PrebuiltImage: req.Image,
			Revision:      req.Revision,
			Source:        "jenkins",
			Trigger:       jenkinsTriggerLabel(req),
			Strict:        req.Strict,
			ConfigOverlay: req.ConfigOverlay,
		})
	}

	// 8. Lifecycle.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Bridge reconciler events into the SSE broker.
	go func() {
		for msg := range events {
			broker.PublishMessage("reconcile", "", msg)
		}
	}()

	// 9. Periodic observe loop. Pure observation — never runs pipelines.
	go func() {
		tick := time.NewTicker(opts.reconcileInt)
		defer tick.Stop()
		runOnce := func() {
			m := current.Load()
			if m == nil {
				return
			}
			if err := rec.Reconcile(ctx, m); err != nil {
				broker.PublishMessage("error", "", "observe failed: "+err.Error())
			}
		}
		runOnce()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				runOnce()
			}
		}
	}()

	// 10. Optional Git watcher → reconciler.HandleChanges.
	if opts.repoPath != "" {
		watchEvents := make(chan gitops.WatchEvent, 16)
		w := &gitops.Watcher{
			RepoPath: opts.repoPath,
			Interval: opts.reconcileInt,
			Events:   watchEvents,
		}
		go func() {
			if err := w.Run(ctx); err != nil && ctx.Err() == nil {
				broker.PublishMessage("error", "", "watcher: "+err.Error())
			}
		}()
		go func() {
			for ev := range watchEvents {
				if ev.Err != nil {
					broker.PublishMessage("error", "", "watcher: "+ev.Err.Error())
					continue
				}
				broker.PublishMessage("info", "", "new commit: "+shortSHA(ev.Commit))
				if ev.Summary.HasManifest {
					if err := loadManifest(); err != nil {
						broker.PublishMessage("error", "", "reload manifest: "+err.Error())
						continue
					}
				}
				m := current.Load()
				if m == nil {
					continue
				}
				if err := rec.HandleChanges(ctx, m, ev.Summary, ev.Commit); err != nil {
					broker.PublishMessage("error", "", err.Error())
				}
			}
		}()
	}

	return srv.Run(ctx, opts.addr)
}

// brokerNotifier adapts api.Broker to pipeline.Notifier.
type brokerNotifier struct{ b *api.Broker }

func (n *brokerNotifier) Emit(server, msg string) {
	if n == nil || n.b == nil {
		return
	}
	n.b.PublishMessage("deploy", server, msg)
}

// historyAdapter adapts api.History to gitops.HistoryRecorder.
type historyAdapter struct{ h *api.History }

func (a *historyAdapter) Add(rec gitops.HistoryEntry) {
	if a == nil || a.h == nil {
		return
	}
	a.h.Add(api.DeployRecord{
		Server:  rec.Server,
		Kind:    rec.Kind,
		Status:  rec.Status,
		Message: rec.Message,
		Image:   rec.Image,
		Source:  rec.Source,
		Trigger: rec.Trigger,
		Drift:   rec.Drift,
	})
}

// jenkinsTriggerLabel formats a JenkinsRequest into the trigger label that
// will appear in the dashboard history (e.g. "build #42 (mc-lobby-build)").
func jenkinsTriggerLabel(req api.JenkinsRequest) string {
	switch {
	case req.BuildID != "" && req.JobName != "":
		return "build #" + req.BuildID + " (" + req.JobName + ")"
	case req.BuildID != "":
		return "build #" + req.BuildID
	case req.JobName != "":
		return req.JobName
	default:
		return "jenkins"
	}
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [manifest-path]",
		Short: "Validate a servers.yaml manifest",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := manifest.Load(args[0])
			if err != nil {
				return err
			}
			fmt.Printf("ok: %d servers, proxy.enabled=%v\n", len(m.Servers), m.Proxy.Enabled)
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the mc-operator version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("mc-operator v0.1.0-dev")
		},
	}
}
