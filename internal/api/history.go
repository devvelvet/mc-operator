package api

import (
	"sync"
	"time"
)

// DeployRecord is one deployment or rollback event, kept for the dashboard history view.
type DeployRecord struct {
	ID        string    `json:"id"`
	Server    string    `json:"server"`
	Kind      string    `json:"kind"` // "config" | "jar" | "rollback" | "sync"
	Revision  string    `json:"revision,omitempty"`
	Image     string    `json:"image,omitempty"`
	Status    string    `json:"status"` // "success" | "failed" | "in_progress"
	Message   string    `json:"message,omitempty"`
	// Source identifies the trigger that initiated the deploy:
	// "manual" (dashboard button), "git" (watcher), "jenkins" (webhook).
	Source string `json:"source,omitempty"`
	// Trigger is human-readable trigger info, e.g. "build #42 (mc-lobby-build)".
	Trigger string `json:"trigger,omitempty"`
	// Drift lists any inconsistencies discovered between the image being
	// deployed and the manifest spec (e.g. version label mismatch). Empty
	// when no drift was observed.
	Drift     []string  `json:"drift,omitempty"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt,omitempty"`
}

// History is a thread-safe ring buffer of DeployRecords.
// It is not persisted — history is lost on daemon restart by design
// (ephemeral observability, not an audit log).
type History struct {
	mu      sync.RWMutex
	records []DeployRecord
	max     int
}

// NewHistory returns a ring buffer holding up to max records.
func NewHistory(max int) *History {
	if max <= 0 {
		max = 200
	}
	return &History{max: max}
}

// Add appends a record, evicting the oldest if the buffer is full.
func (h *History) Add(r DeployRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	h.records = append(h.records, r)
	if len(h.records) > h.max {
		h.records = h.records[len(h.records)-h.max:]
	}
}

// ListAll returns a copy of all records in insertion order (oldest first).
func (h *History) ListAll() []DeployRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]DeployRecord, len(h.records))
	copy(out, h.records)
	return out
}

// ListForServer returns records for a specific server name, newest first.
func (h *History) ListForServer(server string) []DeployRecord {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []DeployRecord
	for i := len(h.records) - 1; i >= 0; i-- {
		if h.records[i].Server == server {
			out = append(out, h.records[i])
		}
	}
	return out
}
