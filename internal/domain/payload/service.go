package payload

import (
	"errors"
	"os"
	"sync"

	"linkpeek/internal/realtime"
	"linkpeek/internal/types"
)

var errServiceUnavailable = errors.New("payload service unavailable")

// Service wraps the payload manager and coordinates realtime updates.
type Service struct {
	manager *Manager
	hubMu   sync.RWMutex
	hub     *realtime.Hub
}

// NewService builds a payload service for the provided manager.
func NewService(manager *Manager) *Service {
	s := &Service{manager: manager}
	if manager != nil {
		manager.SetPublisher(s.PublishList)
	}
	return s
}

// SetRealtimeHub wires the realtime hub used for broadcasting payload updates.
func (s *Service) SetRealtimeHub(hub *realtime.Hub) {
	s.hubMu.Lock()
	s.hub = hub
	s.hubMu.Unlock()
	if s.manager != nil {
		s.manager.SetPublisher(s.PublishList)
	}
}

func (s *Service) currentHub() *realtime.Hub {
	s.hubMu.RLock()
	defer s.hubMu.RUnlock()
	return s.hub
}

// PublishList pushes the latest payload list to listeners if a hub is configured.
func (s *Service) PublishList() {
	hub := s.currentHub()
	if hub == nil {
		return
	}
	hub.Publish("payload.list", s.Snapshot())
}

// Snapshot returns the current payload list for realtime snapshots.
func (s *Service) Snapshot() []types.PayloadListItem {
	if s == nil || s.manager == nil {
		return []types.PayloadListItem{}
	}
	return s.manager.Snapshot()
}

// MaxUploadBytes exposes the maximum allowed upload size.
func (s *Service) MaxUploadBytes() int64 {
	if s == nil || s.manager == nil {
		return 0
	}
	return s.manager.MaxUploadBytes()
}

// Create persists a new payload.
func (s *Service) Create(data []byte, originalFilename, name, category, mimeType string) (*types.PayloadMeta, error) {
	if s == nil || s.manager == nil {
		return nil, errServiceUnavailable
	}
	return s.manager.Create(data, originalFilename, name, category, mimeType)
}

// Get retrieves payload metadata by ID.
func (s *Service) Get(id string) (*types.PayloadMeta, bool) {
	if s == nil || s.manager == nil {
		return nil, false
	}
	return s.manager.Get(id)
}

// List returns metadata for all payloads.
func (s *Service) List() []types.PayloadMeta {
	if s == nil || s.manager == nil {
		return []types.PayloadMeta{}
	}
	return s.manager.List()
}

// Delete removes a payload by ID.
func (s *Service) Delete(id string) error {
	if s == nil || s.manager == nil {
		return errServiceUnavailable
	}
	return s.manager.Delete(id)
}

// GetVariants lists the available variants for a payload.
func (s *Service) GetVariants(id string) []types.PayloadVariant {
	if s == nil || s.manager == nil {
		return []types.PayloadVariant{}
	}
	return s.manager.GetVariants(id)
}

// GetFilePath resolves the filepath for a payload.
func (s *Service) GetFilePath(id string) (string, error) {
	if s == nil || s.manager == nil {
		return "", errServiceUnavailable
	}
	return s.manager.GetFilePath(id)
}

// OpenFile opens the payload file for reading.
func (s *Service) OpenFile(id string) (*os.File, os.FileInfo, error) {
	if s == nil || s.manager == nil {
		return nil, nil, errServiceUnavailable
	}
	return s.manager.OpenFile(id)
}
