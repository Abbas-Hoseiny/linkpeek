package realtime

import (
	"encoding/json"
	"sync"
	"time"
)

const (
	maxTopicsPerClient  = 16
	maxQueuedMessages   = 64
	heartbeatInterval   = 30 * time.Second
	writeDeadline       = 10 * time.Second
	maxMessageSizeBytes = 256 << 10 // 256 KB
)

// Hub manages WebSocket clients and broadcasts messages to subscribed topics.
type Hub struct {
	mu          sync.RWMutex
	clients     map[*Client]struct{}
	register    chan *Client
	unregister  chan *Client
	broadcast   chan *Message
	snapshots   map[string]SnapshotProvider
	done        chan struct{}
	stopOnce    sync.Once
}

// Message represents a message to be sent to clients.
type Message struct {
	Topic   string
	Payload interface{}
}

// SnapshotProvider returns the current snapshot for a topic.
type SnapshotProvider interface {
	Snapshot() (interface{}, error)
}

// Client represents a WebSocket connection.
type Client struct {
	hub           *Hub
	conn          Conn
	send          chan []byte
	topics        map[string]struct{}
	mu            sync.RWMutex
	lastHeartbeat time.Time
}

// Conn abstracts WebSocket write operations for testing.
type Conn interface {
	WriteMessage(messageType int, data []byte) error
	SetWriteDeadline(t time.Time) error
	Close() error
}

// Envelope wraps messages sent to clients.
type Envelope struct {
	Type    string      `json:"type"`
	Topic   string      `json:"topic,omitempty"`
	Payload interface{} `json:"payload,omitempty"`
	Message string      `json:"message,omitempty"`
}

// NewHub creates and returns a new Hub.
func NewHub() *Hub {
	return &Hub{
		clients:    make(map[*Client]struct{}),
		register:   make(chan *Client, 10),
		unregister: make(chan *Client, 10),
		broadcast:  make(chan *Message, 100),
		snapshots:  make(map[string]SnapshotProvider),
		done:       make(chan struct{}),
	}
}

// Start begins the hub's event loop.
func (h *Hub) Start() {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			h.clients[client] = struct{}{}
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
			}
			h.mu.Unlock()

		case msg := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				if client.isSubscribed(msg.Topic) {
					select {
					case client.send <- h.encodeMessage("event", msg.Topic, msg.Payload):
					default:
						// Client buffer full, close it
						go h.closeClient(client)
					}
				}
			}
			h.mu.RUnlock()

		case <-ticker.C:
			h.checkHeartbeats()

		case <-h.done:
			return
		}
	}
}

// Stop gracefully shuts down the hub.
func (h *Hub) Stop() {
	h.stopOnce.Do(func() {
		close(h.done)
		h.mu.Lock()
		for client := range h.clients {
			close(client.send)
			client.conn.Close()
		}
		h.clients = make(map[*Client]struct{})
		h.mu.Unlock()
	})
}

// Publish sends a message to all clients subscribed to the topic.
func (h *Hub) Publish(topic string, payload interface{}) {
	select {
	case h.broadcast <- &Message{Topic: topic, Payload: payload}:
	case <-h.done:
	default:
		// Drop message if channel is full
	}
}

// RegisterSnapshotProvider registers a snapshot provider for a topic.
func (h *Hub) RegisterSnapshotProvider(topic string, provider SnapshotProvider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.snapshots[topic] = provider
}

// GetSnapshot returns the snapshot for a topic.
func (h *Hub) GetSnapshot(topic string) (interface{}, error) {
	h.mu.RLock()
	provider, ok := h.snapshots[topic]
	h.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	return provider.Snapshot()
}

// RegisterClient registers a new client with the hub.
func (h *Hub) RegisterClient(conn Conn) *Client {
	client := &Client{
		hub:           h,
		conn:          conn,
		send:          make(chan []byte, maxQueuedMessages),
		topics:        make(map[string]struct{}),
		lastHeartbeat: time.Now(),
	}
	h.register <- client
	return client
}

// Subscribe adds a topic subscription for the client.
func (c *Client) Subscribe(topic string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.topics) >= maxTopicsPerClient {
		return ErrTooManyTopics
	}
	c.topics[topic] = struct{}{}
	return nil
}

// Unsubscribe removes a topic subscription for the client.
func (c *Client) Unsubscribe(topic string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.topics, topic)
}

// isSubscribed checks if the client is subscribed to a topic.
func (c *Client) isSubscribed(topic string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.topics[topic]
	return ok
}

// UpdateHeartbeat updates the client's last heartbeat time.
func (c *Client) UpdateHeartbeat() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastHeartbeat = time.Now()
}

// WritePump pumps messages from the hub to the websocket connection.
func (c *Client) WritePump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	for msg := range c.send {
		c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
		if err := c.conn.WriteMessage(1, msg); err != nil {
			return
		}
	}
}

// SendError sends an error message to the client.
func (c *Client) SendError(message string) {
	msg := c.hub.encodeMessage("error", "", nil)
	env := &Envelope{Type: "error", Message: message}
	if data, err := json.Marshal(env); err == nil {
		msg = data
	}
	select {
	case c.send <- msg:
	default:
	}
}

// SendSnapshot sends a snapshot to the client.
func (c *Client) SendSnapshot(topic string, payload interface{}) {
	msg := c.hub.encodeMessage("snapshot", topic, payload)
	select {
	case c.send <- msg:
	default:
	}
}

// Close closes the client connection.
func (c *Client) Close() {
	c.hub.unregister <- c
}

// checkHeartbeats closes clients that haven't sent a heartbeat recently.
func (h *Hub) checkHeartbeats() {
	cutoff := time.Now().Add(-2 * heartbeatInterval)
	h.mu.RLock()
	stale := make([]*Client, 0)
	for client := range h.clients {
		client.mu.RLock()
		if client.lastHeartbeat.Before(cutoff) {
			stale = append(stale, client)
		}
		client.mu.RUnlock()
	}
	h.mu.RUnlock()

	for _, client := range stale {
		h.closeClient(client)
	}
}

// closeClient safely closes a client.
func (h *Hub) closeClient(client *Client) {
	h.mu.Lock()
	if _, ok := h.clients[client]; ok {
		delete(h.clients, client)
		close(client.send)
		client.conn.Close()
	}
	h.mu.Unlock()
}

// encodeMessage encodes a message into JSON.
func (h *Hub) encodeMessage(msgType, topic string, payload interface{}) []byte {
	env := Envelope{
		Type:    msgType,
		Topic:   topic,
		Payload: payload,
	}
	data, _ := json.Marshal(env)
	return data
}

// Errors
var (
	ErrTooManyTopics = &hubError{"too many topic subscriptions"}
)

type hubError struct {
	msg string
}

func (e *hubError) Error() string {
	return e.msg
}
