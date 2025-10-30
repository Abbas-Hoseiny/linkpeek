package capture_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"testing"
	"time"

	"linkpeek/internal/capture"
	domaincapture "linkpeek/internal/domain/capture"
	"linkpeek/internal/realtime"
)

type stubConn struct {
	messages chan []byte
}

func newStubConn() *stubConn {
	return &stubConn{messages: make(chan []byte, 8)}
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

type envelope struct {
	Topic string `json:"topic"`
}

func waitForTopics(t *testing.T, conn *stubConn, topics ...string) {
	t.Helper()
	pending := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		pending[topic] = struct{}{}
	}
	timeout := time.After(500 * time.Millisecond)
	for len(pending) > 0 {
		select {
		case msg := <-conn.messages:
			var env envelope
			if err := json.Unmarshal(msg, &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			delete(pending, env.Topic)
		case <-timeout:
			t.Fatalf("timeout waiting for topics: %v", pending)
		}
	}
}

func TestServiceDelegatesToManager(t *testing.T) {
	dir := t.TempDir()
	mgr, err := capture.NewManager(dir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := domaincapture.NewService(mgr)
	if svc == nil {
		t.Fatal("expected service instance")
	}
	if !svc.Available() {
		t.Fatal("expected service to report availability")
	}

	hook, err := svc.CreateHook("demo")
	if err != nil {
		t.Fatalf("create hook: %v", err)
	}
	if hook.ID == "" {
		t.Fatal("expected hook to have id")
	}

	hooks := svc.ListHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected one hook, got %d", len(hooks))
	}

	listed, ok := svc.GetHook(hook.ID)
	if !ok {
		t.Fatalf("expected GetHook(%q) to succeed", hook.ID)
	}
	if listed.ID != hook.ID {
		t.Fatalf("expected hook id %q, got %q", hook.ID, listed.ID)
	}

	req := capture.HookRequest{Method: "GET", Path: "/", Headers: map[string]string{"x": "1"}}
	recorded, err := svc.RecordRequest(hook.ID, req)
	if err != nil {
		t.Fatalf("record request: %v", err)
	}
	if recorded.ID == "" {
		t.Fatal("expected recorded request to have id")
	}

	items := svc.ListRequests(hook.ID, 10)
	if len(items) != 1 {
		t.Fatalf("expected one recorded request, got %d", len(items))
	}

	if err := svc.ClearRequests(hook.ID); err != nil {
		t.Fatalf("clear requests: %v", err)
	}

	buf := bytes.NewBuffer(nil)
	if err := svc.ExportRequests(hook.ID, buf); err != nil {
		t.Fatalf("export requests: %v", err)
	}

	if err := svc.DeleteHook(hook.ID); err != nil {
		t.Fatalf("delete hook: %v", err)
	}
	if _, ok := svc.GetHook(hook.ID); ok {
		t.Fatalf("expected hook %q to be gone", hook.ID)
	}

	if _, err := io.Copy(io.Discard, buf); err != nil { // ensure buffer readable, sanity check
		t.Fatalf("unexpected copy error: %v", err)
	}
}

func TestServicePublishesRealtimeUpdates(t *testing.T) {
	dir := t.TempDir()
	mgr, err := capture.NewManager(dir)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	svc := domaincapture.NewService(mgr)

	hub := realtime.NewHub()
	t.Cleanup(hub.Stop)
	go hub.Start()

	svc.SetRealtimeHub(hub)

	conn := newStubConn()
	client := hub.RegisterClient(conn)
	t.Cleanup(client.Close)
	go client.WritePump()

	if err := client.Subscribe("capture.hooks"); err != nil {
		t.Fatalf("subscribe hooks: %v", err)
	}

	hook, err := svc.CreateHook("demo")
	if err != nil {
		t.Fatalf("create hook: %v", err)
	}
	waitForTopics(t, conn, "capture.hooks")

	if err := client.Subscribe("capture.activity"); err != nil {
		t.Fatalf("subscribe activity: %v", err)
	}
	requestsTopic := fmt.Sprintf("capture.requests::%s", hook.ID)
	if err := client.Subscribe(requestsTopic); err != nil {
		t.Fatalf("subscribe requests: %v", err)
	}

	req := capture.HookRequest{Method: "GET", Path: "/demo"}
	if _, err := svc.RecordRequest(hook.ID, req); err != nil {
		t.Fatalf("record request: %v", err)
	}
	waitForTopics(t, conn, "capture.activity", requestsTopic)

	if err := svc.ClearRequests(hook.ID); err != nil {
		t.Fatalf("clear requests: %v", err)
	}
	waitForTopics(t, conn, "capture.activity", requestsTopic)
}

func TestServiceUnavailable(t *testing.T) {
	var svc *domaincapture.Service
	if svc.Available() {
		t.Fatal("nil service should report unavailable")
	}
	if _, err := svc.CreateHook("demo"); err == nil {
		t.Fatal("expected error when service is nil")
	}
	if _, ok := svc.GetHook("missing"); ok {
		t.Fatal("expected missing hook from nil service")
	}
	if err := svc.DeleteHook("id"); err == nil {
		t.Fatal("expected error when deleting via nil service")
	}
}
