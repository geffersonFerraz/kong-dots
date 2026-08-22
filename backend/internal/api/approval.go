package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/gefferson/kong-flow/backend/internal/kong"
	"github.com/gefferson/kong-flow/backend/internal/plan"
	"github.com/gefferson/kong-flow/backend/internal/store"
)

// Approval decides who may push a change to a real Kong. Everybody else can do
// everything else — read the topology, edit the canvas, build a plan — and their
// "Apply" is filed as a change request for somebody on this list to review.
//
// While it is empty the queue is off entirely and every editor applies directly,
// which is what a single-operator install wants.
type Approval struct {
	// Approvers are display names, as the browser reports them. A name is an
	// identification, not an authentication: on its own it is a convention
	// inside a trusted team, not a control against someone determined.
	Approvers []string
	// Token is a shared secret. When set, it is required to approve — and the
	// Approvers list, if also set, narrows who may use it.
	Token string
}

func (a Approval) Required() bool { return len(a.Approvers) > 0 || a.Token != "" }

// named reports whether an actor is on the approver list (or the list is open).
func (a Approval) named(actor string) bool {
	if len(a.Approvers) == 0 {
		return true
	}
	for _, n := range a.Approvers {
		if strings.EqualFold(strings.TrimSpace(n), actor) {
			return true
		}
	}
	return false
}

func (a Approval) allows(actor, token string) bool {
	if !a.Required() {
		return true
	}
	if a.Token != "" {
		ok := subtle.ConstantTimeCompare([]byte(a.Token), []byte(token)) == 1
		return ok && a.named(actor)
	}
	return a.named(actor)
}

// Identity is who a request claims to come from. Until this tool grows real
// authentication, the actor is whatever name the browser sends — see Approval
// for what that is and is not worth.
type Identity struct {
	Actor    string `json:"actor"`
	Approver bool   `json:"approver"`
}

const (
	headerActor = "X-KongFlow-Actor"
	headerToken = "X-KongFlow-Approval-Token"
)

func (s *Server) identity(c *gin.Context) Identity {
	actor := strings.TrimSpace(c.GetHeader(headerActor))
	if actor == "" {
		actor = "anonymous"
	}
	return Identity{Actor: actor, Approver: s.approval.allows(actor, c.GetHeader(headerToken))}
}

// whoami tells the UI which buttons to show: an editor gets "Request approval"
// where an approver gets "Apply".
func (s *Server) whoami(c *gin.Context) {
	id := s.identity(c)
	c.JSON(http.StatusOK, gin.H{
		"actor":             id.Actor,
		"approver":          id.Approver,
		"approval_required": s.approval.Required(),
	})
}

// ------------------------------------------------------------- submit / list

// fileRequest stores a canvas as a change waiting for review. It is also what
// an editor's "Apply" turns into, so the button never simply fails.
func (s *Server) fileRequest(ctx context.Context, client *kong.Client, conn store.Connection, req planReq, actor string) (store.ChangeRequest, error) {
	if req.Desired == nil {
		return store.ChangeRequest{}, errors.New("desired is required")
	}
	// A preview of what was intended, recorded with the request. What actually
	// runs is re-planned at approval time against the gateway as it is then.
	preview := plan.Plan{}
	if current, err := client.Snapshot(ctx); err == nil {
		preview = plan.BuildWith(current, req.Desired, req.options())
	}

	desiredJSON, err := json.Marshal(req.Desired)
	if err != nil {
		return store.ChangeRequest{}, err
	}
	baselineJSON, err := json.Marshal(req.Baseline)
	if err != nil {
		return store.ChangeRequest{}, err
	}
	planJSON, _ := json.Marshal(preview)

	cr := store.ChangeRequest{
		ID:           uuid.NewString(),
		ConnectionID: conn.ID,
		Title:        strings.TrimSpace(req.Title),
		Status:       store.RequestPending,
		DesiredJSON:  string(desiredJSON),
		BaselineJSON: string(baselineJSON),
		PlanJSON:     string(planJSON),
		RequestedBy:  actor,
		RequestedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.store.CreateChangeRequest(ctx, cr); err != nil {
		return store.ChangeRequest{}, err
	}
	cr.Summary = summaryMap(preview.Summary)
	s.hub.Broadcast(conn.ID, "request_submitted", gin.H{
		"request": cr, "by": req.ClientID, "actor": actor,
	})
	return cr, nil
}

func (s *Server) submitRequest(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	var req planReq
	if err := decode(c, &req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	cr, err := s.fileRequest(ctx, client, conn, req, s.identity(c).Actor)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"request": cr})
}

