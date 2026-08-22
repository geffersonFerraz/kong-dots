// Package store owns the tool's own persistence (PostgreSQL): registered Kong
// connections, canvas node positions and apply history. Kong entities
// themselves are never duplicated here — they are read live from the Admin API.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS kong_connections (
  id                    TEXT PRIMARY KEY,
  name                  TEXT NOT NULL,
  admin_api_url         TEXT NOT NULL,
  base_url              TEXT NOT NULL DEFAULT '',
  auth_type             TEXT NOT NULL DEFAULT 'none',
  auth_secret_encrypted TEXT NOT NULL DEFAULT '',
  auth_header           TEXT NOT NULL DEFAULT '',
  oauth_token_url       TEXT NOT NULL DEFAULT '',
  oauth_client_id       TEXT NOT NULL DEFAULT '',
  oauth_client_secret_encrypted TEXT NOT NULL DEFAULT '',
  workspace             TEXT NOT NULL DEFAULT '',
  environment           TEXT NOT NULL DEFAULT 'dev',
  tags                  TEXT NOT NULL DEFAULT '',
  tls_skip_verify       BOOLEAN NOT NULL DEFAULT FALSE,
  created_at            TIMESTAMPTZ NOT NULL,
  updated_at            TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS canvas_layout (
  connection_id TEXT NOT NULL,
  entity_type   TEXT NOT NULL,
  entity_id     TEXT NOT NULL,
  pos_x         DOUBLE PRECISION NOT NULL,
  pos_y         DOUBLE PRECISION NOT NULL,
  PRIMARY KEY (connection_id, entity_type, entity_id)
);

CREATE TABLE IF NOT EXISTS apply_history (
  id            TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL,
  applied_at    TIMESTAMPTZ NOT NULL,
  plan_json     TEXT NOT NULL,
  result_json   TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT '',
  actor         TEXT NOT NULL DEFAULT 'local'
);
CREATE INDEX IF NOT EXISTS idx_history_conn ON apply_history(connection_id, applied_at DESC);

