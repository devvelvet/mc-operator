package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC(),
	})
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	states := s.Store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"servers": states,
		"count":   len(states),
	})
}

func (s *Server) handleGetServer(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	st, ok := s.Store.Get(name)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	// Look up the spec from the manifest when available so the detail view
	// can show version, plugins, etc.
	var spec any
	if s.Manifest != nil {
		if m := s.Manifest(); m != nil {
			for i := range m.Servers {
				if m.Servers[i].Name == name {
					spec = m.Servers[i]
					break
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"state": st,
		"spec":  spec,
	})
}

func (s *Server) handleServerHistory(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if s.History == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.History.ListForServer(name))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, s.History.ListAll())
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if _, ok := s.Store.Get(name); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if s.Sync == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "manual sync not configured",
		})
		return
	}
	// The sync operation is potentially minutes long (image build + healthcheck
	// budget) so it must run detached from the HTTP request context, otherwise
	// chi's per-request timeout middleware will cancel it. Result and any error
	// surface via the state store, history, and SSE events — exactly the
	// channels the dashboard already subscribes to.
	s.Broker.PublishMessage("sync", name, "manual sync requested")
	go func() {
		if err := s.Sync(context.Background(), name); err != nil {
			s.Broker.PublishMessage("error", name, "sync: "+err.Error())
		}
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued", "server": name})
}

func (s *Server) handleGetManifest(w http.ResponseWriter, r *http.Request) {
	if s.Manifest == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no manifest loaded"})
		return
	}
	m := s.Manifest()
	if m == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no manifest loaded"})
		return
	}
	writeJSON(w, http.StatusOK, m)
}

// handleEvents is a Server-Sent Events endpoint that streams Broker events.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := s.Broker.Subscribe()
	defer unsub()

	// Send an initial comment so the client knows the stream is live.
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	// Heartbeat ticker keeps intermediaries from timing out idle connections.
	hb := time.NewTicker(20 * time.Second)
	defer hb.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-hb.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
			flusher.Flush()
		}
	}
}
