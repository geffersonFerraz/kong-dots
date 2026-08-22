package plan

import (
	"context"
	"fmt"
	"strings"

	"github.com/gefferson/kong-flow/backend/internal/kong"
)

type OpStatus string

const (
	StatusOK      OpStatus = "ok"
	StatusError   OpStatus = "error"
	StatusSkipped OpStatus = "skipped"
)

type OpResult struct {
	Index    int      `json:"index"`
	Type     OpType   `json:"type"`
	Kind     string   `json:"kind"`
	Label    string   `json:"label"`
	EntityID string   `json:"entity_id"`
	NewID    string   `json:"new_id,omitempty"`
	Status   OpStatus `json:"status"`
	Error    string   `json:"error,omitempty"`
}

type Result struct {
	Status  string            `json:"status"` // success | partial | failed
	Results []OpResult        `json:"results"`
	IDMap   map[string]string `json:"id_map"` // draft id -> real Kong id
	Error   string            `json:"error,omitempty"`
}

// ProgressFn is called before and after each operation so the UI can follow an
// apply step by step over the WebSocket.
type ProgressFn func(ev Event)

type Event struct {
	Kind   string    `json:"kind"` // "op_start" | "op_done" | "finished"
	Index  int       `json:"index"`
	Total  int       `json:"total"`
	Op     *Op       `json:"op,omitempty"`
	Result *OpResult `json:"result,omitempty"`
	Final  *Result   `json:"final,omitempty"`
}

// Apply executes the plan against Kong. It stops at the first failure and marks
// the remaining operations as skipped (no automatic rollback — the run is
// recorded in the apply history instead).
func Apply(ctx context.Context, c *kong.Client, p Plan, progress ProgressFn) Result {
	res := Result{Status: "success", Results: []OpResult{}, IDMap: map[string]string{}}
	failed := false

	for i, op := range p.Ops {
		r := OpResult{Index: i, Type: op.Type, Kind: op.Kind, Label: op.Label, EntityID: op.EntityID}
		if failed {
			r.Status = StatusSkipped
			res.Results = append(res.Results, r)
			continue
		}
		if progress != nil {
			o := op
			progress(Event{Kind: "op_start", Index: i, Total: len(p.Ops), Op: &o})
		}

		payload := resolveRefs(op.Payload, res.IDMap)
		var err error
		switch op.Type {
		case OpCreate:
			var created kong.Entity
			delete(payload, "id")
			created, err = c.Create(ctx, op.Kind, payload)
			if err == nil {
				newID := created.ID()
				r.NewID = newID
				if IsDraftID(op.EntityID) && newID != "" {
					res.IDMap[op.EntityID] = newID
				}
			}
		case OpUpdate:
			id := resolveID(op.EntityID, res.IDMap)
			_, err = c.Update(ctx, op.Kind, id, payload)
		case OpDelete:
			id := resolveID(op.EntityID, res.IDMap)
			err = c.Delete(ctx, op.Kind, id, payload)
		default:
			err = fmt.Errorf("unknown operation %q", op.Type)
		}

		if err != nil {
			r.Status, r.Error = StatusError, err.Error()
			failed = true
			res.Error = fmt.Sprintf("%s %s: %v", op.Type, op.Label, err)
			if i == 0 {
				res.Status = "failed"
			} else {
				res.Status = "partial"
			}
		} else {
			r.Status = StatusOK
		}
		res.Results = append(res.Results, r)
		if progress != nil {
			rr := r
			progress(Event{Kind: "op_done", Index: i, Total: len(p.Ops), Result: &rr})
		}
	}
	if progress != nil {
		final := res
		progress(Event{Kind: "finished", Total: len(p.Ops), Final: &final})
	}
	return res
}

func resolveID(id string, idMap map[string]string) string {
	if real, ok := idMap[id]; ok {
		return real
	}
	return id
}

// resolveRefs rewrites foreign keys that still point at draft ids to the real
// ids returned by earlier creates in the same run.
func resolveRefs(e kong.Entity, idMap map[string]string) kong.Entity {
	out := kong.Entity{}
	for k, v := range e {
		out[k] = v
	}
	for _, field := range []string{"service", "route", "consumer", "consumer_group", "upstream"} {
		v, ok := out[field]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case map[string]any:
			id, _ := t["id"].(string)
			if id == "" {
				continue
			}
			if real, ok := idMap[id]; ok {
				out[field] = map[string]any{"id": real}
			} else if IsDraftID(id) {
				// Unresolved draft reference: drop it rather than sending an
				// id Kong will reject with a confusing message.
				delete(out, field)
			} else {
				out[field] = map[string]any{"id": id}
			}
		case string:
			if real, ok := idMap[t]; ok {
				out[field] = map[string]any{"id": real}
			} else if strings.HasPrefix(t, DraftPrefix) {
				delete(out, field)
			}
		}
	}
	return out
}
