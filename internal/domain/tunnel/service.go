package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status represents the current tunnel status
type Status struct {
	Active     bool      `json:"active"`
	URL        string    `json:"url"`
	AlphaURL   string    `json:"alpha_url,omitempty"`
	Since      time.Time `json:"since,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
	UptimeSecs int64     `json:"uptime_secs,omitempty"`
}

// HistoryItem represents a recorded tunnel URL with timestamp
type HistoryItem struct {
	URL    string    `json:"url"`
	SeenAt time.Time `json:"seen_at"`
}

// Service manages tunnel status and history
type Service struct {
	mu              sync.RWMutex
	dataDir         string
	cloudflaredContainer string
	lastStatus      Status
	knownURLs       map[string]struct{}
	reFullQuickURL  *regexp.Regexp
	reHostJSON      *regexp.Regexp
	reBareDomain    *regexp.Regexp
	quickTunnelRe   *regexp.Regexp
	statusPublisher  func(Status)
	historyPublisher func([]HistoryItem)
}

var (
	ErrRestartNotConfigured    = errors.New("tunnel: restart not configured")
	ErrDockerSocketUnavailable = errors.New("tunnel: docker socket unavailable")
)

// NewService creates a new tunnel service
func NewService(dataDir, cloudflaredContainer string) *Service {
	return &Service{
		dataDir:              dataDir,
		cloudflaredContainer: cloudflaredContainer,
		knownURLs:            make(map[string]struct{}),
		reFullQuickURL:       regexp.MustCompile(`https?://[a-z0-9-]+\.trycloudflare\.com/?`),
		reHostJSON:           regexp.MustCompile(`"host"\s*:\s*"([a-z0-9-]+\.trycloudflare\.com)"`),
		reBareDomain:         regexp.MustCompile(`(^|[^a-z0-9-])([a-z0-9-]+\.trycloudflare\.com)(/|[^a-z0-9\.-]|$)`),
		quickTunnelRe:        regexp.MustCompile(`^https?://[a-z0-9-]+\.trycloudflare\.com/?$`),
	}
}

// SetPublishers wires callbacks for status and history updates.
func (s *Service) SetPublishers(status func(Status), history func([]HistoryItem)) {
	s.mu.Lock()
	s.statusPublisher = status
	s.historyPublisher = history
	s.mu.Unlock()
}

// GetStatus returns the current tunnel status
func (s *Service) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastStatus
}

// ReadLastURL reads the last tunnel URL from the cloudflared log
func (s *Service) ReadLastURL() string {
	logPath := filepath.Join(s.dataDir, "cloudflared.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	return s.extractLastURLFromLog(data)
}

// extractLastURLFromLog parses cloudflared logs to find the tunnel URL
func (s *Service) extractLastURLFromLog(b []byte) string {
	// Try full URL pattern
	matches := s.reFullQuickURL.FindAll(b, -1)
	if len(matches) > 0 {
		last := string(matches[len(matches)-1])
		last = strings.TrimSuffix(last, "/")
		if s.quickTunnelRe.MatchString(last + "/") {
			return last
		}
	}

	// Try JSON host field
	if m := s.reHostJSON.FindAllSubmatch(b, -1); len(m) > 0 {
		host := string(m[len(m)-1][1])
		return "https://" + host
	}

	// Try bare domain pattern
	if m := s.reBareDomain.FindAllSubmatch(b, -1); len(m) > 0 {
		host := string(m[len(m)-1][2])
		return "https://" + host
	}

	return ""
}

// RefreshStatus updates the tunnel status by checking the log
func (s *Service) RefreshStatus() {
	url := s.ReadLastURL()
	if url != "" {
		s.RecordURL(url)
	}

	s.mu.Lock()
	var st Status
	if url != "" {
		st.Active = true
		st.URL = url
		st.AlphaURL = url + "/alpha"
		if s.lastStatus.URL == url && !s.lastStatus.Since.IsZero() {
			st.Since = s.lastStatus.Since
		} else {
			st.Since = time.Now().UTC()
		}
		st.LastSeen = time.Now().UTC()
		st.UptimeSecs = int64(time.Since(st.Since).Seconds())
	}
	s.lastStatus = st
	s.mu.Unlock()

	if s.statusPublisher != nil {
		s.statusPublisher(st)
	}
}

// RecordURL adds a URL to the history
func (s *Service) RecordURL(raw string) {
	normalized := s.normalizeURL(raw)
	if normalized == "" {
		return
	}

	s.mu.Lock()
	if _, ok := s.knownURLs[normalized]; ok {
		s.mu.Unlock()
		return
	}
	s.knownURLs[normalized] = struct{}{}
	s.mu.Unlock()

	// Append to history file
	histPath := s.historyPath()
	f, err := os.OpenFile(histPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()

	item := HistoryItem{
		URL:   normalized,
		SeenAt: time.Now().UTC(),
	}
	data, _ := json.Marshal(item)
	f.Write(data)
	f.Write([]byte("\n"))

	s.publishHistory()
}

// normalizeURL cleans and validates a tunnel URL
func (s *Service) normalizeURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	// Ensure URL starts with http:// or https://
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}

	// Remove trailing slash
	raw = strings.TrimSuffix(raw, "/")

	// Validate against pattern
	if !s.quickTunnelRe.MatchString(raw + "/") {
		return ""
	}

	return raw
}

