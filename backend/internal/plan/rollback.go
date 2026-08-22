package plan

import "github.com/gefferson/kong-flow/backend/internal/kong"

// Rollback builds the plan that undoes an applied one: what was created is
// deleted, what was updated goes back to the values it had, what was deleted is
// recreated with the id it used to hold.
//
// Only operations Kong actually accepted are inverted — a run that failed
// halfway leaves the rest untouched, and those were never applied to begin with.
//
// current is the gateway as it is now, which is what makes this safe to offer
// hours later: an entity that somebody has changed since is reported as a
// conflict instead of being quietly reverted along with everything else.
func Rollback(applied Plan, res Result, current kong.State) Plan {
	out := Plan{Ops: []Op{}}

	byID := map[string]map[string]kong.Entity{}
	for kind, entities := range current {
		byID[kind] = map[string]kong.Entity{}
		for _, e := range entities {
			byID[kind][e.ID()] = e
		}
	}
	live := func(kind, id string) (kong.Entity, bool) {
		e, ok := byID[kind][id]
		return e, ok
	}
	conflict := func(op Op, id, reason string, changes []FieldChange) {
		out.Conflicts = append(out.Conflicts, Conflict{
			Kind: op.Kind, EntityID: id, Label: op.Label, Op: op.Type, Reason: reason, Changes: changes,
		})
	}

	for i, op := range applied.Ops {
		// The result rows line up with the ops by index; anything that errored
		// or was skipped never reached Kong, so there is nothing to undo.
		if i >= len(res.Results) || res.Results[i].Status != StatusOK {
			continue
		}
		// A created entity lives under the id Kong assigned, not the draft one.
		id := op.EntityID
		if res.Results[i].NewID != "" {
			id = res.Results[i].NewID
		}

		switch op.Type {
		case OpCreate:
			cur, ok := live(op.Kind, id)
			if !ok {
				continue // already gone; nothing to undo
			}
			if drift := diffFields(op.After, cur); len(drift) > 0 {
				conflict(op, id, ReasonChanged, drift)
			}
			out.Ops = append(out.Ops, Op{
				Type: OpDelete, Kind: op.Kind, EntityID: id,
				Label: op.Label, Before: cur, Payload: cur,
			})

		case OpUpdate:
			cur, ok := live(op.Kind, id)
			if !ok {
				conflict(op, id, ReasonDeleted, nil)
				continue
			}
			if drift := diffFields(op.After, cur); len(drift) > 0 {
				conflict(op, id, ReasonChanged, drift)
			}
			// Only the fields this operation touched go back, so a rollback
			// never reaches beyond what it originally changed.
			payload := kong.Entity{}
			changes := make([]FieldChange, 0, len(op.Changes))
			for _, ch := range op.Changes {
				payload[ch.Field] = ch.From
				changes = append(changes, FieldChange{Field: ch.Field, From: cur[ch.Field], To: ch.From})
			}
			if len(payload) == 0 {
				continue
			}
			if op.Kind == "targets" {
				// Targets are immutable in Kong: they are replaced, not patched.
				payload = sanitize(op.Before)
			}
			out.Ops = append(out.Ops, Op{
				Type: OpUpdate, Kind: op.Kind, EntityID: id, Label: op.Label,
				Before: cur, After: op.Before, Payload: payload, Changes: changes,
			})

		case OpDelete:
			if cur, ok := live(op.Kind, id); ok {
				// Something is back under that id already — recreating would
				// collide, and whatever is there is not this plan's to remove.
				conflict(op, id, ReasonChanged, diffFields(op.Before, cur))
				continue
			}
			out.Ops = append(out.Ops, Op{
				Type: OpCreate, Kind: op.Kind, EntityID: id,
				Label: op.Label, After: op.Before, Payload: sanitize(op.Before),
			})
		}
	}

	sortOps(out.Ops)
	for _, op := range out.Ops {
		switch op.Type {
		case OpCreate:
			out.Summary.Create++
		case OpUpdate:
			out.Summary.Update++
		case OpDelete:
			out.Summary.Delete++
		}
	}
	return out
}
