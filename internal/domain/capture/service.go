package capture

import (
	"errors"
	"io"
	"sync"

	"linkpeek/internal/capture"
	"linkpeek/internal/realtime"
)

const (
	defaultActivityLimit = 50
	defaultRequestsLimit = 100
)

// Service exposes capture hook operations to HTTP handlers without relying on globals.
type Service struct {
	manager *capture.Manager
	hubMu   sync.RWMutex
	hub     *realtime.Hub
}

// NewService wraps the provided capture manager.
func NewService(manager *capture.Manager) *Service {
	return &Service{manager: manager}
}

// Manager returns the underlying capture manager. Primarily for wiring legacy helpers.
func (s *Service) Manager() *capture.Manager {
	if s == nil {
		return nil
	}
	return s.manager
}

// Available reports whether capture is enabled.
func (s *Service) Available() bool {
	return s != nil && s.manager != nil
}

// SetRealtimeHub wires the realtime hub used for broadcasting capture updates.
func (s *Service) SetRealtimeHub(hub *realtime.Hub) {
	if s == nil {
		return
	}
	s.hubMu.Lock()
	s.hub = hub
	s.hubMu.Unlock()
}

func (s *Service) currentHub() *realtime.Hub {
	if s == nil {
		return nil
	}
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	return s.hub
}

// PublishHooks pushes the latest hook list to realtime subscribers.
func (s *Service) PublishHooks() {
	hub := s.currentHub()
	if hub == nil || !s.Available() {
		return
	}
	hooks := s.ListHooks()
	if hooks == nil {
		hooks = []capture.Hook{}
	}
	hub.Publish("capture.hooks", hooks)
}

// PublishActivity broadcasts the most recent capture requests.
func (s *Service) PublishActivity(limit int) {
	hub := s.currentHub()
	if hub == nil || !s.Available() {
		return
	}
	activity := s.RecentRequests(limit)
	if activity == nil {
		activity = []capture.HookRequest{}
	}
	hub.Publish("capture.activity", activity)
}

// PublishRequests broadcasts the latest requests for a specific hook.
func (s *Service) PublishRequests(hookID string, limit int) {
	hub := s.currentHub()
	if hub == nil || !s.Available() {
		return
	}
	if hookID == "" {
		return
	}
	requests := s.ListRequests(hookID, limit)
	if requests == nil {
		requests = []capture.HookRequest{}
	}
	hub.Publish("capture.requests::"+hookID, requests)
}

// ListHooks returns all configured capture hooks.
func (s *Service) ListHooks() []capture.Hook {
	if !s.Available() {
		return nil
	}
	return s.manager.ListHooks()
}

// CreateHook registers a new capture hook with the provided label.
func (s *Service) CreateHook(label string) (capture.Hook, error) {
	if !s.Available() {
		return capture.Hook{}, errors.New("capture service unavailable")
	}
	hook, err := s.manager.CreateHook(label)
	if err == nil {
		s.PublishHooks()
	}
	return hook, err
}

// DeleteHook removes the hook identified by id.
func (s *Service) DeleteHook(id string) error {
	if !s.Available() {
		return errors.New("capture service unavailable")
	}
	if err := s.manager.DeleteHook(id); err != nil {
		return err
	}
	s.PublishHooks()
	return nil
}

// ClearRequests removes stored requests for the given hook.
func (s *Service) ClearRequests(id string) error {
	if !s.Available() {
		return errors.New("capture service unavailable")
	}
	if err := s.manager.ClearRequests(id); err != nil {
		return err
	}
	s.PublishActivity(defaultActivityLimit)
	s.PublishRequests(id, defaultRequestsLimit)
	return nil
}

// ListRequests returns recent requests for the hook.
func (s *Service) ListRequests(id string, limit int) []capture.HookRequest {
	if !s.Available() {
		return nil
	}
	return s.manager.ListRequests(id, limit)
}

// RecentRequests returns the most recent capture requests across hooks.
func (s *Service) RecentRequests(limit int) []capture.HookRequest {
	if !s.Available() {
		return nil
	}
	return s.manager.RecentRequests(limit)
}

// ExportRequests streams hook requests to the writer.
func (s *Service) ExportRequests(id string, w io.Writer) error {
	if !s.Available() {
		return errors.New("capture service unavailable")
	}
	if w == nil {
		return errors.New("nil writer")
	}
	return s.manager.ExportRequests(id, w)
}

// GetHook fetches a hook by id.
func (s *Service) GetHook(id string) (capture.Hook, bool) {
	if !s.Available() {
		return capture.Hook{}, false
	}
	return s.manager.GetHook(id)
}

// HookByToken resolves a hook token to the underlying hook.
func (s *Service) HookByToken(token string) (capture.Hook, bool) {
	if !s.Available() {
		return capture.Hook{}, false
	}
	return s.manager.HookByToken(token)
}

// RecordRequest stores a capture request for the hook.
func (s *Service) RecordRequest(id string, req capture.HookRequest) (capture.HookRequest, error) {
	if !s.Available() {
		return capture.HookRequest{}, errors.New("capture service unavailable")
	}
	recorded, err := s.manager.RecordRequest(id, req)
	if err == nil {
		s.PublishActivity(defaultActivityLimit)
		s.PublishRequests(id, defaultRequestsLimit)
	}
	return recorded, err
}
