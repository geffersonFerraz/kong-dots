package plan

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gefferson/kong-dots/backend/internal/kong"
)

func ent(j string) kong.Entity {
	var e kong.Entity
	if err := json.Unmarshal([]byte(j), &e); err != nil {
		panic(err)
	}
	return e
}

func TestBuildDetectsCreateUpdateDelete(t *testing.T) {
	current := kong.State{
		"services": {ent(`{"id":"s1","name":"api","host":"a.internal","port":80,"created_at":123}`)},
		"routes":   {ent(`{"id":"r1","name":"old","paths":["/old"],"service":{"id":"s1"}}`)},
	}
	desired := kong.State{
		"services": {ent(`{"id":"s1","name":"api","host":"b.internal","port":80}`)},
		"routes":   {ent(`{"id":"draft:route-1","name":"new","paths":["/new"],"service":{"id":"s1"}}`)},
	}

	p := Build(current, desired)
	if p.Summary != (Summary{Create: 1, Update: 1, Delete: 1}) {
		t.Fatalf("unexpected summary: %+v", p.Summary)
	}

	var update Op
	for _, op := range p.Ops {
		if op.Type == OpUpdate {
			update = op
		}
	}
	if len(update.Changes) != 1 || update.Changes[0].Field != "host" {
		t.Fatalf("expected only host to change, got %+v", update.Changes)
	}
	if _, ok := update.Payload["name"]; ok {
		t.Fatalf("unchanged fields must not be sent: %+v", update.Payload)
	}

	// Deletes must run after creates so a route is never orphaned mid-apply.
	lastCreate, firstDelete := -1, len(p.Ops)
	for i, op := range p.Ops {
		if op.Type == OpCreate && i > lastCreate {
			lastCreate = i
		}
		if op.Type == OpDelete && i < firstDelete {
			firstDelete = i
		}
	}
	if firstDelete < lastCreate {
		t.Fatalf("delete scheduled before create: %+v", p.Ops)
	}
}

func TestBuildIgnoresNoiseAndMissingKinds(t *testing.T) {
	current := kong.State{
		"services":  {ent(`{"id":"s1","name":"api","host":"a","path":null,"tags":[],"updated_at":9}`)},
		"consumers": {ent(`{"id":"c1","username":"bob"}`)},
	}
	desired := kong.State{
		"services": {ent(`{"id":"s1","name":"api","host":"a","path":"","tags":null}`)},
	}
	p := Build(current, desired)
	if !p.IsEmpty() {
		t.Fatalf("expected no changes, got %+v", p.Ops)
	}
}

func TestApplyResolvesDraftReferences(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		got = append(got, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "real-svc", "name": body["name"]})
		case r.URL.Path == "/routes":
			ref, _ := body["service"].(map[string]any)
			if ref == nil || ref["id"] != "real-svc" {
				t.Errorf("route was not rewired to the created service: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "real-route"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	c := kong.New(kong.Config{AdminURL: srv.URL})
	p := Build(kong.State{"services": {}, "routes": {}}, kong.State{
		"services": {ent(`{"id":"draft:svc-1","name":"api","host":"a"}`)},
		"routes":   {ent(`{"id":"draft:route-1","name":"r","paths":["/x"],"service":{"id":"draft:svc-1"}}`)},
	})
	res := Apply(context.Background(), c, p, nil)
	if res.Status != "success" {
		t.Fatalf("apply failed: %+v", res)
	}
	if res.IDMap["draft:svc-1"] != "real-svc" {
		t.Fatalf("id map missing draft mapping: %+v", res.IDMap)
	}
	if strings.Join(got, ",") != "POST /services,POST /routes" {
		t.Fatalf("unexpected call order: %v", got)
	}
}

func TestApplySkipsRemainingOpsAfterFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"schema violation"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	c := kong.New(kong.Config{AdminURL: srv.URL})
	p := Build(kong.State{"services": {}}, kong.State{"services": {
		ent(`{"id":"draft:a","name":"a"}`), ent(`{"id":"draft:b","name":"b"}`),
	}})
	res := Apply(context.Background(), c, p, nil)
	if res.Status != "failed" {
		t.Fatalf("expected failed, got %q", res.Status)
	}
	if res.Results[1].Status != StatusSkipped {
		t.Fatalf("expected second op skipped, got %q", res.Results[1].Status)
	}
	if !strings.Contains(res.Error, "schema violation") {
		t.Fatalf("error should surface Kong's message: %q", res.Error)
	}
}

