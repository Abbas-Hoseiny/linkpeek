//go:build legacy
// +build legacy

package scanner

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Job represents a scanner job configuration
type Job struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Method          string    `json:"method"`
	URL             string    `json:"url"`
	IntervalSeconds int       `json:"interval_seconds"`
	Body            string    `json:"body,omitempty"`
	ContentType     string    `json:"content_type,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	LastRun         time.Time `json:"last_run,omitempty"`
	LastStatus      string    `json:"last_status,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	Active          bool      `json:"active"`
}

// Result represents the result of a scanner job execution
type Result struct {
	ID              string    `json:"id"`
	JobID           string    `json:"job_id"`
	JobName         string    `json:"job_name"`
	URL             string    `json:"url"`
	Method          string    `json:"method"`
	Status          int       `json:"status"`
	Error           string    `json:"error,omitempty"`
	DurationMs      int64     `json:"duration_ms"`
	Timestamp       time.Time `json:"ts"`
	ResponseSnippet string    `json:"response_snippet,omitempty"`
}

// ResultBuffer is a ring buffer for scanner results
type ResultBuffer struct {
	mu    sync.RWMutex
	max   int
	items []Result
}

// NewResultBuffer creates a new result buffer
func NewResultBuffer(max int) *ResultBuffer {
	if max <= 0 {
		max = 50
	}
	return &ResultBuffer{max: max, items: make([]Result, 0, max)}
}

// Add adds a result to the buffer
func (b *ResultBuffer) Add(res Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == b.max {
		copy(b.items[0:], b.items[1:])
		b.items[b.max-1] = res
		return
	}
	b.items = append(b.items, res)
}

// Last returns the last n results, optionally filtered by job ID
func (b *ResultBuffer) Last(n int, jobID string) []Result {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if n <= 0 || n > len(b.items) {
		n = len(b.items)
	}
	results := make([]Result, 0, n)
	for i := len(b.items) - 1; i >= 0 && len(results) < n; i-- {
		res := b.items[i]
		if jobID != "" && res.JobID != jobID {
			continue
		}
		results = append(results, res)
	}
	// reverse to chronological order
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

// Manager manages scanner jobs and their execution
type Manager struct {
	mu            sync.RWMutex
	jobs          map[string]*Job
	stops         map[string]chan struct{}
	results       *ResultBuffer
	httpClient    *http.Client
	resultPublisher func() // Callback to publish result updates
	jobPublisher    func() // Callback to publish job updates
}

// NewManager creates a new scanner manager
func NewManager(httpClient *http.Client, resultPublisher, jobPublisher func()) *Manager {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Manager{
		jobs:            make(map[string]*Job),
		stops:           make(map[string]chan struct{}),
		results:         NewResultBuffer(200),
		httpClient:      httpClient,
		resultPublisher: resultPublisher,
		jobPublisher:    jobPublisher,
	}
}

// CreateJob creates a new scanner job
func (m *Manager) CreateJob(name, method, url string, intervalSeconds int, body, contentType string) (*Job, error) {
	if name == "" {
		return nil, fmt.Errorf("job name is required")
	}
	if url == "" {
		return nil, fmt.Errorf("job URL is required")
	}
	if method == "" {
		method = "GET"
	}
	if intervalSeconds <= 0 {
		intervalSeconds = 60
	}

	job := &Job{
		ID:              newJobID(),
		Name:            name,
		Method:          strings.ToUpper(method),
		URL:             url,
		IntervalSeconds: intervalSeconds,
		Body:            body,
		ContentType:     contentType,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		Active:          false,
	}

	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	// Start the job
	m.StartJob(job.ID)

	if m.jobPublisher != nil {
		go m.jobPublisher()
	}

	return job, nil
}

// GetJob retrieves a job by ID
func (m *Manager) GetJob(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	return job, ok
}

// ListJobs returns all jobs
func (m *Manager) ListJobs() []Job {
	m.mu.RLock()
	defer m.mu.RUnlock()
	jobs := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, *job)
	}
	return jobs
}

// DeleteJob stops and removes a job
func (m *Manager) DeleteJob(id string) error {
	m.mu.Lock()
	if _, ok := m.jobs[id]; !ok {
		m.mu.Unlock()
		return fmt.Errorf("job not found")
	}
	delete(m.jobs, id)
	m.mu.Unlock()

	m.StopJob(id)

	if m.jobPublisher != nil {
		go m.jobPublisher()
	}

	return nil
}

