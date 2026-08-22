// Package api exposes the REST + WebSocket surface consumed by the canvas UI.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/gefferson/kong-flow/backend/internal/cryptox"
	"github.com/gefferson/kong-flow/backend/internal/deck"
	"github.com/gefferson/kong-flow/backend/internal/kong"
	"github.com/gefferson/kong-flow/backend/internal/oauth"
	"github.com/gefferson/kong-flow/backend/internal/plan"
	"github.com/gefferson/kong-flow/backend/internal/store"
)

type Server struct {
	store  *store.Store
	cipher *cryptox.Cipher
	hub    *Hub
	// approval decides who may push a change to a real Kong; everybody else's
	// apply is filed as a change request instead.
	approval Approval
	// tokens caches one OAuth2 token per connection, so a token is minted once
	// and reused until it expires instead of per HTTP request.
	tokens *oauth.Registry
}

func NewServer(st *store.Store, c *cryptox.Cipher, hub *Hub, approval Approval) *Server {
	return &Server{store: st, cipher: c, hub: hub, approval: approval, tokens: oauth.NewRegistry()}
}

// Router builds the gin engine. When staticDir is set, anything that is not an
// API route falls back to the built SPA.
func (s *Server) Router(corsOrigins []string, staticDir string) http.Handler {
	if os.Getenv("KONGFLOW_DEBUG") == "" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Logger(), gin.Recovery())
	r.Use(cors.New(cors.Config{
		AllowOrigins: corsOrigins,
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders: []string{"Accept", "Content-Type", "Authorization"},
		MaxAge:       5 * time.Minute,
	}))

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	r.GET("/api/ws", gin.WrapF(s.hub.Handle))
	r.GET("/api/me", s.whoami)

	conns := r.Group("/api/connections")
	{
		conns.GET("", s.listConnections)
		conns.POST("", s.createConnection)
		conns.POST("/test", s.testConnection)

		one := conns.Group("/:id")
		{
			one.GET("", s.getConnection)
			one.PUT("", s.updateConnection)
			one.DELETE("", s.deleteConnection)
			one.GET("/status", s.connectionStatus)
			one.GET("/state", s.getState)
			one.PUT("/layout", s.saveLayout)
			// One passthrough covers both /schemas/plugins/{name} and
			// /schemas/{entity}, which gin cannot express as sibling routes.
			one.GET("/schemas/*path", s.schema)
			one.POST("/plan", s.buildPlan)
			one.POST("/apply", s.apply)
			one.GET("/requests", s.listRequests)
			one.POST("/requests", s.submitRequest)
			one.GET("/requests/:reqId", s.getRequest)
			one.POST("/requests/:reqId/approve", s.approveRequest)
			one.POST("/requests/:reqId/reject", s.rejectRequest)
			one.POST("/requests/:reqId/withdraw", s.withdrawRequest)
			one.GET("/export", s.export)
			one.POST("/import", s.importDeck)
			one.GET("/history", s.history)
			one.GET("/history/:historyId/rollback", s.previewRollback)
			one.POST("/history/:historyId/rollback", s.rollback)
		}
	}

	if staticDir != "" {
		r.NoRoute(staticHandler(staticDir))
	}
	return r
}

// staticHandler serves the SPA, falling back to index.html so client-side
// routing works. API paths never fall through to it.
func staticHandler(dir string) gin.HandlerFunc {
	fs := http.FileServer(http.Dir(dir))
	index := filepath.Join(dir, "index.html")
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if strings.HasPrefix(p, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "no such endpoint"})
			return
		}
		if info, err := os.Stat(filepath.Join(dir, filepath.Clean(p))); err != nil || info.IsDir() {
			c.File(index)
			return
		}
		fs.ServeHTTP(c.Writer, c.Request)
	}
}

// ------------------------------------------------------------------ helpers

func fail(c *gin.Context, status int, err error) {
	msg := "unexpected error"
	if err != nil {
		msg = err.Error()
	}
	var apiErr *kong.APIError
	if errors.As(err, &apiErr) {
		c.JSON(status, gin.H{"error": msg, "kong_status": apiErr.Status, "kong_body": apiErr.Body})
		return
	}
	c.JSON(status, gin.H{"error": msg})
}

func decode(c *gin.Context, v any) error {
	defer c.Request.Body.Close()
	return json.NewDecoder(io.LimitReader(c.Request.Body, 32<<20)).Decode(v)
}

// connectionView hides the encrypted secret from API responses and reports
// only whether one is stored.
type connectionView struct {
	store.Connection
	AuthSecret        string `json:"auth_secret,omitempty"`
	OAuthClientSecret string `json:"oauth_client_secret,omitempty"`
	HasSecret         bool   `json:"has_secret"`
	HasOAuthSecret    bool   `json:"has_oauth_secret"`
}

