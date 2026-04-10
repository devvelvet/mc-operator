package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// JenkinsTrigger is the callback the daemon wires to dispatch a Jenkins
// webhook into the reconciler. It is intentionally decoupled from the
// internal/gitops package so the api package has no upstream dependencies.
//
// strict and configOverlay are forwarded to the underlying SyncOptions.
type JenkinsTrigger func(ctx context.Context, req JenkinsRequest) error

// JenkinsRequest is the validated payload of a /api/v1/triggers/jenkins call.
type JenkinsRequest struct {
	// Server is the manifest server name to deploy. Required.
	Server string `json:"server"`

	// Image is the prebuilt image reference Jenkins built and pushed.
	// When empty, mc-operator falls back to a normal sync (build from local files).
	Image string `json:"image,omitempty"`

	// Revision is the source commit sha (recorded in state.lastCommit).
	Revision string `json:"revision,omitempty"`

	// BuildID + JobName produce the human-readable trigger label shown in
	// the dashboard history (e.g. "build #42 (mc-lobby-build)").
	BuildID string `json:"buildId,omitempty"`
	JobName string `json:"jobName,omitempty"`

	// Strict, when true, makes the deploy fail with 409 Conflict if the
	// image's labels don't match the manifest spec.
	Strict bool `json:"strict,omitempty"`

	// ConfigOverlay, when true, applies the current repo configs on top of
	// the deployed image after it becomes healthy.
	ConfigOverlay bool `json:"configOverlay,omitempty"`
}

// trigger returns a human-readable label for history.
func (r JenkinsRequest) trigger() string {
	switch {
	case r.BuildID != "" && r.JobName != "":
		return "build #" + r.BuildID + " (" + r.JobName + ")"
	case r.BuildID != "":
		return "build #" + r.BuildID
	case r.JobName != "":
		return r.JobName
	default:
		return "jenkins"
	}
}

// handleJenkins is the POST /api/v1/triggers/jenkins handler. It enforces
// bearer-token auth, parses the JSON body, and dispatches to s.Jenkins.
func (s *Server) handleJenkins(w http.ResponseWriter, r *http.Request) {
	if s.JenkinsToken == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "jenkins endpoint disabled (no --jenkins-token configured)",
		})
		return
	}
	if !checkBearer(r, s.JenkinsToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if s.Jenkins == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "jenkins trigger not wired",
		})
		return
	}

	var req JenkinsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json: " + err.Error()})
		return
	}
	if req.Server == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "server is required"})
		return
	}
	if _, ok := s.Store.Get(req.Server); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "server not found: " + req.Server})
		return
	}

	s.Broker.PublishMessage("sync", req.Server, "jenkins trigger: "+req.trigger())

	// Run the deploy detached so the HTTP request returns promptly. Errors
	// surface via SSE events and history records — the same channels the
	// dashboard already subscribes to.
	go func() {
		err := s.Jenkins(context.Background(), req)
		if err == nil {
			return
		}
		// Strict-mode drift errors are reported as a distinct event type so
		// dashboards can highlight them.
		if isDriftErr(err) {
			s.Broker.PublishMessage("drift", req.Server, "jenkins deploy rejected (strict): "+err.Error())
			return
		}
		s.Broker.PublishMessage("error", req.Server, "jenkins deploy: "+err.Error())
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":  "queued",
		"server":  req.Server,
		"image":   req.Image,
		"trigger": req.trigger(),
	})
}

// checkBearer validates the Authorization: Bearer <token> header.
// Constant-time comparison avoids leaking the token via timing.
func checkBearer(r *http.Request, expected string) bool {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := strings.TrimSpace(h[len(prefix):])
	if len(got) != len(expected) {
		return false
	}
	var diff byte
	for i := 0; i < len(got); i++ {
		diff |= got[i] ^ expected[i]
	}
	return diff == 0
}

// driftErr is the interface satisfied by pipeline.DriftError. We can't import
// internal/pipeline from internal/api without creating a cycle, so we sniff
// it via Error() prefix matching. The pipeline.DriftError.Error() output is
// stable: "manifest/image drift: [...]".
func isDriftErr(err error) bool {
	if err == nil {
		return false
	}
	type unwrapper interface{ Unwrap() error }
	for err != nil {
		if strings.HasPrefix(err.Error(), "manifest/image drift") {
			return true
		}
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	var asErr interface{ Error() string }
	if errors.As(err, &asErr) && strings.Contains(asErr.Error(), "manifest/image drift") {
		return true
	}
	return false
}