// StartJob starts a scanner job
func (m *Manager) StartJob(id string) error {
	m.mu.Lock()
	job, ok := m.jobs[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("job not found")
	}
	if job.Active {
		m.mu.Unlock()
		return nil // Already running
	}
	job.Active = true
	job.UpdatedAt = time.Now().UTC()

	stopCh := make(chan struct{})
	m.stops[id] = stopCh
	m.mu.Unlock()

	// Start job execution goroutine
	go m.runJob(job, stopCh)

	if m.jobPublisher != nil {
		go m.jobPublisher()
	}

	return nil
}

// StopJob stops a running scanner job
func (m *Manager) StopJob(id string) {
	m.mu.Lock()
	if job, ok := m.jobs[id]; ok {
		job.Active = false
		job.UpdatedAt = time.Now().UTC()
	}
	if stopCh, ok := m.stops[id]; ok {
		close(stopCh)
		delete(m.stops, id)
	}
	m.mu.Unlock()

	if m.jobPublisher != nil {
		go m.jobPublisher()
	}
}

// runJob executes a scanner job periodically
func (m *Manager) runJob(job *Job, stopCh chan struct{}) {
	ticker := time.NewTicker(time.Duration(job.IntervalSeconds) * time.Second)
	defer ticker.Stop()

	// Run immediately
	m.executeJob(job)

	for {
		select {
		case <-ticker.C:
			m.executeJob(job)
		case <-stopCh:
			return
		}
	}
}

// executeJob performs a single execution of a scanner job
func (m *Manager) executeJob(job *Job) {
	start := time.Now()

	var body io.Reader
	if job.Body != "" {
		body = strings.NewReader(job.Body)
	}

	req, err := http.NewRequest(job.Method, job.URL, body)
	if err != nil {
		m.recordResult(job, 0, fmt.Sprintf("request creation failed: %v", err), 0, "")
		return
	}

	if job.ContentType != "" {
		req.Header.Set("Content-Type", job.ContentType)
	}

	resp, err := m.httpClient.Do(req)
	duration := time.Since(start)

	if err != nil {
		m.recordResult(job, 0, fmt.Sprintf("request failed: %v", err), duration.Milliseconds(), "")
		return
	}
	defer resp.Body.Close()

	// Read response snippet (first 500 bytes)
	snippet := make([]byte, 500)
	n, _ := resp.Body.Read(snippet)
	snippetStr := string(snippet[:n])

	m.recordResult(job, resp.StatusCode, "", duration.Milliseconds(), snippetStr)
}

// recordResult records the result of a job execution
func (m *Manager) recordResult(job *Job, status int, errMsg string, durationMs int64, snippet string) {
	result := Result{
		ID:              newResultID(),
		JobID:           job.ID,
		JobName:         job.Name,
		URL:             job.URL,
		Method:          job.Method,
		Status:          status,
		Error:           errMsg,
		DurationMs:      durationMs,
		Timestamp:       time.Now().UTC(),
		ResponseSnippet: snippet,
	}

	m.results.Add(result)

	// Update job status
	m.mu.Lock()
	if j, ok := m.jobs[job.ID]; ok {
		j.LastRun = result.Timestamp
		if errMsg != "" {
			j.LastStatus = "error"
			j.LastError = errMsg
		} else {
			j.LastStatus = fmt.Sprintf("%d", status)
			j.LastError = ""
		}
		j.UpdatedAt = time.Now().UTC()
	}
	m.mu.Unlock()

	if m.resultPublisher != nil {
		go m.resultPublisher()
	}
	if m.jobPublisher != nil {
		go m.jobPublisher()
	}
}

// GetResults returns recent results
func (m *Manager) GetResults(n int, jobID string) []Result {
	return m.results.Last(n, jobID)
}

// Shutdown stops all running jobs
func (m *Manager) Shutdown() {
	m.mu.Lock()
	jobIDs := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		jobIDs = append(jobIDs, id)
	}
	m.mu.Unlock()

	for _, id := range jobIDs {
		m.StopJob(id)
	}
}

// newJobID generates a unique job ID
func newJobID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "job-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("job-%d", time.Now().UnixNano())
}

// newResultID generates a unique result ID
func newResultID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "res-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("res-%d", time.Now().UnixNano())
}
