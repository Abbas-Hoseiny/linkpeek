package scanner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"linkpeek/internal/realtime"
	"linkpeek/internal/utils"
)

const (
	jobsFilename    = "scanner_jobs.json"
	resultsFilename = "scanner_results.jsonl"

	minIntervalSeconds = 15
	maxIntervalSeconds = 24 * 60 * 60
	maxBodyBytes       = 512 << 10
	defaultResultCap   = 200
)

var (
	ErrServiceUnavailable = errors.New("scanner service unavailable")
	ErrJobNotFound        = errors.New("scanner job not found")
)

// Logger captures the logging interface required by the service.
type Logger interface {
	Printf(string, ...interface{})
}

// Config describes the dependencies required to build a scanner service.
type Config struct {
	DataDir    string
	HTTPClient *http.Client
	Logger     Logger
	MaxResults int
	Clock      func() time.Time
}

// Job represents a scanner job configuration.
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

// Result captures the outcome of a single scanner execution.
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

// CreateJobRequest defines the input expected when creating a job.
type CreateJobRequest struct {
	Name            string
	Method          string
	URL             string
	IntervalSeconds int
	Body            string
	ContentType     string
	Active          bool
}

// Service coordinates scanner job scheduling, persistence, and realtime updates.
type Service struct {
	mu    sync.RWMutex
	jobs  map[string]*Job
	stops map[string]chan struct{}

	results *resultBuffer

	dataDir    string
	httpClient *http.Client
	logger     Logger
	clock      func() time.Time

	hubMu sync.RWMutex
	hub   *realtime.Hub
}

// NewService constructs a scanner service from the provided configuration.
func NewService(cfg Config) (*Service, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("scanner: data directory is required")
	}

	svc := &Service{
		jobs:    make(map[string]*Job),
		stops:   make(map[string]chan struct{}),
		results: newResultBuffer(nonZero(cfg.MaxResults, defaultResultCap)),
		dataDir: cfg.DataDir,
		logger:  cfg.Logger,
		clock:   cfg.Clock,
	}

	if svc.logger == nil {
		svc.logger = log.Default()
	}
	if svc.clock == nil {
		svc.clock = utils.NowUTC
	}

	if cfg.HTTPClient != nil {
		svc.httpClient = cfg.HTTPClient
	} else {
		svc.httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	if err := svc.ensureDataDir(); err != nil {
		return nil, err
	}

	if err := svc.loadJobs(); err != nil {
		return nil, err
	}
	if err := svc.loadResults(); err != nil {
		svc.logger.Printf("scanner: failed to load results: %v", err)
	}

	return svc, nil
}

// SetRealtimeHub wires the realtime hub used for publishing scanner updates.
func (s *Service) SetRealtimeHub(hub *realtime.Hub) {
	if s == nil {
		return
	}
	s.hubMu.Lock()
	s.hub = hub
	s.hubMu.Unlock()

	if hub != nil {
		s.publishJobs()
		s.publishResults()
	}
}

// ListJobs returns all configured jobs in creation order.
func (s *Service) ListJobs() []Job {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	items := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		items = append(items, *job)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].CreatedAt.Before(items[j].CreatedAt)
	})
	return items
}

// SnapshotJobs produces the current job list for realtime snapshots.
func (s *Service) SnapshotJobs() []Job {
	return s.ListJobs()
}

// SnapshotResults returns the most recent results up to the provided limit.
func (s *Service) SnapshotResults(limit int) []Result {
	if limit <= 0 {
		limit = 100
	}
	return s.Results(limit, "")
}

// Results fetches recent results, optionally filtered by job ID.
func (s *Service) Results(n int, jobID string) []Result {
	if s == nil || s.results == nil {
		return nil
	}
	return s.results.Last(n, jobID)
}

// CreateJob validates and persists a new scanner job.
func (s *Service) CreateJob(req CreateJobRequest) (Job, error) {
	if s == nil {
		return Job{}, ErrServiceUnavailable
	}

	job, err := s.prepareJob(req)
	if err != nil {
		return Job{}, err
	}

	if err := s.ensureDataDir(); err != nil {
		return Job{}, err
	}

	s.mu.Lock()
	for {
		if _, exists := s.jobs[job.ID]; !exists {
			break
		}
		job.ID = newJobID()
	}
	stored := job
	s.jobs[stored.ID] = &stored
	if stored.Active {
		s.startJobLocked(stored.ID)
	}
	if err := s.persistJobsLocked(); err != nil {
		if stored.Active {
			s.stopJobLocked(stored.ID)
		}
		delete(s.jobs, stored.ID)
		s.mu.Unlock()
		return Job{}, err
	}
	created := *s.jobs[stored.ID]
	s.mu.Unlock()

	s.publishJobs()
	return created, nil
}

