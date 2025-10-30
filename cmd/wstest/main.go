package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	host := flag.String("host", "localhost:9009", "server host")
	cookie := flag.String("cookie", "", "session cookie value")
	topic := flag.String("topic", "log.event", "topic to subscribe")
	once := flag.Bool("once", false, "exit after first message")
	flag.Parse()

	if *cookie == "" {
		log.Fatal("session cookie required")
	}

	u := url.URL{Scheme: "ws", Host: *host, Path: "/api/realtime"}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
	}
	header := http.Header{}
	header.Set("Cookie", fmt.Sprintf("lp_session=%s", *cookie))

	conn, resp, err := dialer.Dial(u.String(), header)
	if err != nil {
		if resp != nil {
			log.Printf("dial error status=%d", resp.StatusCode)
		}
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	log.Println("connected")

	sub := map[string]any{
		"action": "subscribe",
		"topics": []string{*topic},
	}
	if err := conn.WriteJSON(sub); err != nil {
		log.Fatalf("subscribe: %v", err)
	}
	log.Printf("subscribed to %s", *topic)

	snap := map[string]any{
		"action": "snapshot",
		"topic":  *topic,
	}
	if err := conn.WriteJSON(snap); err != nil {
		log.Printf("snapshot request failed: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			log.Fatalf("read: %v", err)
		}
		b, err := json.MarshalIndent(msg, "", "  ")
		if err != nil {
			log.Fatalf("marshal: %v", err)
		}
		fmt.Printf("%s\n", b)
		conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
		if *once {
			break
		}
	}
}
