// Package deck converts between the live Kong state and the declarative
// decK/`kong.yaml` format, so a graph can be versioned in Git and an existing
// file can be opened on the canvas.
package deck

import (
	"fmt"
	"sort"

	"github.com/gefferson/kong-flow/backend/internal/kong"
	"github.com/gefferson/kong-flow/backend/internal/plan"
	"gopkg.in/yaml.v3"
)

const FormatVersion = "3.0"

// dropped fields are either server-managed or represented by nesting in decK.
var dropped = map[string]bool{
	"created_at": true, "updated_at": true, "ws_id": true,
	"service": true, "route": true, "consumer": true, "consumer_group": true, "upstream": true,
}

// keyOrder puts the identifying fields first so the YAML reads naturally.
var keyOrder = []string{"name", "username", "target", "host", "port", "protocol", "path",
	"paths", "hosts", "methods", "weight", "enabled", "config"}

// Export renders a Kong state snapshot as a decK document.
func Export(state kong.State, includeIDs bool) ([]byte, error) {
	doc := &yaml.Node{Kind: yaml.MappingNode}
	appendKV(doc, "_format_version", scalar(FormatVersion))

	routesByService := map[string][]kong.Entity{}
	var globalRoutes []kong.Entity
	for _, r := range state["routes"] {
		if sid := r.RefID("service"); sid != "" {
			routesByService[sid] = append(routesByService[sid], r)
		} else {
			globalRoutes = append(globalRoutes, r)
		}
	}
	pluginsBy := map[string][]kong.Entity{}
	var globalPlugins []kong.Entity
	for _, p := range state["plugins"] {
		switch {
		case p.RefID("service") != "":
			pluginsBy["service:"+p.RefID("service")] = append(pluginsBy["service:"+p.RefID("service")], p)
		case p.RefID("route") != "":
			pluginsBy["route:"+p.RefID("route")] = append(pluginsBy["route:"+p.RefID("route")], p)
		case p.RefID("consumer") != "":
			pluginsBy["consumer:"+p.RefID("consumer")] = append(pluginsBy["consumer:"+p.RefID("consumer")], p)
		default:
			globalPlugins = append(globalPlugins, p)
		}
	}
	targetsByUpstream := map[string][]kong.Entity{}
	for _, t := range state["targets"] {
		up := t.RefID("upstream")
		targetsByUpstream[up] = append(targetsByUpstream[up], t)
	}

	if svcs := state["services"]; len(svcs) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, s := range sorted(svcs) {
			n := entityNode(s, includeIDs)
			if rs := routesByService[s.ID()]; len(rs) > 0 {
				rseq := &yaml.Node{Kind: yaml.SequenceNode}
				for _, r := range sorted(rs) {
					rn := entityNode(r, includeIDs)
					attachPlugins(rn, pluginsBy["route:"+r.ID()], includeIDs)
					rseq.Content = append(rseq.Content, rn)
				}
				appendKV(n, "routes", rseq)
			}
			attachPlugins(n, pluginsBy["service:"+s.ID()], includeIDs)
			seq.Content = append(seq.Content, n)
		}
		appendKV(doc, "services", seq)
	}

	if len(globalRoutes) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, r := range sorted(globalRoutes) {
			rn := entityNode(r, includeIDs)
			attachPlugins(rn, pluginsBy["route:"+r.ID()], includeIDs)
			seq.Content = append(seq.Content, rn)
		}
		appendKV(doc, "routes", seq)
	}

	if cs := state["consumers"]; len(cs) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, c := range sorted(cs) {
			n := entityNode(c, includeIDs)
			attachPlugins(n, pluginsBy["consumer:"+c.ID()], includeIDs)
			seq.Content = append(seq.Content, n)
		}
		appendKV(doc, "consumers", seq)
	}

	if ups := state["upstreams"]; len(ups) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, u := range sorted(ups) {
			n := entityNode(u, includeIDs)
			if ts := targetsByUpstream[u.ID()]; len(ts) > 0 {
				tseq := &yaml.Node{Kind: yaml.SequenceNode}
				for _, t := range sorted(ts) {
					tseq.Content = append(tseq.Content, entityNode(t, includeIDs))
				}
				appendKV(n, "targets", tseq)
			}
			seq.Content = append(seq.Content, n)
		}
		appendKV(doc, "upstreams", seq)
	}

	if len(globalPlugins) > 0 {
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, p := range sorted(globalPlugins) {
			seq.Content = append(seq.Content, entityNode(p, includeIDs))
		}
		appendKV(doc, "plugins", seq)
	}

	return yaml.Marshal(doc)
}

func attachPlugins(parent *yaml.Node, plugins []kong.Entity, includeIDs bool) {
	if len(plugins) == 0 {
		return
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode}
	for _, p := range sorted(plugins) {
		seq.Content = append(seq.Content, entityNode(p, includeIDs))
	}
	appendKV(parent, "plugins", seq)
}