// DeleteJob removes a job and stops its execution.
func (s *Service) DeleteJob(id string) error {
	if s == nil {
		return ErrServiceUnavailable
	}
	if id == "" {
		return ErrJobNotFound
	}
	if err := s.ensureDataDir(); err != nil {
		return err
	}

	s.mu.Lock()
	if _, ok := s.jobs[id]; !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	s.stopJobLocked(id)
	delete(s.jobs, id)
	if err := s.persistJobsLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	s.publishJobs()
	return nil
}

// ClearResults truncates persisted results and clears the in-memory buffer.
func (s *Service) ClearResults() error {
	if s == nil {
		return ErrServiceUnavailable
	}
	if err := s.ensureDataDir(); err != nil {
		return err
	}

	s.results.Clear()
	path := s.resultsPath()
	if err := os.Truncate(path, 0); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
	}
	s.publishResults()
	return nil
}

// Shutdown stops all running jobs.
func (s *Service) Shutdown() {
	if s == nil {
		return
	}
	s.mu.Lock()
	for id := range s.stops {
		s.stopJobLocked(id)
	}
	s.mu.Unlock()
}

func (s *Service) prepareJob(req CreateJobRequest) (Job, error) {
	now := s.now()
	job := Job{
		ID:              newJobID(),
		Name:            strings.TrimSpace(req.Name),
		Method:          strings.ToUpper(strings.TrimSpace(req.Method)),
		URL:             strings.TrimSpace(req.URL),
		IntervalSeconds: clamp(req.IntervalSeconds, minIntervalSeconds, maxIntervalSeconds),
		Body:            req.Body,
		ContentType:     strings.TrimSpace(req.ContentType),
		CreatedAt:       now,
		UpdatedAt:       now,
		Active:          req.Active,
	}

	if job.URL == "" {
		return Job{}, errors.New("job URL is required")
	}
	if err := utils.ValidateURL(job.URL); err != nil {
		return Job{}, err
	}
	if job.Method == "" {
		job.Method = http.MethodGet
	}
	if job.Name == "" {
		job.Name = job.URL
	}
	if len(job.Body) > maxBodyBytes {
		return Job{}, fmt.Errorf("request body too large (max %d bytes)", maxBodyBytes)
	}
	if job.Body != "" && job.ContentType == "" {
		job.ContentType = "text/plain; charset=utf-8"
	}
	return job, nil
}

func (s *Service) ensureDataDir() error {
	return os.MkdirAll(s.dataDir, 0o755)
}

func (s *Service) jobsPath() string {
	return filepath.Join(s.dataDir, jobsFilename)
}

func (s *Service) resultsPath() string {
	return filepath.Join(s.dataDir, resultsFilename)
}

func (s *Service) loadJobs() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.jobs = make(map[string]*Job)
	s.stops = make(map[string]chan struct{})

	data, err := os.ReadFile(s.jobsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var items []Job
	if err := json.Unmarshal(data, &items); err != nil {
		s.logger.Printf("scanner: jobs decode error: %v", err)
	}

	for i := range items {
		job := items[i]
		if job.ID == "" {
			job.ID = newJobID()
		}
		job.Method = strings.ToUpper(strings.TrimSpace(job.Method))
		if job.Method == "" {
			job.Method = http.MethodGet
		}
		job.IntervalSeconds = clamp(job.IntervalSeconds, minIntervalSeconds, maxIntervalSeconds)
		if job.Name == "" {
			job.Name = job.URL
		}
		stored := job
		s.jobs[stored.ID] = &stored
	}

	for id, job := range s.jobs {
		if job.Active {
			s.startJobLocked(id)
		}
	}
	return nil
}

func (s *Service) loadResults() error {
	path := s.resultsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var res Result
		if err := json.Unmarshal([]byte(line), &res); err == nil {
			s.results.Add(res)
		}
	}
	return nil
}

func (s *Service) persistJobsLocked() error {
	list := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		list = append(list, *job)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.Before(list[j].CreatedAt)
	})
	payload, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.jobsPath() + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.jobsPath())
}

func (s *Service) appendResult(res Result) error {
	path := s.resultsPath()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(res)
}

func (s *Service) startJobLocked(id string) {
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	if _, running := s.stops[id]; running {
		return
	}
	job.Active = true
	job.UpdatedAt = s.now()
	stopCh := make(chan struct{})
	s.stops[id] = stopCh
	go s.runJob(id, stopCh)
}

func (s *Service) stopJobLocked(id string) {
	if job, ok := s.jobs[id]; ok {
		job.Active = false
		job.UpdatedAt = s.now()
	}
	if ch, ok := s.stops[id]; ok {
		close(ch)
		delete(s.stops, id)
	}
}

func (s *Service) runJob(jobID string, stop <-chan struct{}) {
	for {
		job, ok := s.jobSnapshot(jobID)
		if !ok || !job.Active {
			return
		}
		delay := s.nextDelay(job)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-stop:
				timer.Stop()
				return
			}
		} else {
			select {
			case <-stop:
				return
			default:
			}
		}

		job, ok = s.jobSnapshot(jobID)
		if !ok || !job.Active {
			continue
		}
		res := s.performJob(job)
		s.recordResult(res)
		s.applyRunResult(res)

		select {
		case <-stop:
			return
		default:
		}
	}
}

