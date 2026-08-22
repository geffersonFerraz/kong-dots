package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/gefferson/kong-flow/backend/internal/plan"
	"github.com/gefferson/kong-flow/backend/internal/store"
)

// recordedRun loads a run from the history and checks it belongs to the Kong in
// the URL, so one connection's id cannot be used to read another's.
func (s *Server) recordedRun(c *gin.Context, connID string) (store.HistoryEntry, plan.Plan, plan.Result, bool) {
	var p plan.Plan
	var res plan.Result

	entry, err := s.store.GetHistoryEntry(c.Request.Context(), c.Param("historyId"))
	if err != nil || entry.ConnectionID != connID {
		fail(c, http.StatusNotFound, errors.New("no such run in this Kong's history"))
		return entry, p, res, false
	}
	if err := json.Unmarshal([]byte(entry.PlanJSON), &p); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return entry, p, res, false
	}
	if entry.ResultJSON != "" {
		if err := json.Unmarshal([]byte(entry.ResultJSON), &res); err != nil {
			fail(c, http.StatusInternalServerError, err)
			return entry, p, res, false
		}
	}
	return entry, p, res, true
}

// previewRollback answers what undoing a run would do to Kong right now. It is
// rebuilt against the live gateway on every call, so a run from last week is
// judged on today's state.
func (s *Server) previewRollback(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	entry, p, res, ok := s.recordedRun(c, conn.ID)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	current, err := client.Snapshot(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"entry": entry, "plan": plan.Rollback(p, res, current)})
}

// rollback undoes a recorded run against Kong. It is an apply like any other:
// same lock, same conflict rules, and it lands in the history itself — so a
// rollback can be rolled back.
func (s *Server) rollback(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	// Undoing writes to Kong, so it needs the same right as applying does.
	who := s.identity(c)
	if !who.Approver {
		fail(c, http.StatusForbidden, errors.New("only an approver can roll a change back on this Kong"))
		return
	}
	entry, p, res, ok := s.recordedRun(c, conn.ID)
	if !ok {
		return
	}
	var body struct {
		Force    bool   `json:"force"`
		ClientID string `json:"client_id"`
	}
	_ = decode(c, &body)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	release, locked, err := s.store.LockConnection(ctx, conn.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	if !locked {
		fail(c, http.StatusConflict, errors.New("another apply is already running against this Kong — try again in a moment"))
		return
	}
	defer release()

	current, err := client.Snapshot(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	back := plan.Rollback(p, res, current)

	if back.HasConflicts() && !body.Force {
		c.JSON(http.StatusConflict, gin.H{
			"error": "Kong changed since that run — review the conflicts before rolling it back",
			"plan":  back, "entry": entry,
		})
		return
	}
	if back.IsEmpty() {
		c.JSON(http.StatusOK, gin.H{
			"plan":   back,
			"result": plan.Result{Status: "success", Results: []plan.OpResult{}, IDMap: map[string]string{}},
			"note":   "Kong already looks the way it did before that run.",
		})
		return
	}

	s.hub.Broadcast(conn.ID, "apply_started", gin.H{"total": len(back.Ops), "plan": back})
	result := plan.Apply(ctx, client, back, func(ev plan.Event) {
		s.hub.Broadcast(conn.ID, "apply_progress", ev)
	})

	planJSON, _ := json.Marshal(back)
	resultJSON, _ := json.Marshal(result)
	_ = s.store.AddHistory(ctx, store.HistoryEntry{
		ID: uuid.NewString(), ConnectionID: conn.ID,
		// AppliedAt is left to the store, which timestamps at full precision.
		// Formatting it here would round to the second, and two runs inside the
		// same second — an apply and the rollback undoing it — would then sort
		// arbitrarily against each other in the history.
		PlanJSON: string(planJSON), ResultJSON: string(resultJSON),
		Status: result.Status, ErrorMessage: result.Error,
		Actor: who.Actor + " (rolled back " + shortID(entry.ID) + ")",
	})

	s.hub.Broadcast(conn.ID, "apply_finished", result)
	s.hub.Broadcast(conn.ID, "state_changed", gin.H{
		"by": body.ClientID, "actor": who.Actor, "summary": back.Summary,
		"status": result.Status, "rollback": true,
	})
	c.JSON(http.StatusOK, gin.H{"plan": back, "result": result, "entry": entry})
}
