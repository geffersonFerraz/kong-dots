package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sync"

	"github.com/gorilla/websocket"
)

// Hub fans out server events (apply progress, refresh hints) to the browsers
// watching a given Kong connection.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*websocket.Conn]bool
	up      websocket.Upgrader
}

func NewHub(allowedOrigins []string) *Hub {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return &Hub{
		clients: map[string]map[*websocket.Conn]bool{},
		up: websocket.Upgrader{
			// Same-origin is always allowed (the UI served by this binary);
			// anything else must be in the configured CORS origin list.
			CheckOrigin: func(r *http.Request) bool {
				origin := r.Header.Get("Origin")
				if origin == "" || allowed[origin] {
					return true
				}
				u, err := url.Parse(origin)
				return err == nil && u.Host == r.Host
			},
		},
	}
}

type WSMessage struct {
	Type         string `json:"type"`
	ConnectionID string `json:"connection_id,omitempty"`
	Payload      any    `json:"payload,omitempty"`
}

func (h *Hub) Handle(w http.ResponseWriter, r *http.Request) {
	connID := r.URL.Query().Get("connection_id")
	if connID == "" {
		http.Error(w, "connection_id is required", http.StatusBadRequest)
		return
	}
	c, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.add(connID, c)
	defer h.remove(connID, c)
	_ = c.WriteJSON(WSMessage{Type: "hello", ConnectionID: connID})
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) add(connID string, c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[connID] == nil {
		h.clients[connID] = map[*websocket.Conn]bool{}
	}
	h.clients[connID][c] = true
}

func (h *Hub) remove(connID string, c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.clients[connID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, connID)
		}
	}
	c.Close()
}

func (h *Hub) Broadcast(connID, msgType string, payload any) {
	msg := WSMessage{Type: msgType, ConnectionID: connID, Payload: payload}
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	h.mu.RLock()
	conns := make([]*websocket.Conn, 0, len(h.clients[connID]))
	for c := range h.clients[connID] {
		conns = append(conns, c)
	}
	h.mu.RUnlock()
	for _, c := range conns {
		if err := c.WriteMessage(websocket.TextMessage, raw); err != nil {
			h.remove(connID, c)
		}
	}
}