func (s *Service) jobSnapshot(id string) (Job, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, false
	}
	copy := *job
	return copy, true
}

func (s *Service) nextDelay(job Job) time.Duration {
	interval := time.Duration(clamp(job.IntervalSeconds, minIntervalSeconds, maxIntervalSeconds)) * time.Second
	if job.LastRun.IsZero() {
		return 0
	}
	next := job.LastRun.Add(interval)
	now := time.Now()
	if next.After(now) {
		return next.Sub(now)
	}
	return 0
}

func (s *Service) performJob(job Job) Result {
	res := Result{
		ID:      newResultID(),
		JobID:   job.ID,
		JobName: job.Name,
		URL:     job.URL,
		Method:  job.Method,
	}

	start := time.Now()
	timeout := s.httpClient.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var body io.Reader
	if job.Body != "" {
		body = strings.NewReader(job.Body)
	}
	req, err := http.NewRequestWithContext(ctx, job.Method, job.URL, body)
	if err != nil {
		res.Timestamp = s.now()
		res.Error = err.Error()
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}

	if job.Body != "" {
		req.Header.Set("Content-Length", fmt.Sprintf("%d", len(job.Body)))
	}
	if job.ContentType != "" {
		req.Header.Set("Content-Type", job.ContentType)
	}
	req.Header.Set("User-Agent", "LinkPeek-Scanner/1.0")
	req.Header.Set("Accept", "*/*")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		res.Timestamp = s.now()
		res.Error = err.Error()
		res.DurationMs = time.Since(start).Milliseconds()
		return res
	}
	defer resp.Body.Close()

	res.Status = resp.StatusCode
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	text := strings.TrimSpace(string(snippet))
	if len(text) > 240 {
		text = text[:240]
	}
	res.ResponseSnippet = text
	res.Timestamp = s.now()
	res.DurationMs = time.Since(start).Milliseconds()
	return res
}

func (s *Service) recordResult(res Result) {
	s.results.Add(res)
	if err := s.appendResult(res); err != nil {
		s.logger.Printf("scanner: result write error: %v", err)
	}
	s.publishResults()
}

func (s *Service) applyRunResult(res Result) {
	s.mu.Lock()
	job, ok := s.jobs[res.JobID]
	if !ok {
		s.mu.Unlock()
		return
	}
	job.LastRun = res.Timestamp
	if res.Error != "" {
		job.LastStatus = ""
		job.LastError = res.Error
	} else {
		job.LastStatus = fmt.Sprintf("HTTP %d", res.Status)
		job.LastError = ""
	}
	job.UpdatedAt = res.Timestamp
	if err := s.persistJobsLocked(); err != nil {
		s.logger.Printf("scanner: persist error: %v", err)
	}
	s.mu.Unlock()

	s.publishJobs()
}

func (s *Service) publishJobs() {
	hub := s.currentHub()
	if hub == nil {
		return
	}
	snapshot := s.SnapshotJobs()
	go hub.Publish("scanner.jobs", snapshot)
}

func (s *Service) publishResults() {
	hub := s.currentHub()
	if hub == nil {
		return
	}
	snapshot := s.SnapshotResults(100)
	go hub.Publish("scanner.results", snapshot)
}

func (s *Service) currentHub() *realtime.Hub {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	return s.hub
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now().UTC()
}

func nonZero(v, fallback int) int {
	if v <= 0 {
		return fallback
	}
	return v
}

// resultBuffer stores scanner results in a bounded slice preserving chronological order.
type resultBuffer struct {
	mu    sync.RWMutex
	max   int
	items []Result
}

func newResultBuffer(max int) *resultBuffer {
	if max <= 0 {
		max = defaultResultCap
	}
	return &resultBuffer{max: max, items: make([]Result, 0, max)}
}

func (b *resultBuffer) Add(res Result) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) == b.max {
		copy(b.items[0:], b.items[1:])
		b.items[b.max-1] = res
		return
	}
	b.items = append(b.items, res)
}

func (b *resultBuffer) Last(n int, jobID string) []Result {
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
	for i, j := 0, len(results)-1; i < j; i, j = i+1, j-1 {
		results[i], results[j] = results[j], results[i]
	}
	return results
}

func (b *resultBuffer) Clear() {
	b.mu.Lock()
	b.items = make([]Result, 0, cap(b.items))
	b.mu.Unlock()
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func newJobID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err == nil {
		return "job-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("job-%d", time.Now().UnixNano())
}

func newResultID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err == nil {
		return "res-" + hex.EncodeToString(buf)
	}
	return fmt.Sprintf("res-%d", time.Now().UnixNano())
}
