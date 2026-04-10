package api

import (
	"encoding/json"
	"sync"
	"time"
)

// Event is a structured message broadcast to SSE subscribers.
type Event struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Server    string          `json:"server,omitempty"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// Broker is a fan-out pub/sub broker for Event values.
// Each subscriber owns a buffered channel; slow consumers are dropped.
type Broker struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewBroker returns an empty broker.
func NewBroker() *Broker {
	return &Broker{subs: map[chan Event]struct{}{}}
}

// Subscribe returns a new channel that receives every future event, plus an
// unsubscribe function the caller must invoke to release resources.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	unsub := func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, unsub
}

// Publish broadcasts an event to all subscribers. Non-blocking: slow
// subscribers that can't keep up will miss events.
func (b *Broker) Publish(ev Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// PublishMessage is a convenience for simple status messages.
func (b *Broker) PublishMessage(evType, server, msg string) {
	b.Publish(Event{Type: evType, Server: server, Message: msg})
}
