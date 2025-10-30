package payload

import (
	"encoding/json"
	"testing"
	"time"

	"linkpeek/internal/realtime"
)

type stubConn struct {
	messages chan []byte
}

func newStubConn() *stubConn {
	return &stubConn{messages: make(chan []byte, 4)}
}

func (s *stubConn) WriteMessage(_ int, data []byte) error {
	copyData := append([]byte(nil), data...)
	select {
	case s.messages <- copyData:
	default:
	}
	return nil
}

func (s *stubConn) SetWriteDeadline(time.Time) error { return nil }

func (s *stubConn) Close() error { return nil }

func (s *stubConn) nextMessage(t *testing.T) []byte {
	t.Helper()
	select {
	case msg := <-s.messages:
		return msg
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("timeout waiting for message")
		return nil
	}
}

type envelope struct {
	Topic string `json:"topic"`
}

func waitForTopic(t *testing.T, conn *stubConn, topic string) []byte {
	t.Helper()
	msg := conn.nextMessage(t)
	var env envelope
	if err := json.Unmarshal(msg, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Topic != topic {
		t.Fatalf("expected topic %s, got %s", topic, env.Topic)
	}
	return msg
}

func TestServicePublishListBroadcasts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mgr, err := NewManager(dir, 10<<20, nil)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := NewService(mgr)

	hub := realtime.NewHub()
	defer hub.Stop()
	go hub.Start()

	svc.SetRealtimeHub(hub)

	conn := newStubConn()
	client := hub.RegisterClient(conn)
	defer client.Close()
	go client.WritePump()

	if err := client.Subscribe("payload.list"); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	svc.PublishList()
	waitForTopic(t, conn, "payload.list")

	if _, err := svc.Create([]byte("hello"), "test.txt", "Test", "", "text/plain"); err != nil {
		t.Fatalf("create payload: %v", err)
	}

	waitForTopic(t, conn, "payload.list")
}
