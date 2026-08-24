// Package diag records recent platform events in an in-memory ring buffer
// for the /api/v1/debug/diagnostics endpoint. Events are best-effort and
// lost on restart -- they exist to answer "what has this node been doing?"
// without shell access to the logs.
package diag

import (
	"sync"
	"time"
)

const ringSize = 256

// Event is one recorded occurrence.
type Event struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"` // host_offline, host_online, heartbeat_fail, heartbeat_ok, scan, startup
	Msg    string    `json:"msg"`
	Fields []Field   `json:"fields,omitempty"`
}

// Field is a key/value detail pair (slice keeps order, JSON-friendly).
type Field struct {
	Key   string `json:"k"`
	Value string `json:"v"`
}

var (
	mu      sync.RWMutex
	events  []Event
	started = time.Now()
)

// StartedAt returns the process start time.
func StartedAt() time.Time { return started }

// Record appends an event. Extra key/value pairs are optional and must come
// in pairs (unpaired keys are ignored).
func Record(kind, msg string, kv ...string) {
	var fields []Field
	for i := 0; i+1 < len(kv); i += 2 {
		fields = append(fields, Field{Key: kv[i], Value: kv[i+1]})
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) >= ringSize {
		events = events[1:]
	}
	events = append(events, Event{Time: time.Now().UTC(), Kind: kind, Msg: msg, Fields: fields})
}

// Recent returns the last n events, oldest first.
func Recent(n int) []Event {
	mu.RLock()
	defer mu.RUnlock()
	if n <= 0 || n > ringSize {
		n = ringSize
	}
	if len(events) == 0 {
		return nil
	}
	if n > len(events) {
		n = len(events)
	}
	out := make([]Event, n)
	copy(out, events[len(events)-n:])
	return out
}