func viewWithSecretFlag(c store.Connection) connectionView {
	has := c.AuthSecret != ""
	hasOAuth := c.OAuthClientSecret != ""
	c.AuthSecret, c.OAuthClientSecret = "", ""
	return connectionView{Connection: c, HasSecret: has, HasOAuthSecret: hasOAuth}
}

// client builds a Kong client for a stored connection, decrypting its secret.
func (s *Server) client(ctx context.Context, id string) (*kong.Client, store.Connection, error) {
	conn, err := s.store.GetConnection(ctx, id)
	if err != nil {
		return nil, conn, err
	}
	secret, err := s.cipher.Decrypt(conn.AuthSecret)
	if err != nil {
		return nil, conn, fmt.Errorf("could not decrypt stored credential: %w", err)
	}
	cfg := kong.Config{
		AdminURL:      conn.AdminAPIURL,
		AuthType:      kong.AuthType(conn.AuthType),
		Secret:        secret,
		AuthHeader:    conn.AuthHeader,
		Workspace:     conn.Workspace,
		TLSSkipVerify: conn.TLSSkipVerify,
	}
	if cfg.AuthType == kong.AuthOAuth2 {
		clientSecret, err := s.cipher.Decrypt(conn.OAuthClientSecret)
		if err != nil {
			return nil, conn, fmt.Errorf("could not decrypt stored client secret: %w", err)
		}
		cfg.Tokens = s.tokens.Source(conn.ID, oauth.Config{
			TokenURL:      conn.OAuthTokenURL,
			ClientID:      conn.OAuthClientID,
			ClientSecret:  clientSecret,
			TLSSkipVerify: conn.TLSSkipVerify,
		})
	}
	return kong.New(cfg), conn, nil
}

func (s *Server) resolve(c *gin.Context) (*kong.Client, store.Connection, bool) {
	client, conn, err := s.client(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, errors.New("connection not found"))
		return nil, conn, false
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return nil, conn, false
	}
	return client, conn, true
}

// -------------------------------------------------------------- connections

type connectionReq struct {
	Name        string  `json:"name"`
	AdminAPIURL string  `json:"admin_api_url"`
	BaseURL     string  `json:"base_url"`
	AuthType    string  `json:"auth_type"`
	AuthSecret  *string `json:"auth_secret"`
	AuthHeader  string  `json:"auth_header"`
	// OAuth2 client-credentials. A nil secret means "keep the stored one".
	OAuthTokenURL     string  `json:"oauth_token_url"`
	OAuthClientID     string  `json:"oauth_client_id"`
	OAuthClientSecret *string `json:"oauth_client_secret"`
	Workspace         string  `json:"workspace"`
	Environment       string  `json:"environment"`
	Tags              string  `json:"tags"`
	TLSSkipVerify     bool    `json:"tls_skip_verify"`
}

func (req connectionReq) validate() error {
	if strings.TrimSpace(req.Name) == "" {
		return errors.New("name is required")
	}
	u := strings.TrimSpace(req.AdminAPIURL)
	if u == "" {
		return errors.New("admin_api_url is required")
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return errors.New("admin_api_url must start with http:// or https://")
	}
	// The proxy base URL is optional, but a typo there would silently produce
	// broken Route URLs, so it is checked when present.
	if b := strings.TrimSpace(req.BaseURL); b != "" &&
		!strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
		return errors.New("base_url must start with http:// or https://")
	}
	switch req.AuthType {
	case "", "none", "key", "rbac", "bearer", "basic":
	case "oauth2":
		if strings.TrimSpace(req.OAuthTokenURL) == "" {
			return errors.New("oauth_token_url is required for OAuth2")
		}
		if !strings.HasPrefix(req.OAuthTokenURL, "http://") && !strings.HasPrefix(req.OAuthTokenURL, "https://") {
			return errors.New("oauth_token_url must start with http:// or https://")
		}
		if strings.TrimSpace(req.OAuthClientID) == "" {
			return errors.New("oauth_client_id is required for OAuth2")
		}
	default:
		return fmt.Errorf("unsupported auth_type %q", req.AuthType)
	}
	return nil
}

