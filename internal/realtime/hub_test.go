package realtime

import (
	"sync"
	"testing"
	"time"
)

// mockConn implements Conn interface for testing.
type mockConn struct {
	mu       sync.Mutex
	messages [][]byte
	closed   bool
}

func (m *mockConn) WriteMessage(messageType int, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return &hubError{"connection closed"}
	}
	m.messages = append(m.messages, data)
	return nil
}

func (m *mockConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *mockConn) getMessages() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([][]byte{}, m.messages...)
}

func (m *mockConn) isClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}

// mockSnapshotProvider implements SnapshotProvider for testing.
type mockSnapshotProvider struct {
	data interface{}
}

func (m *mockSnapshotProvider) Snapshot() (interface{}, error) {
	return m.data, nil
}

func TestHubLifecycle(t *testing.T) {
	hub := NewHub()
	
	// Start hub in background
	done := make(chan struct{})
	go func() {
		hub.Start()
		close(done)
	}()

	// Give hub time to start
	time.Sleep(10 * time.Millisecond)

	// Stop hub
	hub.Stop()

	// Wait for hub to finish
	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("hub did not stop within timeout")
	}
}

func TestSubscriptionLifecycle(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	conn := &mockConn{}
	client := hub.RegisterClient(conn)

	// Subscribe to topics
	if err := client.Subscribe("test.topic1"); err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}
	if err := client.Subscribe("test.topic2"); err != nil {
		t.Fatalf("failed to subscribe: %v", err)
	}

	// Verify subscriptions
	if !client.isSubscribed("test.topic1") {
		t.Error("client should be subscribed to test.topic1")
	}
	if !client.isSubscribed("test.topic2") {
		t.Error("client should be subscribed to test.topic2")
	}
	if client.isSubscribed("test.topic3") {
		t.Error("client should not be subscribed to test.topic3")
	}

	// Unsubscribe
	client.Unsubscribe("test.topic1")
	if client.isSubscribed("test.topic1") {
		t.Error("client should not be subscribed to test.topic1 after unsubscribe")
	}
	if !client.isSubscribed("test.topic2") {
		t.Error("client should still be subscribed to test.topic2")
	}

	// Close client
	client.Close()
	time.Sleep(50 * time.Millisecond)
}

func TestPublishToMultipleSubscribers(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	// Create multiple clients
	conn1 := &mockConn{}
	client1 := hub.RegisterClient(conn1)
	client1.Subscribe("test.broadcast")

	conn2 := &mockConn{}
	client2 := hub.RegisterClient(conn2)
	client2.Subscribe("test.broadcast")

	conn3 := &mockConn{}
	client3 := hub.RegisterClient(conn3)
	client3.Subscribe("test.other")

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	// Start write pumps
	go client1.WritePump()
	go client2.WritePump()
	go client3.WritePump()

	// Publish message
	testPayload := map[string]string{"test": "data"}
	hub.Publish("test.broadcast", testPayload)

	// Wait for messages to be processed
	time.Sleep(150 * time.Millisecond)

	// Check that subscribed clients received the message
	msgs1 := conn1.getMessages()
	msgs2 := conn2.getMessages()
	msgs3 := conn3.getMessages()

	if len(msgs1) != 1 {
		t.Errorf("client1 should receive 1 message, got %d", len(msgs1))
	}
	if len(msgs2) != 1 {
		t.Errorf("client2 should receive 1 message, got %d", len(msgs2))
	}
	if len(msgs3) != 0 {
		t.Errorf("client3 should receive 0 messages, got %d", len(msgs3))
	}

	client1.Close()
	client2.Close()
	client3.Close()
}

func TestMaxTopicsPerClient(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	conn := &mockConn{}
	client := hub.RegisterClient(conn)

	// Subscribe to max number of topics
	for i := 0; i < maxTopicsPerClient; i++ {
		topic := "test.topic" + string(rune(i))
		if err := client.Subscribe(topic); err != nil {
			t.Fatalf("failed to subscribe to topic %d: %v", i, err)
		}
	}

	// Try to subscribe to one more
	err := client.Subscribe("test.overflow")
	if err != ErrTooManyTopics {
		t.Errorf("expected ErrTooManyTopics, got %v", err)
	}

	client.Close()
}

