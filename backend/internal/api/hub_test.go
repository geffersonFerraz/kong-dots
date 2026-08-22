package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// dialHub opens a canvas's socket against a test server.
func dialHub(t *testing.T, srv *httptest.Server, connID, clientID, name string) *websocket.Conn {
	t.Helper()
	url := strings.Replace(srv.URL, "http://", "ws://", 1) +
		"/?connection_id=" + connID + "&client_id=" + clientID + "&name=" + name
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// nextOfType reads until a frame of the wanted type shows up. Presence rosters
// arrive unprompted whenever anybody joins, so a test looking for a cursor has
// to read past them.
func nextOfType(t *testing.T, c *websocket.Conn, want string) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var msg struct {
			Type    string         `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if err := c.ReadJSON(&msg); err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		if msg.Type == want {
			return msg.Payload
		}
	}
}

// expectNothing asserts a socket stays quiet, which is how "the sender does not
// get its own frame back" is verified.
func expectNothing(t *testing.T, c *websocket.Conn, forbidden string) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		var msg struct {
			Type string `json:"type"`
		}
		if err := c.ReadJSON(&msg); err != nil {
			return // read deadline: nothing more came, which is the point
		}
		if msg.Type == forbidden {
			t.Fatalf("received a %q frame that should not have been sent here", forbidden)
		}
	}
}

func newHubServer(t *testing.T) (*Hub, *httptest.Server) {
	t.Helper()
	hub := NewHub(nil)
	srv := httptest.NewServer(http.HandlerFunc(hub.Handle))
	t.Cleanup(srv.Close)
	return hub, srv
}

func send(t *testing.T, c *websocket.Conn, v any) {
	t.Helper()
	if err := c.WriteJSON(v); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestPointersReachTheOtherCanvasesAndNotTheSender(t *testing.T) {
	_, srv := newHubServer(t)
	alice := dialHub(t, srv, "conn-1", "tab-a", "alice")
	bob := dialHub(t, srv, "conn-1", "tab-b", "bob")
	// Somebody looking at a different Kong must never see any of this.
	carol := dialHub(t, srv, "conn-2", "tab-c", "carol")

	send(t, alice, map[string]any{"type": "cursor", "payload": map[string]any{"x": 120.5, "y": -40}})

	got := nextOfType(t, bob, "cursor")
	if got["id"] != "tab-a" || got["name"] != "alice" {
		t.Fatalf("cursor should say whose it is: %v", got)
	}
	if got["x"] != 120.5 || got["y"] != float64(-40) {
		t.Fatalf("cursor moved in transit: %v", got)
	}

	expectNothing(t, alice, "cursor")
	expectNothing(t, carol, "cursor")
}

func TestDraggingANodeIsRelayedWithTheFinalFrameMarked(t *testing.T) {
	_, srv := newHubServer(t)
	alice := dialHub(t, srv, "conn-1", "tab-a", "alice")
	bob := dialHub(t, srv, "conn-1", "tab-b", "bob")

	send(t, alice, map[string]any{"type": "node_move", "payload": map[string]any{
		"node": "services:svc-1", "x": 300, "y": 180,
	}})
	moving := nextOfType(t, bob, "node_move")
	if moving["node"] != "services:svc-1" || moving["x"] != float64(300) || moving["dropped"] != false {
		t.Fatalf("unexpected mid-drag frame: %v", moving)
	}

	send(t, alice, map[string]any{"type": "node_move", "payload": map[string]any{
		"node": "services:svc-1", "x": 310, "y": 190, "dropped": true,
	}})
	dropped := nextOfType(t, bob, "node_move")
	if dropped["dropped"] != true {
		t.Fatalf("the last frame of a drag must be marked: %v", dropped)
	}

	// A move with no node names nothing and is dropped rather than relayed.
	send(t, alice, map[string]any{"type": "node_move", "payload": map[string]any{"x": 1, "y": 2}})
	expectNothing(t, bob, "node_move")
}

func TestRosterListsEveryoneAndForgetsThemOnLeave(t *testing.T) {
	hub, srv := newHubServer(t)
	alice := dialHub(t, srv, "conn-1", "tab-a", "alice")
	bob := dialHub(t, srv, "conn-1", "tab-b", "bob")

	// Alice's first roster is her own join, with only herself on it; the one
	// worth asserting is the one Bob's arrival triggers.
	var roster map[string]any
	for {
		roster = nextOfType(t, alice, "presence")
		if len(roster["peers"].([]any)) == 2 {
			break
		}
	}

	// Opening a node tells everyone else which one, so two people notice before
	// they both edit it.
	send(t, bob, map[string]any{"type": "presence", "payload": map[string]any{
		"name": "bob", "node": "routes:rt-1",
	}})
	for {
		roster = nextOfType(t, alice, "presence")
		found := false
		for _, p := range roster["peers"].([]any) {
			m := p.(map[string]any)
			if m["id"] == "tab-b" && m["node"] == "routes:rt-1" {
				found = true
			}
		}
		if found {
			break
		}
	}

	bob.Close()
	// The roster shrinking is what lets a canvas drop a departed pointer.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if len(hub.Peers("conn-1")) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bob still listed after disconnecting: %v", hub.Peers("conn-1"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestReconnectingTabReplacesItsOwnRow(t *testing.T) {
	hub, srv := newHubServer(t)
	dialHub(t, srv, "conn-1", "tab-a", "alice")
	// Same tab id: a reload, not a second person.
	dialHub(t, srv, "conn-1", "tab-a", "alice")

	deadline := time.Now().Add(2 * time.Second)
	for {
		peers := hub.Peers("conn-1")
		if len(peers) == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a reconnecting tab was counted twice: %v", peers)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestMalformedFramesDoNotKillTheSocket(t *testing.T) {
	_, srv := newHubServer(t)
	alice := dialHub(t, srv, "conn-1", "tab-a", "alice")
	bob := dialHub(t, srv, "conn-1", "tab-b", "bob")

	if err := alice.WriteMessage(websocket.TextMessage, []byte("{not json")); err != nil {
		t.Fatalf("write: %v", err)
	}
	send(t, alice, map[string]any{"type": "who-knows", "payload": map[string]any{}})

	// The connection survives, and the next real frame still gets through.
	send(t, alice, map[string]any{"type": "cursor", "payload": map[string]any{"x": 5, "y": 6}})
	if got := nextOfType(t, bob, "cursor"); got["x"] != float64(5) {
		t.Fatalf("unexpected cursor after junk frames: %v", got)
	}
}

func TestCanvasEditsAreRelayedUntouched(t *testing.T) {
	_, srv := newHubServer(t)
	alice := dialHub(t, srv, "conn-1", "tab-a", "alice")
	bob := dialHub(t, srv, "conn-1", "tab-b", "bob")
	carol := dialHub(t, srv, "conn-2", "tab-c", "carol")

	// The server has no idea what a canvas op means, and must not need to: it
	// carries whatever shape the browsers agree on.
	edit := map[string]any{
		"changes": []any{
			map[string]any{"kind": "services", "id": "draft:svc-1", "value": map[string]any{
				"name": "billing", "host": "billing.internal", "config": map[string]any{"nested": []any{1.0, 2.0}},
			}},
			map[string]any{"kind": "routes", "id": "rt-1", "value": nil},
		},
	}
	send(t, alice, map[string]any{"type": "canvas_op", "payload": edit})

	got := nextOfType(t, bob, "canvas_op")
	if got["id"] != "tab-a" || got["name"] != "alice" {
		t.Fatalf("an edit must say whose it is: %v", got)
	}
	data := got["data"].(map[string]any)
	changes := data["changes"].([]any)
	if len(changes) != 2 {
		t.Fatalf("payload changed in transit: %v", data)
	}
	first := changes[0].(map[string]any)
	value := first["value"].(map[string]any)
	if value["name"] != "billing" || value["config"].(map[string]any)["nested"].([]any)[1] != 2.0 {
		t.Fatalf("nested payload mangled: %v", value)
	}
	// A removal travels as an explicit null, not as an absent key.
	if second := changes[1].(map[string]any); second["value"] != nil {
		t.Fatalf("expected an explicit null for a removal: %v", second)
	}

	expectNothing(t, alice, "canvas_op") // never echoed back to its author
	expectNothing(t, carol, "canvas_op") // and never crosses to another Kong
}

func TestJoiningTabCanAskForTheCanvasAndBeHandedIt(t *testing.T) {
	_, srv := newHubServer(t)
	alice := dialHub(t, srv, "conn-1", "tab-a", "alice")
	bob := dialHub(t, srv, "conn-1", "tab-b", "bob")

	// Bob has just opened the canvas and has no draft yet.
	send(t, bob, map[string]any{"type": "state_request", "payload": map[string]any{}})
	if asked := nextOfType(t, alice, "state_request"); asked["id"] != "tab-b" {
		t.Fatalf("the request must name who is asking: %v", asked)
	}

	send(t, alice, map[string]any{"type": "state_sync", "payload": map[string]any{
		"entities": map[string]any{"services": []any{map[string]any{"id": "svc-1", "name": "billing"}}},
	}})
	handed := nextOfType(t, bob, "state_sync")
	entities := handed["data"].(map[string]any)["entities"].(map[string]any)
	if len(entities["services"].([]any)) != 1 {
		t.Fatalf("the canvas did not survive the hand-off: %v", entities)
	}
}
