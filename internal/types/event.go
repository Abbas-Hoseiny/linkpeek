package types

import (
	"sync"
	"time"
)

// Event is a single log record for UI/debugging
type Event struct {
	Ts         time.Time         `json:"ts"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query"`
	Class      string            `json:"class,omitempty"`
	RemoteIP   string            `json:"remote_ip"`
	UA         string            `json:"ua"`
	Status     int               `json:"status"`
	DurationMs int64             `json:"duration_ms"`
	RequestID  string            `json:"request_id"`
	Headers    map[string]string `json:"headers,omitempty"`
	Sess       string            `json:"sess,omitempty"`
	SFU        string            `json:"sfu,omitempty"`
	SFM        string            `json:"sfm,omitempty"`
	SFD        string            `json:"sfd,omitempty"`
	SFS        string            `json:"sfs,omitempty"`
	Referer    string            `json:"referer,omitempty"`
	Origin     string            `json:"origin,omitempty"`
}

// EventBuffer is a ring buffer for recent events
type EventBuffer struct {
	mu     sync.RWMutex
	max    int
	buffer []Event
}

// NewEventBuffer creates a new event buffer with the specified maximum size
func NewEventBuffer(max int) *EventBuffer {
	return &EventBuffer{max: max, buffer: make([]Event, 0, max)}
}

// Add appends an event to the buffer, removing the oldest event if at capacity
func (eb *EventBuffer) Add(e Event) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	if len(eb.buffer) == eb.max {
		copy(eb.buffer[0:], eb.buffer[1:])
		eb.buffer[eb.max-1] = e
	} else {
		eb.buffer = append(eb.buffer, e)
	}
}

// Last returns the last n events from the buffer
func (eb *EventBuffer) Last(n int) []Event {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	if n <= 0 || n > len(eb.buffer) {
		n = len(eb.buffer)
	}
	out := make([]Event, n)
	copy(out, eb.buffer[len(eb.buffer)-n:])
	return out
}
