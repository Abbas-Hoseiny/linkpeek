package realtime

import (
	"time"

	"github.com/gorilla/websocket"
)

// WSConn wraps a Gorilla WebSocket connection to implement the Conn interface.
type WSConn struct {
	ws *websocket.Conn
}

// NewWSConn creates a new WSConn from a Gorilla WebSocket connection.
func NewWSConn(ws *websocket.Conn) *WSConn {
	return &WSConn{ws: ws}
}

// WriteMessage writes a message to the WebSocket connection.
func (w *WSConn) WriteMessage(messageType int, data []byte) error {
	return w.ws.WriteMessage(messageType, data)
}

// SetWriteDeadline sets the write deadline on the WebSocket connection.
func (w *WSConn) SetWriteDeadline(t time.Time) error {
	return w.ws.SetWriteDeadline(t)
}

// Close closes the WebSocket connection.
func (w *WSConn) Close() error {
	return w.ws.Close()
}
