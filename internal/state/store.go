// Package state persists mc-operator runtime state across restarts.
// The state file is local-only and must not be committed to Git.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/devvelvet/mc-operator/pkg/mctypes"
)

// ServerState is mc-operator's view of a single server's runtime state.
type ServerState struct {
	Name           string              `json:"name"`
	CurrentImage   string              `json:"currentImage"`
	PreviousImage  string              `json:"previousImage,omitempty"`
	LastCommit     string              `json:"lastCommit,omitempty"`
	LastDeployedAt time.Time           `json:"lastDeployedAt,omitempty"`
	Port           int                 `json:"port,omitempty"`
	Sync           mctypes.SyncStatus  `json:"sync"`
	Health         mctypes.HealthStatus `json:"health"`
	Message        string              `json:"message,omitempty"`
}

// State is the root document persisted to state.json.
type State struct {
	Version      int                    `json:"version"`
	UpdatedAt    time.Time              `json:"updatedAt"`
	Servers      map[string]ServerState `json:"servers"`
	AllocatedPorts map[string]int       `json:"allocatedPorts"`
}

// Store is a thread-safe on-disk state store.
type Store struct {
	mu   sync.RWMutex
	path string
	s    State
}

// Open loads the state file at path, creating an empty one if it doesn't exist.
func Open(path string) (*Store, error) {
	st := &Store{
		path: path,
		s: State{
			Version:        1,
			Servers:        map[string]ServerState{},
			AllocatedPorts: map[string]int{},
		},
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, err
			}
			return st, st.save()
		}
		return nil, fmt.Errorf("read state %s: %w", path, err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &st.s); err != nil {
			return nil, fmt.Errorf("parse state: %w", err)
		}
		if st.s.Servers == nil {
			st.s.Servers = map[string]ServerState{}
		}
		if st.s.AllocatedPorts == nil {
			st.s.AllocatedPorts = map[string]int{}
		}
	}
	return st, nil
}

// Snapshot returns a copy of all server states.
func (s *Store) Snapshot() []ServerState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]ServerState, 0, len(s.s.Servers))
	for _, v := range s.s.Servers {
		out = append(out, v)
	}
	return out
}

// Get returns the state for one server.
func (s *Store) Get(name string) (ServerState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.s.Servers[name]
	return v, ok
}

// Upsert writes a server state and persists to disk.
func (s *Store) Upsert(state ServerState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.s.Servers[state.Name] = state
	s.s.UpdatedAt = time.Now().UTC()
	return s.save()
}

// Delete removes a server from state.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.s.Servers, name)
	delete(s.s.AllocatedPorts, name)
	s.s.UpdatedAt = time.Now().UTC()
	return s.save()
}

// AllocatePort returns the port assigned to name, allocating a new one if needed.
// Search starts at startPort and increments until a free port is found.
func (s *Store) AllocatePort(name string, startPort int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.s.AllocatedPorts[name]; ok {
		return p
	}
	used := map[int]bool{}
	for _, p := range s.s.AllocatedPorts {
		used[p] = true
	}
	p := startPort
	for used[p] {
		p++
	}
	s.s.AllocatedPorts[name] = p
	_ = s.save()
	return p
}

// save writes the state to disk atomically.
func (s *Store) save() error {
	b, err := json.MarshalIndent(s.s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