func sorted(items []kong.Entity) []kong.Entity {
	out := append([]kong.Entity(nil), items...)
	sort.SliceStable(out, func(i, j int) bool { return sortKey(out[i]) < sortKey(out[j]) })
	return out
}

func sortKey(e kong.Entity) string {
	for _, k := range []string{"name", "username", "target", "id"} {
		if v := e.Str(k); v != "" {
			return v
		}
	}
	return ""
}

func entityNode(e kong.Entity, includeIDs bool) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode}
	keys := make([]string, 0, len(e))
	for k, v := range e {
		if dropped[k] || v == nil {
			continue
		}
		if k == "id" && !includeIDs {
			continue
		}
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		pi, pj := prio(keys[i]), prio(keys[j])
		if pi != pj {
			return pi < pj
		}
		return keys[i] < keys[j]
	})
	for _, k := range keys {
		var vn yaml.Node
		if err := vn.Encode(e[k]); err != nil {
			continue
		}
		appendKV(n, k, &vn)
	}
	return n
}

func prio(k string) int {
	for i, o := range keyOrder {
		if o == k {
			return i
		}
	}
	return len(keyOrder) + 1
}

func appendKV(m *yaml.Node, key string, val *yaml.Node) {
	m.Content = append(m.Content, scalar(key), val)
}

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

// ---------------------------------------------------------------------- import

type rawDoc struct {
	FormatVersion string           `yaml:"_format_version"`
	Services      []map[string]any `yaml:"services"`
	Routes        []map[string]any `yaml:"routes"`
	Consumers     []map[string]any `yaml:"consumers"`
	Upstreams     []map[string]any `yaml:"upstreams"`
	Plugins       []map[string]any `yaml:"plugins"`
}

// Import parses a decK document into a flat state. Entities without an id get a
// draft id so the canvas treats them as pending creations.
func Import(data []byte) (kong.State, error) {
	var doc rawDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	st := kong.State{}
	for _, k := range kong.Kinds {
		st[k] = []kong.Entity{}
	}
	seq := 0
	newID := func(kind string, m map[string]any) string {
		if id, ok := m["id"].(string); ok && id != "" {
			return id
		}
		seq++
		return fmt.Sprintf("%s%s-%d", plan.DraftPrefix, kind, seq)
	}

	addPlugins := func(parentKind, parentID string, list any) {
		items, ok := list.([]any)
		if !ok {
			return
		}
		for _, raw := range items {
			m, ok := toMap(raw)
			if !ok {
				continue
			}
			p := kong.Entity(m)
			p["id"] = newID("plugin", m)
			if parentKind != "" {
				p[parentKind] = map[string]any{"id": parentID}
			}
			st["plugins"] = append(st["plugins"], p)
		}
	}

	for _, sm := range doc.Services {
		svc := kong.Entity(sm)
		sid := newID("service", sm)
		svc["id"] = sid
		routes, _ := svc["routes"]
		plugins, _ := svc["plugins"]
		delete(svc, "routes")
		delete(svc, "plugins")
		st["services"] = append(st["services"], svc)
		addPlugins("service", sid, plugins)
		if items, ok := routes.([]any); ok {
			for _, raw := range items {
				rm, ok := toMap(raw)
				if !ok {
					continue
				}
				r := kong.Entity(rm)
				rid := newID("route", rm)
				r["id"] = rid
				rp := r["plugins"]
				delete(r, "plugins")
				r["service"] = map[string]any{"id": sid}
				st["routes"] = append(st["routes"], r)
				addPlugins("route", rid, rp)
			}
		}
	}
	for _, rm := range doc.Routes {
		r := kong.Entity(rm)
		rid := newID("route", rm)
		r["id"] = rid
		rp := r["plugins"]
		delete(r, "plugins")
		st["routes"] = append(st["routes"], r)
		addPlugins("route", rid, rp)
	}
	for _, cm := range doc.Consumers {
		c := kong.Entity(cm)
		cid := newID("consumer", cm)
		c["id"] = cid
		cp := c["plugins"]
		delete(c, "plugins")
		st["consumers"] = append(st["consumers"], c)
		addPlugins("consumer", cid, cp)
	}
	for _, um := range doc.Upstreams {
		u := kong.Entity(um)
		uid := newID("upstream", um)
		u["id"] = uid
		targets := u["targets"]
		delete(u, "targets")
		st["upstreams"] = append(st["upstreams"], u)
		if items, ok := targets.([]any); ok {
			for _, raw := range items {
				tm, ok := toMap(raw)
				if !ok {
					continue
				}
				t := kong.Entity(tm)
				t["id"] = newID("target", tm)
				t["upstream"] = map[string]any{"id": uid}
				st["targets"] = append(st["targets"], t)
			}
		}
	}
	for _, pm := range doc.Plugins {
		p := kong.Entity(pm)
		p["id"] = newID("plugin", pm)
		st["plugins"] = append(st["plugins"], p)
	}
	return st, nil
}

// toMap normalizes YAML mappings, which may decode as map[any]any.
func toMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[fmt.Sprint(k)] = val
		}
		return out, true
	}
	return nil, false
}
