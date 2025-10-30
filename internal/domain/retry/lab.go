package retry

import (
	"sync"
	"time"

	"linkpeek/internal/utils"
)

// Scenario represents a retry testing scenario
type Scenario struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

// Stat tracks statistics for a retry scenario
type Stat struct {
	TotalHits int64
	LastSeen  time.Time
	IPs       map[string]struct{}
}

// StatDTO is the data transfer object for API responses
type StatDTO struct {
	ID        string     `json:"id"`
	TotalHits int64      `json:"total_hits"`
	UniqueIPs int        `json:"unique_ips,omitempty"`
	LastSeen  *time.Time `json:"last_seen,omitempty"`
}

// Lab manages retry testing scenarios and statistics
type Lab struct {
	mu        sync.RWMutex
	scenarios []Scenario
	stats     map[string]*Stat
	publisher func() // Callback to publish realtime updates
}

// NewLab creates a new retry lab with predefined scenarios
func NewLab(publisher func()) *Lab {
	scenarios := []Scenario{
		{
			ID:          "retry-hint",
			Title:       "Retry-After Hint",
			Description: "Returns 503 Service Unavailable with a Retry-After header to observe client backoff logic.",
			Path:        "/retrylab/retry-hint",
		},
		{
			ID:          "drop-after-n",
			Title:       "Drop Connection Mid-Body",
			Description: "Streams a small payload then drops the connection abruptly to simulate flaky upstreams.",
			Path:        "/retrylab/drop-after-n",
		},
		{
			ID:          "wrong-length",
			Title:       "Incorrect Content-Length",
			Description: "Advertises a larger Content-Length than delivered to test truncation handling.",
			Path:        "/retrylab/wrong-length",
		},
	}

	lab := &Lab{
		scenarios: scenarios,
		stats:     make(map[string]*Stat, len(scenarios)),
		publisher: publisher,
	}

	// Initialize stats for all scenarios
	for _, sc := range scenarios {
		lab.stats[sc.ID] = &Stat{IPs: map[string]struct{}{}}
	}

	return lab
}

// RecordHit records a hit for a specific scenario
func (l *Lab) RecordHit(id, ip string) {
	l.mu.Lock()
	stat, ok := l.stats[id]
	if !ok {
		stat = &Stat{IPs: map[string]struct{}{}}
		l.stats[id] = stat
	}
	stat.TotalHits++
	stat.LastSeen = utils.NowUTC()
	if ip != "" {
		if stat.IPs == nil {
			stat.IPs = map[string]struct{}{}
		}
		stat.IPs[ip] = struct{}{}
	}
	publisher := l.publisher
	l.mu.Unlock()

	// Publish stats update if publisher is configured
	if publisher != nil {
		go publisher()
	}
}

// SnapshotStats returns current statistics for all scenarios
func (l *Lab) SnapshotStats() []StatDTO {
	l.mu.RLock()
	defer l.mu.RUnlock()

	items := make([]StatDTO, 0, len(l.scenarios))
	for _, sc := range l.scenarios {
		stat, ok := l.stats[sc.ID]
		if !ok {
			continue
		}
		dto := StatDTO{
			ID:        sc.ID,
			TotalHits: stat.TotalHits,
		}
		if stat.IPs != nil {
			dto.UniqueIPs = len(stat.IPs)
		}
		if !stat.LastSeen.IsZero() {
			ts := stat.LastSeen
			dto.LastSeen = &ts
		}
		items = append(items, dto)
	}
	return items
}

// ListScenarios returns all available retry scenarios
func (l *Lab) ListScenarios() []Scenario {
	l.mu.RLock()
	defer l.mu.RUnlock()
	// Return a copy to prevent external modification
	scenarios := make([]Scenario, len(l.scenarios))
	copy(scenarios, l.scenarios)
	return scenarios
}

// SetPublisher configures the callback invoked after stats update.
func (l *Lab) SetPublisher(publisher func()) {
	l.mu.Lock()
	l.publisher = publisher
	l.mu.Unlock()
}

// Reset clears statistics for the provided scenario IDs. When no IDs are
// supplied the stats for every scenario are reset. The publisher callback is
// triggered so realtime subscribers receive an updated snapshot.
func (l *Lab) Reset(ids ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.stats == nil {
		l.stats = make(map[string]*Stat)
	}

	resetAll := len(ids) == 0
	targets := map[string]struct{}{}
	if !resetAll {
		for _, id := range ids {
			if id == "" {
				continue
			}
			targets[id] = struct{}{}
		}
		if len(targets) == 0 {
			return
		}
	}

	publish := false
	for _, sc := range l.scenarios {
		if !resetAll {
			if _, ok := targets[sc.ID]; !ok {
				continue
			}
		}
		stat, ok := l.stats[sc.ID]
		if !ok {
			continue
		}
		stat.TotalHits = 0
		stat.LastSeen = time.Time{}
		stat.IPs = make(map[string]struct{})
		publish = true
	}

	if publish && l.publisher != nil {
		go l.publisher()
	}
}