-- A change somebody wants made to a Kong but is not allowed to make. It holds
-- the desired canvas state and the state that canvas was built on, so an
-- approver can re-plan it against a gateway that has moved on since.
CREATE TABLE IF NOT EXISTS change_requests (
  id            TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL,
  title         TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'pending',
  desired_json  TEXT NOT NULL,
  baseline_json TEXT NOT NULL DEFAULT '',
  plan_json     TEXT NOT NULL DEFAULT '',
  result_json   TEXT NOT NULL DEFAULT '',
  requested_by  TEXT NOT NULL DEFAULT '',
  requested_at  TIMESTAMPTZ NOT NULL,
  reviewed_by   TEXT NOT NULL DEFAULT '',
  reviewed_at   TIMESTAMPTZ,
  review_note   TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_requests_conn ON change_requests(connection_id, requested_at DESC);
CREATE INDEX IF NOT EXISTS idx_requests_pending ON change_requests(connection_id) WHERE status = 'pending';
`

// Open connects to PostgreSQL and brings the schema up to date. dsn is a
// libpq/pgx connection string, e.g.
// postgres://kongflow:secret@db:5432/kongflow?sslmode=disable.
//
// The database is often started alongside this process (compose, k8s), so the
// first connection is retried for a while instead of failing the boot.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)

	if err := waitReady(db, 30*time.Second); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func waitReady(db *sql.DB, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	var err error
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("database not reachable after %s: %w", limit, err)
		}
		time.Sleep(time.Second)
	}
}

// migrate adds columns introduced after a database was first created. Fresh
// databases already get them from schema above; this is for the ones that are
// not fresh.
func migrate(db *sql.DB) error {
	added := []struct{ name, decl string }{
		{"base_url", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_token_url", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_client_id", "TEXT NOT NULL DEFAULT ''"},
		{"oauth_client_secret_encrypted", "TEXT NOT NULL DEFAULT ''"},
	}
	for _, col := range added {
		if _, err := db.Exec(`ALTER TABLE kong_connections ADD COLUMN IF NOT EXISTS ` + col.name + ` ` + col.decl); err != nil {
			return fmt.Errorf("add column %s: %w", col.name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Redact strips the password from a connection string so it can be logged.
func Redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), "xxxxx")
	}
	return u.String()
}

// ---------------------------------------------------------------- connections

type Connection struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AdminAPIURL string `json:"admin_api_url"`
	// BaseURL is where this Kong serves traffic (its proxy), as opposed to where
	// it is administered. Nothing calls it; the UI uses it to build Route URLs.
	BaseURL    string `json:"base_url,omitempty"`
	AuthType   string `json:"auth_type"` // none | key | rbac | basic | bearer | oauth2
	AuthSecret string `json:"auth_secret,omitempty"`
	AuthHeader string `json:"auth_header,omitempty"`
	// OAuth2 client-credentials fields, used when AuthType is "oauth2".
	OAuthTokenURL     string `json:"oauth_token_url,omitempty"`
	OAuthClientID     string `json:"oauth_client_id,omitempty"`
	OAuthClientSecret string `json:"oauth_client_secret,omitempty"`
	Workspace         string `json:"workspace,omitempty"`
	Environment       string `json:"environment"`
	Tags              string `json:"tags,omitempty"`
	TLSSkipVerify     bool   `json:"tls_skip_verify"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

func (s *Store) ListConnections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `
	  SELECT id,name,admin_api_url,base_url,auth_type,auth_secret_encrypted,auth_header,
	         oauth_token_url,oauth_client_id,oauth_client_secret_encrypted,workspace,
	         environment,tags,tls_skip_verify,created_at,updated_at
	  FROM kong_connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type scanner interface{ Scan(dest ...any) error }

func scanConn(sc scanner) (Connection, error) {
	var c Connection
	var created, updated time.Time
	err := sc.Scan(&c.ID, &c.Name, &c.AdminAPIURL, &c.BaseURL, &c.AuthType, &c.AuthSecret, &c.AuthHeader,
		&c.OAuthTokenURL, &c.OAuthClientID, &c.OAuthClientSecret,
		&c.Workspace, &c.Environment, &c.Tags, &c.TLSSkipVerify, &created, &updated)
	c.CreatedAt, c.UpdatedAt = rfc3339(created), rfc3339(updated)
	return c, err
}

// Timestamps cross the API as RFC3339 strings; the columns are timestamptz.
func rfc3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Store) GetConnection(ctx context.Context, id string) (Connection, error) {
	row := s.db.QueryRowContext(ctx, `
	  SELECT id,name,admin_api_url,base_url,auth_type,auth_secret_encrypted,auth_header,
	         oauth_token_url,oauth_client_id,oauth_client_secret_encrypted,workspace,
	         environment,tags,tls_skip_verify,created_at,updated_at
	  FROM kong_connections WHERE id = $1`, id)
	c, err := scanConn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) CreateConnection(ctx context.Context, c Connection) error {
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
	  INSERT INTO kong_connections
	    (id,name,admin_api_url,base_url,auth_type,auth_secret_encrypted,auth_header,
	     oauth_token_url,oauth_client_id,oauth_client_secret_encrypted,workspace,
	     environment,tags,tls_skip_verify,created_at,updated_at)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		c.ID, c.Name, c.AdminAPIURL, c.BaseURL, c.AuthType, c.AuthSecret, c.AuthHeader,
		c.OAuthTokenURL, c.OAuthClientID, c.OAuthClientSecret, c.Workspace,
		c.Environment, c.Tags, c.TLSSkipVerify, now, now)
	return err
}

func (s *Store) UpdateConnection(ctx context.Context, c Connection) error {
	res, err := s.db.ExecContext(ctx, `
	  UPDATE kong_connections SET name=$1,admin_api_url=$2,base_url=$3,auth_type=$4,auth_secret_encrypted=$5,
	    auth_header=$6,oauth_token_url=$7,oauth_client_id=$8,oauth_client_secret_encrypted=$9,
	    workspace=$10,environment=$11,tags=$12,tls_skip_verify=$13,updated_at=$14
	  WHERE id=$15`,
		c.Name, c.AdminAPIURL, c.BaseURL, c.AuthType, c.AuthSecret, c.AuthHeader,
		c.OAuthTokenURL, c.OAuthClientID, c.OAuthClientSecret, c.Workspace,
		c.Environment, c.Tags, c.TLSSkipVerify, time.Now().UTC(), c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM canvas_layout WHERE connection_id=$1`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM apply_history WHERE connection_id=$1`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM kong_connections WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --------------------------------------------------------------------- layout

type LayoutPos struct {
	EntityType string  `json:"entity_type"`
	EntityID   string  `json:"entity_id"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
}

