package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gefferson/kong-flow/backend/internal/cryptox"
	"github.com/gefferson/kong-flow/backend/internal/storetest"
)

// fakeKong is a stand-in Admin API: it keeps whatever is POSTed so tests can
// assert on what the tool actually persisted.
type fakeKong struct {
	mu      sync.Mutex
	created map[string][]map[string]any
	// deleted records the paths a DELETE reached, which is how a rollback is
	// checked to have actually removed what it said it would.
	deleted  []string
	services []map[string]any
	srv      *httptest.Server
}

func newFakeKong(t *testing.T) *fakeKong {
	t.Helper()
	f := &fakeKong{created: map[string][]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			writeJSONTest(w, map[string]any{
				"version": "3.9.1", "hostname": "fake",
				"configuration": map[string]any{"edition": "community"},
				"plugins":       map[string]any{"available_on_server": map[string]any{"rate-limiting": true, "key-auth": true}},
			})
		case strings.HasPrefix(r.URL.Path, "/schemas/"):
			writeJSONTest(w, map[string]any{"fields": []any{map[string]any{"schema_for": r.URL.Path}}})
		case r.Method == http.MethodPost:
			kind := strings.Trim(r.URL.Path, "/")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.created[kind] = append(f.created[kind], body)
			id := fmt.Sprintf("%s-%d", kind, len(f.created[kind]))
			f.mu.Unlock()
			body["id"] = id
			writeJSONTest(w, body)
		case r.Method == http.MethodDelete:
			f.mu.Lock()
			f.deleted = append(f.deleted, r.URL.Path)
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/services":
			f.mu.Lock()
			defer f.mu.Unlock()
			writeJSONTest(w, map[string]any{"data": f.services})
		case r.Method == http.MethodGet:
			writeJSONTest(w, map[string]any{"data": []any{}})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSONTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	// No approvers configured: every editor applies directly, which is the
	// behaviour most of these tests are about.
	return newTestServerWith(t, Approval{})
}

// newTestServerWith builds a server whose apply is gated on review.
func newTestServerWith(t *testing.T, approval Approval) http.Handler {
	t.Helper()
	st := storetest.New(t)
	cipher, err := cryptox.New("unit-test-key")
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(st, cipher, NewHub(nil), approval).Router([]string{"*"}, "")
}

// asActor is `do` with a name (and optionally an approval token) attached, the
// way the browser identifies itself.
func asActor(t *testing.T, h http.Handler, actor, token, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(headerActor, actor)
	if token != "" {
		req.Header.Set(headerToken, token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func do(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func registerConnection(t *testing.T, h http.Handler, adminURL string) string {
	t.Helper()
	rec, body := do(t, h, http.MethodPost, "/api/connections", map[string]any{
		"name": "unit", "admin_api_url": adminURL, "auth_type": "key", "auth_secret": "s3cr3t",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create connection: %d %s", rec.Code, rec.Body)
	}
	return body["id"].(string)
}

func TestConnectionCRUDNeverLeaksTheSecret(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	id := registerConnection(t, h, kong.srv.URL)

	rec, body := do(t, h, http.MethodGet, "/api/connections/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	if _, leaked := body["auth_secret"]; leaked {
		t.Errorf("the stored credential must never be returned: %v", body)
	}
	if body["has_secret"] != true {
		t.Errorf("has_secret should tell the UI a credential exists: %v", body)
	}

	// Updating without auth_secret keeps the stored one.
	rec, _ = do(t, h, http.MethodPut, "/api/connections/"+id, map[string]any{
		"name": "unit-renamed", "admin_api_url": kong.srv.URL, "auth_type": "key",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	_, body = do(t, h, http.MethodGet, "/api/connections/"+id, nil)
	if body["name"] != "unit-renamed" || body["has_secret"] != true {
		t.Errorf("update dropped the credential: %v", body)
	}

	rec, _ = do(t, h, http.MethodDelete, "/api/connections/"+id, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	rec, _ = do(t, h, http.MethodGet, "/api/connections/"+id, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deleted connection should be gone, got %d", rec.Code)
	}
}

func TestValidationRejectsBadInput(t *testing.T) {
	h := newTestServer(t)
	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{"no name", map[string]any{"admin_api_url": "http://x"}},
		{"no url", map[string]any{"name": "x"}},
		{"url without scheme", map[string]any{"name": "x", "admin_api_url": "kong:8001"}},
		{"unknown auth", map[string]any{"name": "x", "admin_api_url": "http://x", "auth_type": "magic"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := do(t, h, http.MethodPost, "/api/connections", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
			if body["error"] == nil {
				t.Errorf("an explanation should come back: %v", body)
			}
		})
	}
}

func TestStateAndLayoutRoundTrip(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	kong.services = []map[string]any{{"id": "svc-1", "name": "api", "host": "api.internal"}}
	id := registerConnection(t, h, kong.srv.URL)

	rec, body := do(t, h, http.MethodPut, "/api/connections/"+id+"/layout", map[string]any{
		"positions": []map[string]any{{"entity_type": "services", "entity_id": "svc-1", "x": 120, "y": 240}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("save layout: %d %s", rec.Code, rec.Body)
	}

	rec, body = do(t, h, http.MethodGet, "/api/connections/"+id+"/state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("state: %d %s", rec.Code, rec.Body)
	}
	state := body["state"].(map[string]any)
	if len(state["services"].([]any)) != 1 {
		t.Errorf("live services missing: %v", state)
	}
	pos := body["layout"].(map[string]any)["services:svc-1"].(map[string]any)
	if pos["x"] != float64(120) || pos["y"] != float64(240) {
		t.Errorf("layout not persisted: %v", pos)
	}
}

// Regression for the gin migration: /schemas/plugins/{name} and /schemas/{kind}
// are siblings that a radix router cannot express as separate routes.
func TestSchemaPassthroughHandlesBothShapes(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	id := registerConnection(t, h, kong.srv.URL)

	for _, path := range []string{"plugins/rate-limiting", "services", "plugins/pre-function"} {
		rec, body := do(t, h, http.MethodGet, "/api/connections/"+id+"/schemas/"+path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("schema %s: %d %s", path, rec.Code, rec.Body)
		}
		got := body["fields"].([]any)[0].(map[string]any)["schema_for"]
		if got != "/schemas/"+path {
			t.Errorf("schema %s proxied to %v", path, got)
		}
	}

	rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/schemas/../../etc/passwd", nil)
	if rec.Code == http.StatusOK {
		t.Errorf("traversal should be rejected, got %d", rec.Code)
	}
}

func TestPlanAndApplyPersistWhatTheCanvasSent(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	id := registerConnection(t, h, kong.srv.URL)

	desired := map[string]any{"desired": map[string]any{
		"services": []map[string]any{{"id": "draft:svc", "name": "api", "host": "api.internal", "port": 8080}},
		"plugins": []map[string]any{{
			"id": "draft:plg", "name": "rate-limiting", "enabled": true,
			"config":  map[string]any{"minute": 17, "policy": "local"},
			"service": map[string]any{"id": "draft:svc"},
		}},
		"routes": []map[string]any{}, "consumers": []map[string]any{},
		"upstreams": []map[string]any{}, "targets": []map[string]any{},
	}}

	rec, body := do(t, h, http.MethodPost, "/api/connections/"+id+"/plan", desired)
	if rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	summary := body["summary"].(map[string]any)
	if summary["create"] != float64(2) {
		t.Fatalf("expected two creates, got %v", summary)
	}

	rec, body = do(t, h, http.MethodPost, "/api/connections/"+id+"/apply", desired)
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if status := body["result"].(map[string]any)["status"]; status != "success" {
		t.Fatalf("apply status %v: %s", status, rec.Body)
	}

	kong.mu.Lock()
	defer kong.mu.Unlock()
	plugin := kong.created["plugins"][0]
	cfg, ok := plugin["config"].(map[string]any)
	if !ok || cfg["minute"] != float64(17) || cfg["policy"] != "local" {
		t.Fatalf("the plugin reached Kong without its config: %v", plugin)
	}
	if ref := plugin["service"].(map[string]any); ref["id"] != "services-1" {
		t.Errorf("draft reference not resolved: %v", plugin["service"])
	}

	// The run is auditable afterwards.
	rec, _ = do(t, h, http.MethodGet, "/api/connections/"+id+"/history", nil)
	var history []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	if len(history) != 1 || history[0]["status"] != "success" {
		t.Errorf("apply not recorded in history: %s", rec.Body)
	}
}

func TestApplyRejectsAnEmptyRequest(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	id := registerConnection(t, h, kong.srv.URL)

	rec, body := do(t, h, http.MethodPost, "/api/connections/"+id+"/apply", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(fmt.Sprint(body["error"]), "desired") {
		t.Errorf("error should say what is missing: %v", body)
	}
}

func TestExportAndImportRoundTrip(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	kong.services = []map[string]any{{"id": "svc-1", "name": "api", "host": "api.internal", "port": float64(80)}}
	id := registerConnection(t, h, kong.srv.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/connections/"+id+"/export", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body)
	}
	yaml := rec.Body.String()
	if !strings.Contains(yaml, "name: api") || !strings.Contains(yaml, `_format_version: "3.0"`) {
		t.Fatalf("unexpected decK output:\n%s", yaml)
	}
	if !strings.Contains(rec.Header().Get("Content-Disposition"), "unit.kong.yaml") {
		t.Errorf("export should download as a file: %q", rec.Header().Get("Content-Disposition"))
	}

	req = httptest.NewRequest(http.MethodPost, "/api/connections/"+id+"/import", strings.NewReader(yaml))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	services := out["state"].(map[string]any)["services"].([]any)
	if len(services) != 1 {
		t.Fatalf("import lost the service: %s", rec.Body)
	}
	if name := services[0].(map[string]any)["name"]; name != "api" {
		t.Errorf("imported service is wrong: %v", name)
	}
}

func TestUnknownConnectionAndEndpoint(t *testing.T) {
	h := newTestServer(t)
	for _, path := range []string{
		"/api/connections/does-not-exist/state",
		"/api/connections/does-not-exist/history",
	} {
		rec, _ := do(t, h, http.MethodGet, path, nil)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusOK {
			t.Errorf("%s: unexpected %d", path, rec.Code)
		}
	}
	rec, _ := do(t, h, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("healthz should answer 200, got %d", rec.Code)
	}
}

func TestBaseURLIsStoredAndValidated(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)

	rec, body := do(t, h, http.MethodPost, "/api/connections", map[string]any{
		"name": "prod", "admin_api_url": kong.srv.URL, "base_url": "https://api.example.com/",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	// The trailing slash is trimmed so the UI can concatenate a path safely.
	if body["base_url"] != "https://api.example.com" {
		t.Errorf("base_url not normalised: %v", body["base_url"])
	}

	id := body["id"].(string)
	rec, body = do(t, h, http.MethodPut, "/api/connections/"+id, map[string]any{
		"name": "prod", "admin_api_url": kong.srv.URL, "base_url": "https://edge.example.com",
	})
	if rec.Code != http.StatusOK || body["base_url"] != "https://edge.example.com" {
		t.Errorf("base_url not updated: %d %v", rec.Code, body["base_url"])
	}

	// It is optional...
	rec, body = do(t, h, http.MethodPost, "/api/connections", map[string]any{
		"name": "no-base", "admin_api_url": kong.srv.URL,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("base_url should be optional: %d %s", rec.Code, rec.Body)
	}
	if _, present := body["base_url"]; present {
		t.Errorf("an empty base_url should be omitted, got %v", body["base_url"])
	}

	// ...but a typo is caught rather than producing broken Route URLs later.
	rec, body = do(t, h, http.MethodPost, "/api/connections", map[string]any{
		"name": "bad", "admin_api_url": kong.srv.URL, "base_url": "api.example.com",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a schemeless base_url, got %d", rec.Code)
	}
	if !strings.Contains(fmt.Sprint(body["error"]), "base_url") {
		t.Errorf("error should name the field: %v", body["error"])
	}
}

// ------------------------------------------------- approval queue

// desiredWithOneService is the smallest canvas that asks Kong for something.
func desiredWithOneService(name string) map[string]any {
	return map[string]any{
		"desired": map[string]any{
			"services":  []map[string]any{{"id": "draft:svc", "name": name, "host": name + ".internal", "port": 80}},
			"routes":    []map[string]any{},
			"plugins":   []map[string]any{},
			"consumers": []map[string]any{},
			"upstreams": []map[string]any{},
			"targets":   []map[string]any{},
		},
		"baseline": map[string]any{
			"services": []map[string]any{}, "routes": []map[string]any{}, "plugins": []map[string]any{},
			"consumers": []map[string]any{}, "upstreams": []map[string]any{}, "targets": []map[string]any{},
		},
		"title": "add " + name,
	}
}

func TestEditorsApplyIntoTheQueueInsteadOfIntoKong(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	rec, body := asActor(t, h, "bob", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected the change to be queued (202), got %d %s", rec.Code, rec.Body)
	}
	if body["status"] != "pending_approval" {
		t.Fatalf("an editor's apply must not reach Kong: %v", body)
	}
	fake.mu.Lock()
	reached := len(fake.created["services"])
	fake.mu.Unlock()
	if reached != 0 {
		t.Fatalf("%d service(s) reached Kong without approval", reached)
	}

	request := body["request"].(map[string]any)
	if request["requested_by"] != "bob" || request["status"] != "pending" {
		t.Fatalf("unexpected request: %v", request)
	}

	// It is waiting where an approver can find it.
	rec, listed := asActor(t, h, "alice", "", http.MethodGet, "/api/connections/"+id+"/requests?status=pending", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list requests: %d %s", rec.Code, rec.Body)
	}
	if reqs := listed["requests"].([]any); len(reqs) != 1 {
		t.Fatalf("expected one pending request, got %v", reqs)
	}
	if identity := listed["identity"].(map[string]any); identity["approver"] != true {
		t.Fatalf("alice is on the approver list: %v", identity)
	}
}

func TestOnlyAnApproverCanPushAQueuedChange(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	_, body := asActor(t, h, "bob", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	reqID := body["request"].(map[string]any)["id"].(string)
	approve := "/api/connections/" + id + "/requests/" + reqID + "/approve"

	// Bob cannot wave his own change through.
	if rec, _ := asActor(t, h, "bob", "", http.MethodPost, approve, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-approver, got %d %s", rec.Code, rec.Body)
	}

	rec, out := asActor(t, h, "alice", "", http.MethodPost, approve, map[string]any{"note": "looks fine"})
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if status := out["result"].(map[string]any)["status"]; status != "success" {
		t.Fatalf("apply status %v: %s", status, rec.Body)
	}
	fake.mu.Lock()
	created := fake.created["services"]
	fake.mu.Unlock()
	if len(created) != 1 || created[0]["name"] != "billing" {
		t.Fatalf("the approved change did not reach Kong: %v", created)
	}

	// A decided request cannot be applied twice.
	if rec, _ := asActor(t, h, "alice", "", http.MethodPost, approve, nil); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on a second approval, got %d %s", rec.Code, rec.Body)
	}

	// And it is recorded against the person who approved it.
	rec, _ = asActor(t, h, "alice", "", http.MethodGet, "/api/connections/"+id+"/history", nil)
	var history []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	if len(history) != 1 || !strings.Contains(fmt.Sprint(history[0]["actor"]), "alice") ||
		!strings.Contains(fmt.Sprint(history[0]["actor"]), "bob") {
		t.Errorf("history should name both the approver and the author: %s", rec.Body)
	}
}

func TestApprovalTokenIsRequiredWhenOneIsConfigured(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}, Token: "s3cret"})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	// The right name without the token is still just an editor.
	rec, body := asActor(t, h, "alice", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	if rec.Code != http.StatusAccepted || body["status"] != "pending_approval" {
		t.Fatalf("a name alone must not grant approval: %d %s", rec.Code, rec.Body)
	}
	reqID := body["request"].(map[string]any)["id"].(string)

	// The token in somebody else's hands is not enough either.
	approve := "/api/connections/" + id + "/requests/" + reqID + "/approve"
	if rec, _ := asActor(t, h, "mallory", "s3cret", http.MethodPost, approve, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a token holder who is not on the list, got %d %s", rec.Code, rec.Body)
	}

	if rec, _ := asActor(t, h, "alice", "s3cret", http.MethodPost, approve, nil); rec.Code != http.StatusOK {
		t.Fatalf("approve with name and token: %d %s", rec.Code, rec.Body)
	}
}

func TestRejectedRequestNeverReachesKong(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	_, body := asActor(t, h, "bob", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	reqID := body["request"].(map[string]any)["id"].(string)

	rec, out := asActor(t, h, "alice", "", http.MethodPost,
		"/api/connections/"+id+"/requests/"+reqID+"/reject", map[string]any{"note": "use the shared upstream"})
	if rec.Code != http.StatusOK {
		t.Fatalf("reject: %d %s", rec.Code, rec.Body)
	}
	request := out["request"].(map[string]any)
	if request["status"] != "rejected" || request["review_note"] != "use the shared upstream" {
		t.Fatalf("unexpected request after reject: %v", request)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if n := len(fake.created["services"]); n != 0 {
		t.Fatalf("a rejected change reached Kong (%d services)", n)
	}
}

func TestAuthorCanWithdrawTheirOwnRequest(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	_, body := asActor(t, h, "bob", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	reqID := body["request"].(map[string]any)["id"].(string)
	withdraw := "/api/connections/" + id + "/requests/" + reqID + "/withdraw"

	if rec, _ := asActor(t, h, "carol", "", http.MethodPost, withdraw, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 withdrawing somebody else's request, got %d %s", rec.Code, rec.Body)
	}
	if rec, _ := asActor(t, h, "bob", "", http.MethodPost, withdraw, nil); rec.Code != http.StatusOK {
		t.Fatalf("bob should be able to take back his own request: %d %s", rec.Code, rec.Body)
	}
}

func TestWithoutApproversEveryoneAppliesDirectly(t *testing.T) {
	h := newTestServer(t) // no approvers configured
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	rec, _ := asActor(t, h, "bob", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	if rec.Code != http.StatusOK {
		t.Fatalf("a single-operator install must keep applying directly: %d %s", rec.Code, rec.Body)
	}
	rec, me := do(t, h, http.MethodGet, "/api/me", nil)
	if rec.Code != http.StatusOK || me["approver"] != true || me["approval_required"] != false {
		t.Fatalf("unexpected identity: %v", me)
	}
}

func TestQueuedChangeIsRePlannedAgainstKongAtApprovalTime(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	// Bob's canvas saw an empty Kong and proposes deleting nothing.
	_, body := asActor(t, h, "bob", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	reqID := body["request"].(map[string]any)["id"].(string)

	// Meanwhile somebody adds a service straight into Kong.
	fake.mu.Lock()
	fake.services = []map[string]any{{"id": "s-out-of-band", "name": "legacy", "host": "legacy.internal"}}
	fake.mu.Unlock()

	// The plan the approver is shown is built against Kong as it is now, and it
	// leaves the service Bob never saw alone.
	rec, out := asActor(t, h, "alice", "", http.MethodGet, "/api/connections/"+id+"/requests/"+reqID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get request: %d %s", rec.Code, rec.Body)
	}
	fresh := out["plan"].(map[string]any)
	if summary := fresh["summary"].(map[string]any); summary["delete"] != float64(0) {
		t.Fatalf("a queued change must not delete what its author never saw: %v", summary)
	}
	if ignored := fresh["ignored"].([]any); len(ignored) != 1 {
		t.Fatalf("expected the out-of-band service to be reported as ignored: %v", fresh["ignored"])
	}
}

// ------------------------------------------------------------- rollback

func TestRollbackUndoesARunAndIsItselfRecorded(t *testing.T) {
	h := newTestServer(t)
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	// Apply something, then find it in the history.
	rec, _ := do(t, h, http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	if rec.Code != http.StatusOK {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	rec, _ = do(t, h, http.MethodGet, "/api/connections/"+id+"/history", nil)
	var history []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	if len(history) != 1 {
		t.Fatalf("expected one run in the history: %s", rec.Body)
	}
	runID := history[0]["id"].(string)

	// Kong now reports the service that run created.
	fake.mu.Lock()
	fake.services = []map[string]any{{"id": "services-1", "name": "billing", "host": "billing.internal", "port": float64(80)}}
	fake.mu.Unlock()

	// The preview says what undoing it would do, without doing it.
	rec, preview := do(t, h, http.MethodGet, "/api/connections/"+id+"/history/"+runID+"/rollback", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body)
	}
	summary := preview["plan"].(map[string]any)["summary"].(map[string]any)
	if summary["delete"] != float64(1) {
		t.Fatalf("undoing a create should delete it: %v", summary)
	}
	fake.mu.Lock()
	deletedBefore := len(fake.deleted)
	fake.mu.Unlock()
	if deletedBefore != 0 {
		t.Fatalf("a preview must not touch Kong (%d deletes)", deletedBefore)
	}

	// Running it does reach Kong.
	rec, out := do(t, h, http.MethodPost, "/api/connections/"+id+"/history/"+runID+"/rollback", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	if status := out["result"].(map[string]any)["status"]; status != "success" {
		t.Fatalf("rollback status %v: %s", status, rec.Body)
	}
	fake.mu.Lock()
	deleted := append([]string{}, fake.deleted...)
	fake.mu.Unlock()
	if len(deleted) != 1 || !strings.Contains(deleted[0], "services-1") {
		t.Fatalf("expected the created service to be deleted, got %v", deleted)
	}

	// The rollback is itself a run, so it can be rolled back in turn.
	rec, _ = do(t, h, http.MethodGet, "/api/connections/"+id+"/history", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	if len(history) != 2 {
		t.Fatalf("the rollback should be recorded too: %s", rec.Body)
	}
	if !strings.Contains(fmt.Sprint(history[0]["actor"]), "rolled back") {
		t.Errorf("history should say what it was: %v", history[0]["actor"])
	}
}

func TestRollbackRefusesWhenKongMovedOn(t *testing.T) {
	h := newTestServer(t)
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	do(t, h, http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/history", nil)
	var history []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	runID := history[0]["id"].(string)

	// Somebody edited the service after that run.
	fake.mu.Lock()
	fake.services = []map[string]any{{"id": "services-1", "name": "billing", "host": "moved.internal", "port": float64(80)}}
	fake.mu.Unlock()

	rec, out := do(t, h, http.MethodPost, "/api/connections/"+id+"/history/"+runID+"/rollback", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 when the entity drifted, got %d %s", rec.Code, rec.Body)
	}
	if conflicts := out["plan"].(map[string]any)["conflicts"].([]any); len(conflicts) != 1 {
		t.Fatalf("the refusal must say what is in the way: %v", out["plan"])
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.deleted) != 0 {
		t.Fatalf("a refused rollback must not touch Kong: %v", fake.deleted)
	}
}

func TestOnlyAnApproverCanRollBack(t *testing.T) {
	h := newTestServerWith(t, Approval{Approvers: []string{"alice"}})
	fake := newFakeKong(t)
	id := registerConnection(t, h, fake.srv.URL)

	// Alice applies, so there is something to undo.
	asActor(t, h, "alice", "", http.MethodPost, "/api/connections/"+id+"/apply", desiredWithOneService("billing"))
	rec, _ := asActor(t, h, "alice", "", http.MethodGet, "/api/connections/"+id+"/history", nil)
	var history []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	runID := history[0]["id"].(string)
	path := "/api/connections/" + id + "/history/" + runID + "/rollback"

	if rec, _ := asActor(t, h, "bob", "", http.MethodPost, path, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for an editor, got %d %s", rec.Code, rec.Body)
	}
	// Reading what a rollback would do is safe for anyone.
	if rec, _ := asActor(t, h, "bob", "", http.MethodGet, path, nil); rec.Code != http.StatusOK {
		t.Fatalf("preview should be readable: %d %s", rec.Code, rec.Body)
	}
}

func TestRollbackOfAnotherKongsRunIsNotFound(t *testing.T) {
	h := newTestServer(t)
	fake := newFakeKong(t)
	mine := registerConnection(t, h, fake.srv.URL)
	other := registerConnection(t, h, fake.srv.URL)

	do(t, h, http.MethodPost, "/api/connections/"+mine+"/apply", desiredWithOneService("billing"))
	rec, _ := do(t, h, http.MethodGet, "/api/connections/"+mine+"/history", nil)
	var history []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &history)
	runID := history[0]["id"].(string)

	rec, _ = do(t, h, http.MethodGet, "/api/connections/"+other+"/history/"+runID+"/rollback", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("a run must not be reachable through another Kong: %d %s", rec.Code, rec.Body)
	}
}