func TestHeartbeatTimeout(t *testing.T) {
	// Create hub with shorter heartbeat interval for testing
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	conn := &mockConn{}
	client := hub.RegisterClient(conn)
	client.Subscribe("test.topic")

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	// Set last heartbeat to old time
	client.mu.Lock()
	client.lastHeartbeat = time.Now().Add(-3 * heartbeatInterval)
	client.mu.Unlock()

	// Trigger heartbeat check
	hub.checkHeartbeats()

	// Wait a bit for cleanup
	time.Sleep(100 * time.Millisecond)

	// Verify client was closed
	if !conn.isClosed() {
		t.Error("stale client should be closed")
	}
}

func TestSnapshotProvider(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	// Register snapshot provider
	testData := map[string]string{"key": "value"}
	provider := &mockSnapshotProvider{data: testData}
	hub.RegisterSnapshotProvider("test.snapshot", provider)

	// Get snapshot
	snapshot, err := hub.GetSnapshot("test.snapshot")
	if err != nil {
		t.Fatalf("failed to get snapshot: %v", err)
	}

	data, ok := snapshot.(map[string]string)
	if !ok {
		t.Fatal("snapshot data type mismatch")
	}
	if data["key"] != "value" {
		t.Errorf("expected value 'value', got '%s'", data["key"])
	}

	// Get non-existent snapshot
	snapshot, err = hub.GetSnapshot("non.existent")
	if err != nil {
		t.Errorf("expected nil error for non-existent snapshot, got %v", err)
	}
	if snapshot != nil {
		t.Error("expected nil snapshot for non-existent topic")
	}
}

func TestClientWritePump(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	conn := &mockConn{}
	client := hub.RegisterClient(conn)
	client.Subscribe("test.topic")

	// Wait for registration
	time.Sleep(50 * time.Millisecond)

	// Start write pump
	done := make(chan struct{})
	go func() {
		client.WritePump()
		close(done)
	}()

	// Send some messages
	hub.Publish("test.topic", map[string]string{"msg": "1"})
	hub.Publish("test.topic", map[string]string{"msg": "2"})

	// Wait for messages
	time.Sleep(150 * time.Millisecond)

	// Check messages received
	msgs := conn.getMessages()
	if len(msgs) != 2 {
		t.Errorf("expected 2 messages, got %d", len(msgs))
	}

	// Close client
	client.Close()

	// Wait for write pump to finish
	select {
	case <-done:
		// Success
	case <-time.After(time.Second):
		t.Fatal("write pump did not finish within timeout")
	}
}

func TestMessageBufferOverflow(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	conn := &mockConn{}
	client := hub.RegisterClient(conn)
	client.Subscribe("test.overflow")

	// Don't start write pump, so messages accumulate

	// Send more messages than buffer can hold
	for i := 0; i < maxQueuedMessages+10; i++ {
		hub.Publish("test.overflow", map[string]interface{}{"count": i})
	}

	// Wait for hub to process
	time.Sleep(200 * time.Millisecond)

	// Client should be closed due to buffer overflow
	hub.mu.RLock()
	_, exists := hub.clients[client]
	hub.mu.RUnlock()

	if exists {
		t.Error("client should be closed after buffer overflow")
	}
}

func TestConcurrentPublish(t *testing.T) {
	hub := NewHub()
	go hub.Start()
	defer hub.Stop()

	// Create clients
	conns := make([]*mockConn, 5)
	clients := make([]*Client, 5)
	for i := 0; i < 5; i++ {
		conns[i] = &mockConn{}
		clients[i] = hub.RegisterClient(conns[i])
		clients[i].Subscribe("test.concurrent")
		go clients[i].WritePump()
	}

	// Wait for all registrations
	time.Sleep(100 * time.Millisecond)

	// Publish messages concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hub.Publish("test.concurrent", map[string]int{"msg": idx})
		}(i)
	}

	wg.Wait()
	time.Sleep(300 * time.Millisecond)

	// Each client should receive all 10 messages
	for i, conn := range conns {
		msgs := conn.getMessages()
		if len(msgs) != 10 {
			t.Errorf("client %d should receive 10 messages, got %d", i, len(msgs))
		}
	}

	for _, client := range clients {
		client.Close()
	}
}
