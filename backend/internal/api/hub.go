package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Hub fans out server events (apply progress, refresh hints) to the browsers
// watching a given Kong connection, and keeps track of who is watching so each
// canvas can show the other people editing the same gateway.
type Hub struct {
	mu      sync.RWMutex
	clients map[string]map[*client]bool
	up      websocket.Upgrader
}

// client is one open canvas. A gorilla connection tolerates exactly one writer
// at a time, and both broadcasts and presence updates write to it from other
// goroutines, so every write goes through its own mutex.
type client struct {
	conn *websocket.Conn
	wmu  sync.Mutex

	mu   sync.RWMutex
	id   string // stable per browser tab, so a reconnect replaces the old row
	name string // display name the person chose
	node string // node id currently open in their properties panel
	at   time.Time
}

// Peer is what the other canvases are told about one editor.
type Peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Node     string `json:"node,omitempty"`
	JoinedAt string `json:"joined_at"`
}

func (c *client) peer() Peer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return Peer{ID: c.id, Name: c.name, Node: c.node, JoinedAt: c.at.UTC().Format(time.RFC3339)}
}

func (c *client) write(raw []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteMessage(websocket.TextMessage, raw)
}

func NewHub(allowedOrigins []string) *Hub {
	allowed := map[string]bool{}
	for _, o := range allowedOrigins {
		allowed[o] = true
	}
	return &Hub{
		clients: map[string]map[*client]bool{},
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

// clientMessage is what a canvas sends up the socket: who is here, where their
// pointer is, which node they are dragging, and the edits they make to the
// shared draft. Applying that draft to Kong still goes through the REST API —
// nothing here reaches the gateway.
type clientMessage struct {
	Type    string `json:"type"`
	Payload struct {
		Name string  `json:"name"`
		Node string  `json:"node"`
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		// Gone marks a pointer that left the canvas, so the others can drop it
		// instead of leaving it frozen where it was last seen.
		Gone bool `json:"gone"`
		// Dropped marks the last frame of a drag, the one worth persisting.
		Dropped bool `json:"dropped"`
	} `json:"payload"`
}

// envelope is how a canvas frame reaches the others: whose it is, plus the
// payload passed through untouched. The server does not interpret canvas edits,
// it only decides who sees them.
type envelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type relayed struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

// Pointer frames are tiny, but a full canvas hand-off to somebody who just
// joined carries every entity and its plugin config. This only bounds what a
// misbehaving client can send.
const maxClientFrame = 4 << 20

// passthrough frames the server relays without looking inside: edits to the
// shared draft, and the canvas hand-off a joining tab asks for.
var passthrough = map[string]bool{
	"canvas_op":     true,
	"state_request": true,
	"state_sync":    true,
}

func (h *Hub) Handle(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	connID := q.Get("connection_id")
	if connID == "" {
		http.Error(w, "connection_id is required", http.StatusBadRequest)
		return
	}
	conn, err := h.up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &client{
		conn: conn,
		id:   defaultStr(q.Get("client_id"), uuid.NewString()),
		name: defaultStr(q.Get("name"), "Someone"),
		at:   time.Now(),
	}

	h.add(connID, c)
	defer func() {
		h.remove(connID, c)
		h.broadcastPresence(connID)
	}()

	h.send(c, WSMessage{Type: "hello", ConnectionID: connID, Payload: gin.H{"client_id": c.id}})
	h.broadcastPresence(connID)

	conn.SetReadLimit(maxClientFrame)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var env envelope
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		if passthrough[env.Type] {
			p := c.peer()
			h.relay(connID, c, env.Type, relayed{ID: p.ID, Name: p.Name, Data: env.Payload})
			continue
		}

		var msg clientMessage
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "presence":
			c.mu.Lock()
			if msg.Payload.Name != "" {
				c.name = msg.Payload.Name
			}
			c.node = msg.Payload.Node
			c.mu.Unlock()
			h.broadcastPresence(connID)

		// Pointers and drags arrive many times a second, so they are relayed as
		// they come instead of going through the roster, which would rebuild and
		// re-send the whole peer list on every frame of every mouse move.
		case "cursor":
			p := c.peer()
			h.relay(connID, c, "cursor", gin.H{
				"id": p.ID, "name": p.Name, "x": msg.Payload.X, "y": msg.Payload.Y, "gone": msg.Payload.Gone,
			})
		case "node_move":
			if msg.Payload.Node == "" {
				continue
			}
			p := c.peer()
			h.relay(connID, c, "node_move", gin.H{
				"id": p.ID, "name": p.Name, "node": msg.Payload.Node,
				"x": msg.Payload.X, "y": msg.Payload.Y, "dropped": msg.Payload.Dropped,
			})
		}
	}
}

func (h *Hub) add(connID string, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[connID] == nil {
		h.clients[connID] = map[*client]bool{}
	}
	// A tab that reconnects (page reload, dropped socket) would otherwise show
	// up twice until the stale connection times out.
	for other := range h.clients[connID] {
		if other != c && other.peer().ID == c.id {
			delete(h.clients[connID], other)
			other.conn.Close()
		}
	}
	h.clients[connID][c] = true
}

func (h *Hub) remove(connID string, c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.clients[connID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, connID)
		}
	}
	c.conn.Close()
}

func (h *Hub) peers(connID string) []*client {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*client, 0, len(h.clients[connID]))
	for c := range h.clients[connID] {
		out = append(out, c)
	}
	return out
}

// Peers lists everyone currently watching a connection, oldest first so the
// roster does not reshuffle on every update.
func (h *Hub) Peers(connID string) []Peer {
	clients := h.peers(connID)
	out := make([]Peer, 0, len(clients))
	for _, c := range clients {
		out = append(out, c.peer())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].JoinedAt != out[j].JoinedAt {
			return out[i].JoinedAt < out[j].JoinedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (h *Hub) broadcastPresence(connID string) {
	h.Broadcast(connID, "presence", gin.H{"peers": h.Peers(connID)})
}

func (h *Hub) send(c *client, msg WSMessage) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	if err := c.write(raw); err != nil {
		c.conn.Close()
	}
}

func (h *Hub) Broadcast(connID, msgType string, payload any) {
	h.fanOut(connID, nil, msgType, payload)
}

// relay forwards one client's ephemeral frame to everyone else on the same
// Kong. The sender is skipped: it already knows where its own pointer is, and
// echoing it back would fight with the local rendering.
func (h *Hub) relay(connID string, from *client, msgType string, payload any) {
	h.fanOut(connID, from, msgType, payload)
}

func (h *Hub) fanOut(connID string, except *client, msgType string, payload any) {
	msg := WSMessage{Type: msgType, ConnectionID: connID, Payload: payload}
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for _, c := range h.peers(connID) {
		if c == except {
			continue
		}
		if err := c.write(raw); err != nil {
			h.remove(connID, c)
		}
	}
}