// The bug that motivated these tests was a plugin reaching Kong with an empty
// config, so the whole persist path is asserted on the exact bytes sent.
func TestCreatePluginSendsTheConfigItWasGiven(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "plg-real"})
	}))
	defer srv.Close()

	desired := kong.State{"plugins": {ent(`{
		"id":"draft:plugins-1","name":"rate-limiting","enabled":true,
		"protocols":["http","https"],
		"config":{"minute":17,"policy":"local","fault_tolerant":false,"hour":null,
		          "redis":{"host":"redis.internal","port":6379}}
	}`)}}
	p := Build(kong.State{"plugins": {}}, desired)
	if p.Summary.Create != 1 {
		t.Fatalf("expected one create, got %+v", p.Summary)
	}

	res := Apply(context.Background(), kong.New(kong.Config{AdminURL: srv.URL}), p, nil)
	if res.Status != "success" {
		t.Fatalf("apply failed: %+v", res)
	}

	cfg, ok := body["config"].(map[string]any)
	if !ok {
		t.Fatalf("config missing from the create payload: %+v", body)
	}
	if cfg["minute"] != float64(17) {
		t.Errorf("minute not sent: %+v", cfg)
	}
	if cfg["policy"] != "local" {
		t.Errorf("policy not sent: %+v", cfg)
	}
	if cfg["fault_tolerant"] != false {
		t.Errorf("a false value must survive, got %v", cfg["fault_tolerant"])
	}
	redis, _ := cfg["redis"].(map[string]any)
	if redis["host"] != "redis.internal" {
		t.Errorf("nested record not sent: %+v", cfg["redis"])
	}
	if _, sent := body["id"]; sent {
		t.Errorf("the draft id must never be sent to Kong: %+v", body)
	}
	if body["name"] != "rate-limiting" || body["enabled"] != true {
		t.Errorf("plugin identity lost: %+v", body)
	}
}

