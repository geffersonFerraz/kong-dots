package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gefferson/kong-dots/backend/internal/cryptox"
	"github.com/gefferson/kong-dots/backend/internal/store"
)

// fakeKong is a stand-in Admin API: it keeps whatever is POSTed so tests can
// assert on what the tool actually persisted.
type fakeKong struct {
	mu       sync.Mutex
	created  map[string][]map[string]any
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
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cipher, err := cryptox.New("unit-test-key")
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(st, cipher, NewHub(nil)).Router([]string{"*"}, "")
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