func (req connectionReq) toConnection(id, secret, oauthSecret string) store.Connection {
	return store.Connection{
		ID: id, Name: strings.TrimSpace(req.Name),
		AdminAPIURL: strings.TrimRight(strings.TrimSpace(req.AdminAPIURL), "/"),
		BaseURL:     strings.TrimRight(strings.TrimSpace(req.BaseURL), "/"),
		AuthType:    defaultStr(req.AuthType, "none"), AuthSecret: secret,
		AuthHeader:    req.AuthHeader,
		OAuthTokenURL: strings.TrimSpace(req.OAuthTokenURL),
		OAuthClientID: strings.TrimSpace(req.OAuthClientID), OAuthClientSecret: oauthSecret,
		Workspace:   req.Workspace,
		Environment: defaultStr(req.Environment, "dev"), Tags: req.Tags,
		TLSSkipVerify: req.TLSSkipVerify,
	}
}

func (s *Server) listConnections(c *gin.Context) {
	conns, err := s.store.ListConnections(c.Request.Context())
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	out := make([]connectionView, 0, len(conns))
	for _, conn := range conns {
		out = append(out, viewWithSecretFlag(conn))
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) getConnection(c *gin.Context) {
	conn, err := s.store.GetConnection(c.Request.Context(), c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, errors.New("connection not found"))
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, viewWithSecretFlag(conn))
}

func (s *Server) createConnection(c *gin.Context) {
	var req connectionReq
	if err := decode(c, &req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	secret, oauthSecret, err := s.encryptSecrets(req, store.Connection{})
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	conn := req.toConnection(uuid.NewString(), secret, oauthSecret)
	if err := s.store.CreateConnection(c.Request.Context(), conn); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	stored, _ := s.store.GetConnection(c.Request.Context(), conn.ID)
	c.JSON(http.StatusCreated, viewWithSecretFlag(stored))
}

func (s *Server) updateConnection(c *gin.Context) {
	id := c.Param("id")
	existing, err := s.store.GetConnection(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, errors.New("connection not found"))
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	var req connectionReq
	if err := decode(c, &req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if err := req.validate(); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	secret, oauthSecret, err := s.encryptSecrets(req, existing)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	if err := s.store.UpdateConnection(c.Request.Context(), req.toConnection(id, secret, oauthSecret)); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	stored, _ := s.store.GetConnection(c.Request.Context(), id)
	c.JSON(http.StatusOK, viewWithSecretFlag(stored))
}

// encryptSecrets encrypts whichever credentials the request carries. A nil
// field means "keep whatever is already stored", so the UI never has to send a
// secret back just to rename a connection.
func (s *Server) encryptSecrets(req connectionReq, existing store.Connection) (string, string, error) {
	secret, oauthSecret := existing.AuthSecret, existing.OAuthClientSecret
	if req.AuthSecret != nil {
		enc, err := s.cipher.Encrypt(*req.AuthSecret)
		if err != nil {
			return "", "", err
		}
		secret = enc
	}
	if req.OAuthClientSecret != nil {
		enc, err := s.cipher.Encrypt(*req.OAuthClientSecret)
		if err != nil {
			return "", "", err
		}
		oauthSecret = enc
	}
	return secret, oauthSecret, nil
}

func (s *Server) deleteConnection(c *gin.Context) {
	id := c.Param("id")
	s.tokens.Forget(id)
	err := s.store.DeleteConnection(c.Request.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(c, http.StatusNotFound, errors.New("connection not found"))
		return
	}
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// testConnection probes an Admin API before the connection is saved. When an
// existing id is given, the stored credential is reused unless a new one is
// supplied.
func (s *Server) testConnection(c *gin.Context) {
	var req struct {
		connectionReq
		ID string `json:"id"`
	}
	if err := decode(c, &req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	// Secrets the form did not resend fall back to what is already stored, so
	// an existing connection can be re-tested without retyping them.
	var stored store.Connection
	if req.ID != "" {
		stored, _ = s.store.GetConnection(c.Request.Context(), req.ID)
	}
	secret := s.secretOrStored(req.AuthSecret, stored.AuthSecret)
	clientSecret := s.secretOrStored(req.OAuthClientSecret, stored.OAuthClientSecret)

	cfg := kong.Config{
		AdminURL: strings.TrimRight(strings.TrimSpace(req.AdminAPIURL), "/"),
		AuthType: kong.AuthType(defaultStr(req.AuthType, "none")), Secret: secret,
		AuthHeader: req.AuthHeader, Workspace: req.Workspace,
		TLSSkipVerify: req.TLSSkipVerify, Timeout: 10 * time.Second,
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	// With OAuth2 the token is fetched first: a failure there is a credential
	// problem, not a gateway problem, and the two deserve different messages.
	var tokenInfo gin.H
	if cfg.AuthType == kong.AuthOAuth2 {
		source := oauth.NewTokenSource(oauth.Config{
			TokenURL:      strings.TrimSpace(req.OAuthTokenURL),
			ClientID:      strings.TrimSpace(req.OAuthClientID),
			ClientSecret:  clientSecret,
			TLSSkipVerify: req.TLSSkipVerify,
			Timeout:       10 * time.Second,
		})
		tok, err := source.Token(ctx)
		if err != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "stage": "oauth", "error": err.Error()})
			return
		}
		cfg.Tokens = source
		tokenInfo = gin.H{
			"token_type": tok.Kind(),
			"expires_in": tok.ExpiresIn,
			"scope":      tok.Scope,
			"expires_at": tok.ExpiresAt.UTC().Format(time.RFC3339),
		}
	}

	client := kong.New(cfg)
	info, err := client.Info(ctx)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "stage": "admin_api", "error": err.Error(), "oauth": tokenInfo})
		return
	}
	workspaces, _ := client.Workspaces(ctx)
	names := []string{}
	for _, ws := range workspaces {
		if n := ws.Name(); n != "" {
			names = append(names, n)
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "info": info, "workspaces": names, "oauth": tokenInfo})
}