// GetHistory returns all recorded tunnel URLs
func (s *Service) GetHistory() []HistoryItem {
	histPath := s.historyPath()
	f, err := os.Open(histPath)
	if err != nil {
		return []HistoryItem{}
	}
	defer f.Close()

	var items []HistoryItem
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var item HistoryItem
		if err := json.Unmarshal(scanner.Bytes(), &item); err == nil {
			items = append(items, item)
		}
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].SeenAt.After(items[j].SeenAt)
	})
	return items
}

// LoadHistory loads known URLs from history file
func (s *Service) LoadHistory() {
	items := s.GetHistory()
	s.mu.Lock()
	for _, item := range items {
		s.knownURLs[item.URL] = struct{}{}
	}
	s.mu.Unlock()
}

// ClearHistory removes the history file
func (s *Service) ClearHistory() error {
	histPath := s.historyPath()
	s.mu.Lock()
	s.knownURLs = make(map[string]struct{})
	s.mu.Unlock()
	
	if err := os.Remove(histPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.publishHistory()
	return nil
}

// IsTunnelHost checks if a hostname is a tunnel host
func (s *Service) IsTunnelHost(host string) bool {
	return strings.HasSuffix(host, ".trycloudflare.com")
}

// historyPath returns the path to the history file
func (s *Service) historyPath() string {
	return filepath.Join(s.dataDir, "tunnel_history.jsonl")
}

// RestartCloudflared attempts to restart the cloudflared container
// RestartCloudflared attempts to restart the configured cloudflared container.
func (s *Service) RestartCloudflared(ctx context.Context, timeoutSec int) error {
	if s.cloudflaredContainer == "" {
		return ErrRestartNotConfigured
	}
	if _, err := os.Stat("/var/run/docker.sock"); err != nil {
		if os.IsNotExist(err) {
			return ErrDockerSocketUnavailable
		}
		return fmt.Errorf("tunnel: docker socket check failed: %w", err)
	}

	tr := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.DialTimeout("unix", "/var/run/docker.sock", 5*time.Second)
		},
	}
	client := &http.Client{Transport: tr}
	u := fmt.Sprintf("http://unix/containers/%s/restart?timeout=%d", s.cloudflaredContainer, timeoutSec)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("docker restart failed: %s", strings.TrimSpace(string(body)))
	}
	return nil
}

func (s *Service) publishHistory() {
	if s.historyPublisher == nil {
		return
	}
	history := s.GetHistory()
	s.historyPublisher(history)
}