func (s *Store) GetLayout(ctx context.Context, connID string) (map[string]LayoutPos, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_type,entity_id,pos_x,pos_y FROM canvas_layout WHERE connection_id=$1`, connID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LayoutPos{}
	for rows.Next() {
		var p LayoutPos
		if err := rows.Scan(&p.EntityType, &p.EntityID, &p.X, &p.Y); err != nil {
			return nil, err
		}
		out[p.EntityType+":"+p.EntityID] = p
	}
	return out, rows.Err()
}

func (s *Store) SaveLayout(ctx context.Context, connID string, positions []LayoutPos) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
	  INSERT INTO canvas_layout (connection_id,entity_type,entity_id,pos_x,pos_y)
	  VALUES ($1,$2,$3,$4,$5)
	  ON CONFLICT(connection_id,entity_type,entity_id) DO UPDATE SET pos_x=excluded.pos_x, pos_y=excluded.pos_y`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range positions {
		if p.EntityID == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, connID, p.EntityType, p.EntityID, p.X, p.Y); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// -------------------------------------------------------------------- history

type HistoryEntry struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	AppliedAt    string `json:"applied_at"`
	PlanJSON     string `json:"plan_json"`
	ResultJSON   string `json:"result_json"`
	Status       string `json:"status"` // success | partial | failed
	ErrorMessage string `json:"error_message,omitempty"`
	Actor        string `json:"actor"`
}

// parseTime reads an RFC3339 timestamp coming from the API, defaulting to now —
// at full precision — when the caller did not send one. Callers that care about
// ordering should leave it empty rather than formatting a second-resolution
// string themselves.
func parseTime(v string) (time.Time, error) {
	if v == "" {
		return time.Now().UTC(), nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q: %w", v, err)
	}
	return t, nil
}

func (s *Store) AddHistory(ctx context.Context, h HistoryEntry) error {
	appliedAt, err := parseTime(h.AppliedAt)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
	  INSERT INTO apply_history (id,connection_id,applied_at,plan_json,result_json,status,error_message,actor)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		h.ID, h.ConnectionID, appliedAt, h.PlanJSON, h.ResultJSON, h.Status, h.ErrorMessage, h.Actor)
	return err
}

func (s *Store) ListHistory(ctx context.Context, connID string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
	  SELECT id,connection_id,applied_at,plan_json,result_json,status,error_message,actor
	  FROM apply_history WHERE connection_id=$1 ORDER BY applied_at DESC LIMIT $2`, connID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		var appliedAt time.Time
		if err := rows.Scan(&h.ID, &h.ConnectionID, &appliedAt, &h.PlanJSON, &h.ResultJSON,
			&h.Status, &h.ErrorMessage, &h.Actor); err != nil {
			return nil, err
		}
		h.AppliedAt = rfc3339(appliedAt)
		out = append(out, h)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------- locking

// applyLockKey namespaces the advisory lock so it cannot collide with any other
// use of PostgreSQL's single advisory-lock space.
func applyLockKey(connID string) string { return "kong-flow:apply:" + connID }

// LockConnection takes an advisory lock covering applies to one Kong, so two
// people hitting "Apply" at the same moment cannot interleave their operations
// — the lock lives in PostgreSQL, so it holds across replicas of this server
// too. It never waits: an apply can run for minutes, and the second caller is
// better told to retry than left hanging on a connection.
//
// The returned release must be called when the apply finishes; acquired is
// false (with a nil error) when somebody else holds the lock.
func (s *Store) LockConnection(ctx context.Context, connID string) (release func(), acquired bool, err error) {
	// The lock belongs to the session that took it, so it has to be held on one
	// pinned connection rather than borrowed from the pool per statement.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, false, err
	}
	key := applyLockKey(connID)
	var ok bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&ok); err != nil {
		conn.Close()
		return nil, false, err
	}
	if !ok {
		conn.Close()
		return nil, false, nil
	}
	return func() {
		// The request's context is usually done by now (or was cancelled, which
		// is exactly when releasing matters most), so unlock outside of it.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, key)
		conn.Close()
	}, true, nil
}

// ----------------------------------------------------------- change requests

// Change request statuses. A request leaves "pending" exactly once.
const (
	RequestPending   = "pending"
	RequestApplied   = "applied"
	RequestRejected  = "rejected"
	RequestFailed    = "failed"
	RequestWithdrawn = "withdrawn"
)

// ChangeRequest is a proposed change waiting for somebody allowed to push it to
// Kong. The heavy JSON blobs stay out of the wire format; the API layer decides
// which of them a given endpoint returns.
type ChangeRequest struct {
	ID           string `json:"id"`
	ConnectionID string `json:"connection_id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	DesiredJSON  string `json:"-"`
	BaselineJSON string `json:"-"`
	// PlanJSON is the plan as it looked when the request was made — a record of
	// intent, not what will run. Approving re-plans against the live gateway.
	PlanJSON     string `json:"-"`
	ResultJSON   string `json:"-"`
	RequestedBy  string `json:"requested_by"`
	RequestedAt  string `json:"requested_at"`
	ReviewedBy   string `json:"reviewed_by,omitempty"`
	ReviewedAt   string `json:"reviewed_at,omitempty"`
	ReviewNote   string `json:"review_note,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
	// Summary counts the operations the request asked for, so a list can be
	// rendered without shipping every plan.
	Summary map[string]int `json:"summary,omitempty"`
}

const requestCols = `id,connection_id,title,status,desired_json,baseline_json,plan_json,result_json,
	requested_by,requested_at,reviewed_by,reviewed_at,review_note,error_message`

func (s *Store) CreateChangeRequest(ctx context.Context, r ChangeRequest) error {
	requestedAt, err := parseTime(r.RequestedAt)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
	  INSERT INTO change_requests (`+requestCols+`)
	  VALUES ($1,$2,$3,$4,$5,$6,$7,'',$8,$9,'',NULL,'','')`,
		r.ID, r.ConnectionID, r.Title, defaultTo(r.Status, RequestPending),
		r.DesiredJSON, r.BaselineJSON, r.PlanJSON, r.RequestedBy, requestedAt)
	return err
}