// secretOrStored prefers a secret typed into the form, falling back to the
// decrypted stored one.
func (s *Server) secretOrStored(supplied *string, encrypted string) string {
	if supplied != nil {
		return *supplied
	}
	if encrypted == "" {
		return ""
	}
	dec, err := s.cipher.Decrypt(encrypted)
	if err != nil {
		return ""
	}
	return dec
}

func (s *Server) connectionStatus(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	info, err := client.Info(ctx)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "info": info, "oauth": s.tokenStatus(conn)})
}

// tokenStatus exposes when the cached OAuth2 token expires, so the UI can show
// that the connection is running on a live token.
func (s *Server) tokenStatus(conn store.Connection) gin.H {
	if kong.AuthType(conn.AuthType) != kong.AuthOAuth2 {
		return nil
	}
	source := s.tokens.Source(conn.ID, oauth.Config{
		TokenURL: conn.OAuthTokenURL, ClientID: conn.OAuthClientID,
		ClientSecret: s.secretOrStored(nil, conn.OAuthClientSecret), TLSSkipVerify: conn.TLSSkipVerify,
	})
	tok := source.Peek()
	if tok == nil {
		return gin.H{"token": false}
	}
	return gin.H{
		"token": true, "scope": tok.Scope, "expires_in": tok.ExpiresIn,
		"expires_at": tok.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// ------------------------------------------------------------------- state

func (s *Server) getState(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	state, err := client.Snapshot(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	layout, err := s.store.GetLayout(ctx, conn.ID)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	info, _ := client.Info(ctx)
	c.JSON(http.StatusOK, gin.H{
		"connection": viewWithSecretFlag(conn),
		"info":       info,
		"state":      state,
		"layout":     layout,
		"fetched_at": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) saveLayout(c *gin.Context) {
	var req struct {
		Positions []store.LayoutPos `json:"positions"`
	}
	if err := decode(c, &req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SaveLayout(c.Request.Context(), c.Param("id"), req.Positions); err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"saved": len(req.Positions)})
}

// schema proxies Kong's own schema endpoints, e.g. `plugins/rate-limiting` or
// `services`, so the UI can build forms for plugins it has never heard of.
func (s *Server) schema(c *gin.Context) {
	client, _, ok := s.resolve(c)
	if !ok {
		return
	}
	path := strings.Trim(c.Param("path"), "/")
	if path == "" || strings.Contains(path, "..") {
		fail(c, http.StatusBadRequest, errors.New("invalid schema path"))
		return
	}
	schema, err := client.Schema(c.Request.Context(), path)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, schema)
}

// --------------------------------------------------------------- plan/apply

func (s *Server) buildPlan(c *gin.Context) {
	client, _, ok := s.resolve(c)
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
	current, err := client.Snapshot(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	c.JSON(http.StatusOK, plan.BuildWith(current, req.Desired, req.options()))
}

// planReq is what the canvas sends for both /plan and /apply.
type planReq struct {
	Desired kong.State `json:"desired"`
	// Baseline is the live state this canvas was loaded from. Sending it is what
	// lets two people work on the same Kong at once: without it, everything the
	// canvas has not heard of looks like something the user deleted.
	Baseline kong.State `json:"baseline"`
	Plan     *plan.Plan `json:"plan"`
	// Force applies even though entities drifted underneath the canvas.
	Force bool `json:"force"`
	// Title is the one-line description an editor gives a change request.
	Title string `json:"title"`
	// Actor and ClientID identify the editor, for the history and so the other
	// canvases can tell whose change just landed.
	Actor    string `json:"actor"`
	ClientID string `json:"client_id"`
}

func (r planReq) options() plan.Options { return plan.Options{Baseline: r.Baseline} }

func (s *Server) apply(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	var req planReq
	if err := decode(c, &req); err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	if req.Desired == nil && req.Plan == nil {
		fail(c, http.StatusBadRequest, errors.New("either desired or plan is required"))
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Minute)
	defer cancel()

	// An editor who may not touch this Kong still gets to press Apply: what it
	// does is file the change for review, and say so.
	who := s.identity(c)
	if !who.Approver {
		cr, err := s.fileRequest(ctx, client, conn, req, who.Actor)
		if err != nil {
			fail(c, http.StatusBadRequest, err)
			return
		}
		c.JSON(http.StatusAccepted, gin.H{
			"status":  "pending_approval",
			"request": cr,
			"message": "Filed for approval — nothing has been sent to Kong yet.",
		})
		return
	}

	// One apply at a time per Kong. Everything below reads the live state and
	// then writes to it, so a second apply starting in between would plan
	// against a gateway that is already being changed.
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

	// Always re-plan against the live state so an apply cannot act on a stale
	// diff produced minutes earlier.
	p := plan.Plan{}
	switch {
	case req.Desired != nil:
		current, err := client.Snapshot(ctx)
		if err != nil {
			fail(c, http.StatusBadGateway, err)
			return
		}
		p = plan.BuildWith(current, req.Desired, req.options())
	default:
		p = *req.Plan
	}

	// Somebody else changed these entities after this canvas read them. Applying
	// would quietly undo their work, so the canvas is sent back the drift and
	// has to decide: reload, or say force.
	if p.HasConflicts() && !req.Force {
		c.JSON(http.StatusConflict, gin.H{
			"error": fmt.Sprintf("%d entit%s changed in Kong since this canvas loaded", len(p.Conflicts), plural(len(p.Conflicts), "y", "ies")),
			"plan":  p,
		})
		return
	}

	if p.IsEmpty() {
		c.JSON(http.StatusOK, gin.H{
			"plan":   p,
			"result": plan.Result{Status: "success", Results: []plan.OpResult{}, IDMap: map[string]string{}},
		})
		return
	}

	s.hub.Broadcast(conn.ID, "apply_started", gin.H{"total": len(p.Ops), "plan": p})
	result := plan.Apply(ctx, client, p, func(ev plan.Event) {
		s.hub.Broadcast(conn.ID, "apply_progress", ev)
	})

	actor := defaultStr(who.Actor, defaultStr(strings.TrimSpace(req.Actor), "local"))
	planJSON, _ := json.Marshal(p)
	resultJSON, _ := json.Marshal(result)
	_ = s.store.AddHistory(ctx, store.HistoryEntry{
		ID: uuid.NewString(), ConnectionID: conn.ID,
		// AppliedAt is left to the store, which timestamps at full precision.
		// Formatting it here would round to the second, and two runs inside the
		// same second — an apply and the rollback undoing it — would then sort
		// arbitrarily against each other in the history.
		PlanJSON: string(planJSON), ResultJSON: string(resultJSON),
		Status: result.Status, ErrorMessage: result.Error, Actor: actor,
	})
	s.hub.Broadcast(conn.ID, "apply_finished", result)
	// Every other canvas on this Kong is now looking at a stale topology.
	s.hub.Broadcast(conn.ID, "state_changed", gin.H{
		"by": req.ClientID, "actor": actor, "summary": p.Summary, "status": result.Status,
	})
	c.JSON(http.StatusOK, gin.H{"plan": p, "result": result})
}

// ---------------------------------------------------------- import / export

func (s *Server) export(c *gin.Context) {
	client, conn, ok := s.resolve(c)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()
	state, err := client.Snapshot(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, err)
		return
	}
	yamlBytes, err := deck.Export(state, c.Query("include_ids") == "true")
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	filename := strings.ReplaceAll(strings.ToLower(conn.Name), " ", "-") + ".kong.yaml"
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Data(http.StatusOK, "application/yaml", yamlBytes)
}

func (s *Server) importDeck(c *gin.Context) {
	defer c.Request.Body.Close()
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 32<<20))
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	state, err := deck.Import(body)
	if err != nil {
		fail(c, http.StatusBadRequest, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"state": state})
}

func (s *Server) history(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	entries, err := s.store.ListHistory(c.Request.Context(), c.Param("id"), limit)
	if err != nil {
		fail(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, entries)
}

// plural picks the right suffix for a count, for messages the user reads.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func defaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