func TestUpdatePluginSendsTheWholeConfig(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("expected PATCH for an update, got %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	current := kong.State{"plugins": {ent(`{"id":"plg-1","name":"rate-limiting","enabled":true,
		"config":{"minute":10,"policy":"local","limit_by":"consumer","hour":null}}`)}}
	desired := kong.State{"plugins": {ent(`{"id":"plg-1","name":"rate-limiting","enabled":true,
		"config":{"minute":42,"policy":"local","limit_by":"consumer","hour":null}}`)}}

	p := Build(current, desired)
	if p.Summary.Update != 1 {
		t.Fatalf("expected one update, got %+v", p.Summary)
	}
	if len(p.Ops[0].Changes) != 1 || p.Ops[0].Changes[0].Field != "config" {
		t.Fatalf("only config should be diffed, got %+v", p.Ops[0].Changes)
	}

	res := Apply(context.Background(), kong.New(kong.Config{AdminURL: srv.URL}), p, nil)
	if res.Status != "success" {
		t.Fatalf("apply failed: %+v", res)
	}
	// Kong replaces `config` wholesale on PATCH, so the untouched keys have to
	// travel with the changed one or they silently fall back to defaults.
	cfg := body["config"].(map[string]any)
	if cfg["minute"] != float64(42) || cfg["policy"] != "local" || cfg["limit_by"] != "consumer" {
		t.Errorf("config not sent whole: %+v", cfg)
	}
	if _, sent := body["name"]; sent {
		t.Errorf("unchanged fields must stay out of a PATCH: %+v", body)
	}
}

func TestCreatePluginAttachedToADraftParent(t *testing.T) {
	bodies := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies[r.URL.Path] = body
		switch r.URL.Path {
		case "/services":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "svc-real"})
		case "/routes":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "rt-real"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "plg-real"})
		}
	}))
	defer srv.Close()

	desired := kong.State{
		"services": {ent(`{"id":"draft:svc","name":"api","host":"api.internal"}`)},
		"routes":   {ent(`{"id":"draft:rt","name":"r","paths":["/x"],"service":{"id":"draft:svc"}}`)},
		"plugins":  {ent(`{"id":"draft:plg","name":"key-auth","config":{"key_names":["apikey"]},"route":{"id":"draft:rt"}}`)},
	}
	p := Build(kong.State{"services": {}, "routes": {}, "plugins": {}}, desired)
	res := Apply(context.Background(), kong.New(kong.Config{AdminURL: srv.URL}), p, nil)
	if res.Status != "success" {
		t.Fatalf("apply failed: %+v", res)
	}

	route := bodies["/routes"]["service"].(map[string]any)
	if route["id"] != "svc-real" {
		t.Errorf("route not wired to the created service: %+v", bodies["/routes"])
	}
	plugin := bodies["/plugins"]
	ref := plugin["route"].(map[string]any)
	if ref["id"] != "rt-real" {
		t.Errorf("plugin not wired to the created route: %+v", plugin)
	}
	cfg := plugin["config"].(map[string]any)
	if names, _ := cfg["key_names"].([]any); len(names) != 1 || names[0] != "apikey" {
		t.Errorf("plugin config lost while resolving references: %+v", cfg)
	}
}

func TestUpdateKeepsMeaningfulZeroValues(t *testing.T) {
	current := kong.State{"services": {ent(`{"id":"s1","name":"api","enabled":true,"retries":5,"path":"/v1"}`)}}
	desired := kong.State{"services": {ent(`{"id":"s1","name":"api","enabled":false,"retries":0,"path":""}`)}}

	p := Build(current, desired)
	if p.Summary.Update != 1 {
		t.Fatalf("expected an update, got %+v", p.Summary)
	}
	payload := p.Ops[0].Payload
	if payload["enabled"] != false {
		t.Errorf("enabled=false must be sent, got %v", payload["enabled"])
	}
	if payload["retries"] != float64(0) {
		t.Errorf("retries=0 must be sent, got %v", payload["retries"])
	}
	if payload["path"] != "" {
		t.Errorf("clearing a string must be sent, got %v", payload["path"])
	}
}

func TestPluginMovedBetweenParents(t *testing.T) {
	current := kong.State{"plugins": {ent(`{"id":"p1","name":"key-auth","service":{"id":"s1"},"route":null,"consumer":null}`)}}
	desired := kong.State{"plugins": {ent(`{"id":"p1","name":"key-auth","service":null,"route":{"id":"r1"},"consumer":null}`)}}

	p := Build(current, desired)
	if p.Summary.Update != 1 {
		t.Fatalf("expected an update, got %+v", p.Summary)
	}
	payload := p.Ops[0].Payload
	if payload["service"] != nil {
		t.Errorf("the old owner must be cleared, got %v", payload["service"])
	}
	ref, ok := payload["route"].(map[string]any)
	if !ok || ref["id"] != "r1" {
		t.Errorf("the new owner must be sent, got %v", payload["route"])
	}
}

func TestTargetUpdateIsReplacedNotPatched(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "tgt-new"})
	}))
	defer srv.Close()

	current := kong.State{"targets": {ent(`{"id":"t1","target":"10.0.0.1:80","weight":100,"upstream":{"id":"u1"}}`)}}
	desired := kong.State{"targets": {ent(`{"id":"t1","target":"10.0.0.1:80","weight":50,"upstream":{"id":"u1"}}`)}}

	p := Build(current, desired)
	res := Apply(context.Background(), kong.New(kong.Config{AdminURL: srv.URL}), p, nil)
	if res.Status != "success" {
		t.Fatalf("apply failed: %+v", res)
	}
	if strings.Join(calls, ",") != "DELETE /upstreams/u1/targets/t1,POST /upstreams/u1/targets" {
		t.Fatalf("targets are immutable and must be replaced, got %v", calls)
	}
}

func TestApplyReportsProgressForEveryOperation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "new"})
	}))
	defer srv.Close()

	p := Build(kong.State{"services": {}}, kong.State{"services": {
		ent(`{"id":"draft:a","name":"a","host":"a"}`), ent(`{"id":"draft:b","name":"b","host":"b"}`),
	}})

	var events []string
	res := Apply(context.Background(), kong.New(kong.Config{AdminURL: srv.URL}), p, func(ev Event) {
		events = append(events, ev.Kind)
	})
	if res.Status != "success" {
		t.Fatalf("apply failed: %+v", res)
	}
	want := "op_start,op_done,op_start,op_done,finished"
	if strings.Join(events, ",") != want {
		t.Fatalf("progress stream was %v, want %s", events, want)
	}
}
