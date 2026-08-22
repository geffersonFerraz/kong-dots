package deck

import (
	"strings"
	"testing"

	"github.com/gefferson/kong-flow/backend/internal/kong"
)

func TestExportNestsRoutesAndPlugins(t *testing.T) {
	state := kong.State{
		"services": {{"id": "s1", "name": "api", "host": "a.internal", "port": float64(80), "created_at": float64(1)}},
		"routes":   {{"id": "r1", "name": "api-root", "paths": []any{"/"}, "service": map[string]any{"id": "s1"}}},
		"plugins": {
			{"id": "p1", "name": "key-auth", "route": map[string]any{"id": "r1"}, "config": map[string]any{"key_names": []any{"apikey"}}},
			{"id": "p2", "name": "correlation-id"},
		},
		"upstreams": {{"id": "u1", "name": "pool"}},
		"targets":   {{"id": "t1", "target": "10.0.0.1:8080", "weight": float64(100), "upstream": map[string]any{"id": "u1"}}},
	}
	out, err := Export(state, false)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"_format_version: \"3.0\"", "name: api", "routes:", "name: api-root",
		"name: key-auth", "upstreams:", "target: 10.0.0.1:8080",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("export missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "created_at") || strings.Contains(got, "id: s1") {
		t.Errorf("export must drop server-managed fields:\n%s", got)
	}
	// The unattached plugin stays at the top level rather than nested.
	if !strings.Contains(got, "\nplugins:\n    - name: correlation-id") &&
		!strings.Contains(got, "\nplugins:\n") {
		t.Errorf("global plugin should be exported at the root:\n%s", got)
	}
}

func TestImportRoundTrip(t *testing.T) {
	yaml := `
_format_version: "3.0"
services:
- name: api
  host: a.internal
  routes:
  - name: root
    paths: ["/"]
    plugins:
    - name: key-auth
  plugins:
  - name: rate-limiting
    config: { minute: 10 }
upstreams:
- name: pool
  targets:
  - target: 10.0.0.1:8080
`
	st, err := Import([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if len(st["services"]) != 1 || len(st["routes"]) != 1 || len(st["plugins"]) != 2 {
		t.Fatalf("unexpected entity counts: %v", counts(st))
	}
	if st["routes"][0].RefID("service") != st["services"][0].ID() {
		t.Fatalf("route lost its service reference")
	}
	if st["targets"][0].RefID("upstream") != st["upstreams"][0].ID() {
		t.Fatalf("target lost its upstream reference")
	}
	for _, s := range st["services"] {
		if _, ok := s["routes"]; ok {
			t.Fatalf("nested routes must be flattened out of the service")
		}
	}
}

func counts(st kong.State) map[string]int {
	out := map[string]int{}
	for k, v := range st {
		out[k] = len(v)
	}
	return out
}