// ListChangeRequests returns a connection's requests, newest first. status may
// be empty for all of them.
func (s *Store) ListChangeRequests(ctx context.Context, connID, status string, limit int) ([]ChangeRequest, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
	  SELECT `+requestCols+` FROM change_requests
	  WHERE connection_id=$1 AND ($2 = '' OR status=$2)
	  ORDER BY requested_at DESC LIMIT $3`, connID, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChangeRequest{}
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetChangeRequest(ctx context.Context, id string) (ChangeRequest, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+requestCols+` FROM change_requests WHERE id=$1`, id)
	r, err := scanRequest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ChangeRequest{}, ErrNotFound
	}
	return r, err
}

// CountPendingRequests powers the "N waiting for review" badge.
func (s *Store) CountPendingRequests(ctx context.Context, connID string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM change_requests WHERE connection_id=$1 AND status=$2`, connID, RequestPending).Scan(&n)
	return n, err
}

// Review moves a request out of "pending". The status guard is the whole point:
// two approvers clicking at the same moment means the second one is told the
// request was already decided instead of applying it twice.
func (s *Store) Review(ctx context.Context, id, status, reviewedBy, note, resultJSON, errMessage string) error {
	res, err := s.db.ExecContext(ctx, `
	  UPDATE change_requests
	  SET status=$2, reviewed_by=$3, reviewed_at=$4, review_note=$5, result_json=$6, error_message=$7
	  WHERE id=$1 AND status=$8`,
		id, status, reviewedBy, time.Now().UTC(), note, resultJSON, errMessage, RequestPending)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRequest(sc scanner) (ChangeRequest, error) {
	var r ChangeRequest
	var requestedAt time.Time
	var reviewedAt sql.NullTime
	err := sc.Scan(&r.ID, &r.ConnectionID, &r.Title, &r.Status, &r.DesiredJSON, &r.BaselineJSON,
		&r.PlanJSON, &r.ResultJSON, &r.RequestedBy, &requestedAt, &r.ReviewedBy, &reviewedAt,
		&r.ReviewNote, &r.ErrorMessage)
	r.RequestedAt = rfc3339(requestedAt)
	if reviewedAt.Valid {
		r.ReviewedAt = rfc3339(reviewedAt.Time)
	}
	return r, err
}

func defaultTo(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// GetHistoryEntry loads one recorded run, which is what a rollback is built
// from: the plan that was applied plus which of its operations Kong accepted.
func (s *Store) GetHistoryEntry(ctx context.Context, id string) (HistoryEntry, error) {
	row := s.db.QueryRowContext(ctx, `
	  SELECT id,connection_id,applied_at,plan_json,result_json,status,error_message,actor
	  FROM apply_history WHERE id=$1`, id)
	var h HistoryEntry
	var appliedAt time.Time
	err := row.Scan(&h.ID, &h.ConnectionID, &appliedAt, &h.PlanJSON, &h.ResultJSON,
		&h.Status, &h.ErrorMessage, &h.Actor)
	if errors.Is(err, sql.ErrNoRows) {
		return HistoryEntry{}, ErrNotFound
	}
	h.AppliedAt = rfc3339(appliedAt)
	return h, err
}
