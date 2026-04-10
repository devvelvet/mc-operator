// Package api hosts the HTTP API and embedded web dashboard for mc-operator.
// It exposes a small REST surface plus an SSE stream for live updates, and
// serves the static dashboard from an embed.FS.
package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/devvelvet/mc-operator/internal/state"
	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

// SyncTrigger is invoked when a user clicks "sync" in the dashboard.
// It is wired by cmd/mc-operator so the api package does not depend on
// the reconciler directly. Return an error to surface a failure in the HTTP
// response; nil error means the sync was enqueued successfully.
type SyncTrigger func(ctx context.Context, serverName string) error

// Server holds the dependencies needed by API handlers.
type Server struct {
	Store        *state.Store
	Broker       *Broker
	History      *History
	Manifest     func() *mctypes.Manifest // callback returning current manifest snapshot
	Sync         SyncTrigger              // optional manual-sync hook
	Jenkins      JenkinsTrigger           // optional jenkins-webhook hook
	JenkinsToken string                   // shared bearer token for /triggers/jenkins
}

// New constructs a Server. Manifest may be nil until one is loaded.
func New(store *state.Store, broker *Broker) *Server {
	return &Server{Store: store, Broker: broker, History: NewHistory(200)}
}

// Router returns the HTTP handler tree.
//
// Per-request timeout middleware is intentionally NOT applied at the router
// level: the SSE events endpoint is a long-lived stream and the manual sync
// endpoint dispatches into a long-running goroutine. http.Server.ReadHeaderTimeout
// already protects against slowloris-style attacks on connection establishment.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)

	r.Route("/api/v1", func(api chi.Router) {
		api.Get("/healthz", s.handleHealth)
		api.Get("/servers", s.handleListServers)
		api.Get("/servers/{name}", s.handleGetServer)
		api.Get("/servers/{name}/history", s.handleServerHistory)
		api.Post("/servers/{name}/sync", s.handleSync)
		api.Get("/history", s.handleHistory)
		api.Get("/manifest", s.handleGetManifest)
		api.Get("/events", s.handleEvents) // SSE — must not be timed out
		api.Post("/triggers/jenkins", s.handleJenkins)
	})

	// Embedded dashboard under /.
	r.Handle("/*", WebHandler())
	return r
}

// Run starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Printf("mc-operator API listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