func (s *Server) listRequests(c *gin.Context) {
	_, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	list, err := s.store.ListChangeRequests(c.Request.Context(), conn.ID, c.Query("status"), 100)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	for i := range list {
		list[i].Summary = storedSummary(list[i].PlanJSON)
	}
	c.JSON(http.StatusOK, gin.H{"requests": list, "identity": s.identity(c)})
}

// getRequest returns a request together with a plan rebuilt against Kong right
// now. A request can sit in the queue for hours, and it is that fresh plan —
// conflicts and all — that the approver is being asked to accept.
func (s *Server) getRequest(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	cr, err := s.requestOf(c, conn.ID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	body := gin.H{"request": cr, "identity": s.identity(c), "submitted_plan": rawJSON(cr.PlanJSON)}
	if cr.ResultJSON != "" {
		body["result"] = rawJSON(cr.ResultJSON)
	}
	if cr.Status == store.RequestPending {
		desired, baseline, err := decodeStates(cr)
		if err != nil {
			fail(c, http.StatusInternalServerError, err)
			return
		}
		current, err := client.Snapshot(ctx)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		body["plan"] = plan.BuildWith(current, desired, plan.Options{Baseline: baseline})
	}
	c.JSON(http.StatusOK, body)
}

// ---------------------------------------------------------- approve / reject

func (s *Server) approveRequest(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	who := s.identity(c)
	if !who.Approver {
		fail(c, http.StatusForbidden, errors.New("only an approver can push a change to this Kong"))
		return
	}
	cr, err := s.requestOf(c, conn.ID)
	if err != nil {
		return
	}
	var body struct {
		Note     string `json:"note"`
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

	// Re-read inside the lock: another approver may have decided this one while
	// the request was in flight.
	if cr, err = s.store.GetChangeRequest(ctx, cr.ID); err != nil {
		fail(c, http.StatusNotFound, err)
		return
	}
	if cr.Status != store.RequestPending {
		fail(c, http.StatusConflict, errors.New("this request was already "+cr.Status))
		return
	}

	desired, baseline, err := decodeStates(cr)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	current, err := client.Snapshot(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	p := plan.BuildWith(current, desired, plan.Options{Baseline: baseline})
	if p.HasConflicts() && !body.Force {
		c.JSON(http.StatusConflict, gin.H{
			"error": "Kong changed since this request was written — review the conflicts before approving",
			"plan":  p, "request": cr,
		})
		return
	}

	s.hub.Broadcast(conn.ID, "apply_started", gin.H{"total": len(p.Ops), "plan": p})
	result := plan.Apply(ctx, client, p, func(ev plan.Event) {
		s.hub.Broadcast(conn.ID, "apply_progress", ev)
	})

	planJSON, _ := json.Marshal(p)
	resultJSON, _ := json.Marshal(result)
	_ = s.store.AddHistory(ctx, store.HistoryEntry{
		ID: uuid.NewString(), ConnectionID: conn.ID,
		// AppliedAt is left to the store, which timestamps at full precision.
		// Formatting it here would round to the second, and two runs inside the
		// same second — an apply and the rollback undoing it — would then sort
		// arbitrarily against each other in the history.
		PlanJSON: string(planJSON), ResultJSON: string(resultJSON),
		Status:       result.Status,
		ErrorMessage: result.Error,
		Actor:        who.Actor + " (approved " + shortID(cr.ID) + " by " + cr.RequestedBy + ")",
	})

	status := store.RequestApplied
	if result.Status != "success" {
		status = store.RequestFailed
	}
	if err := s.store.Review(ctx, cr.ID, status, who.Actor, body.Note, string(resultJSON), result.Error); err != nil {
		fail(c, http.StatusConflict, err)
		return
	}
	cr.Status, cr.ReviewedBy, cr.ReviewNote = status, who.Actor, body.Note

	s.hub.Broadcast(conn.ID, "apply_finished", result)
	s.hub.Broadcast(conn.ID, "request_reviewed", gin.H{"request": cr, "by": body.ClientID, "actor": who.Actor})
	s.hub.Broadcast(conn.ID, "state_changed", gin.H{
		"by": body.ClientID, "actor": who.Actor, "summary": p.Summary, "status": result.Status,
	})
	c.JSON(http.StatusOK, gin.H{"request": cr, "plan": p, "result": result})
}

func (s *Server) rejectRequest(c *gin.Context) {
	_, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	who := s.identity(c)
	if !who.Approver {
		fail(c, http.StatusForbidden, errors.New("only an approver can decide a change request"))
		return
	}
	s.decide(c, conn.ID, store.RequestRejected, who.Actor)
}

// withdrawRequest lets the person who filed a request take it back. An approver
// can do it too, to clear a queue somebody abandoned.
func (s *Server) withdrawRequest(c *gin.Context) {
	_, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	who := s.identity(c)
	cr, err := s.requestOf(c, conn.ID)
	if err != nil {
		return
	}
	if !who.Approver && !strings.EqualFold(cr.RequestedBy, who.Actor) {
		fail(c, http.StatusForbidden, errors.New("only the person who filed this request can withdraw it"))
		return
	}
	s.decide(c, conn.ID, store.RequestWithdrawn, who.Actor)
}

func (s *Server) decide(c *gin.Context, connID, status, actor string) {
	cr, err := s.requestOf(c, connID)
	if err != nil {
		return
	}
	var body struct {
		Note     string `json:"note"`
		ClientID string `json:"client_id"`
	}
	_ = decode(c, &body)

	if err := s.store.Review(c.Request.Context(), cr.ID, status, actor, body.Note, "", ""); err != nil {
		fail(c, http.StatusConflict, errors.New("this request is no longer pending"))
		return
	}
	cr.Status, cr.ReviewedBy, cr.ReviewNote = status, actor, body.Note
	s.hub.Broadcast(connID, "request_reviewed", gin.H{"request": cr, "by": body.ClientID, "actor": actor})
	c.JSON(http.StatusOK, gin.H{"request": cr})
}

// ------------------------------------------------------------------ helpers

// requestOf loads the request named in the URL and checks it belongs to the
// connection in the URL, so one Kong's id cannot be used to read another's.
func (s *Server) requestOf(c *gin.Context, connID string) (store.ChangeRequest, error) {
	cr, err := s.store.GetChangeRequest(c.Request.Context(), c.Param("reqId"))
	if err != nil {
		fail(c, http.StatusNotFound, errors.New("no such change request"))
		return cr, err
	}
	if cr.ConnectionID != connID {
		err = errors.New("no such change request")
		fail(c, http.StatusNotFound, err)
		return cr, err
	}
	return cr, nil
}

func decodeStates(cr store.ChangeRequest) (desired, baseline kong.State, err error) {
	if err = json.Unmarshal([]byte(cr.DesiredJSON), &desired); err != nil {
		return nil, nil, err
	}
	if cr.BaselineJSON != "" && cr.BaselineJSON != "null" {
		if err = json.Unmarshal([]byte(cr.BaselineJSON), &baseline); err != nil {
			return nil, nil, err
		}
	}
	return desired, baseline, nil
}

func summaryMap(s plan.Summary) map[string]int {
	return map[string]int{"create": s.Create, "update": s.Update, "delete": s.Delete}
}

// storedSummary reads the op counts back out of a recorded plan, for list rows.
func storedSummary(planJSON string) map[string]int {
	if planJSON == "" {
		return nil
	}
	var p plan.Plan
	if json.Unmarshal([]byte(planJSON), &p) != nil {
		return nil
	}
	return summaryMap(p.Summary)
}

// rawJSON passes a stored blob straight through without re-encoding it.
func rawJSON(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
