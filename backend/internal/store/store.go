// Package store owns the tool's own persistence (SQLite): registered Kong
// connections, canvas node positions and apply history. Kong entities
// themselves are never duplicated here — they are read live from the Admin API.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
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
  tls_skip_verify       INTEGER NOT NULL DEFAULT 0,
  created_at            TEXT NOT NULL,
  updated_at            TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS canvas_layout (
  connection_id TEXT NOT NULL,
  entity_type   TEXT NOT NULL,
  entity_id     TEXT NOT NULL,
  pos_x         REAL NOT NULL,
  pos_y         REAL NOT NULL,
  PRIMARY KEY (connection_id, entity_type, entity_id)
);

CREATE TABLE IF NOT EXISTS apply_history (
  id            TEXT PRIMARY KEY,
  connection_id TEXT NOT NULL,
  applied_at    TEXT NOT NULL,
  plan_json     TEXT NOT NULL,
  result_json   TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL,
  error_message TEXT NOT NULL DEFAULT '',
  actor         TEXT NOT NULL DEFAULT 'local'
);
CREATE INDEX IF NOT EXISTS idx_history_conn ON apply_history(connection_id, applied_at DESC);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate adds columns introduced after a database was first created. SQLite
// has no "ADD COLUMN IF NOT EXISTS", so existing columns are read first.
func migrate(db *sql.DB) error {
	added := map[string]string{
		"base_url":                      "TEXT NOT NULL DEFAULT ''",
		"oauth_token_url":               "TEXT NOT NULL DEFAULT ''",
		"oauth_client_id":               "TEXT NOT NULL DEFAULT ''",
		"oauth_client_secret_encrypted": "TEXT NOT NULL DEFAULT ''",
	}
	rows, err := db.Query(`PRAGMA table_info(kong_connections)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return err
		}
		delete(added, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for name, decl := range added {
		if _, err := db.Exec(`ALTER TABLE kong_connections ADD COLUMN ` + name + ` ` + decl); err != nil {
			return fmt.Errorf("add column %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

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
	var skip int
	err := sc.Scan(&c.ID, &c.Name, &c.AdminAPIURL, &c.BaseURL, &c.AuthType, &c.AuthSecret, &c.AuthHeader,
		&c.OAuthTokenURL, &c.OAuthClientID, &c.OAuthClientSecret,
		&c.Workspace, &c.Environment, &c.Tags, &skip, &c.CreatedAt, &c.UpdatedAt)
	c.TLSSkipVerify = skip == 1
	return c, err
}

func (s *Store) GetConnection(ctx context.Context, id string) (Connection, error) {
	row := s.db.QueryRowContext(ctx, `
	  SELECT id,name,admin_api_url,base_url,auth_type,auth_secret_encrypted,auth_header,
	         oauth_token_url,oauth_client_id,oauth_client_secret_encrypted,workspace,
	         environment,tags,tls_skip_verify,created_at,updated_at
	  FROM kong_connections WHERE id = ?`, id)
	c, err := scanConn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) CreateConnection(ctx context.Context, c Connection) error {
	now := time.Now().UTC().Format(time.RFC3339)
	c.CreatedAt, c.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `
	  INSERT INTO kong_connections
	    (id,name,admin_api_url,base_url,auth_type,auth_secret_encrypted,auth_header,
	     oauth_token_url,oauth_client_id,oauth_client_secret_encrypted,workspace,
	     environment,tags,tls_skip_verify,created_at,updated_at)
	  VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Name, c.AdminAPIURL, c.BaseURL, c.AuthType, c.AuthSecret, c.AuthHeader,
		c.OAuthTokenURL, c.OAuthClientID, c.OAuthClientSecret, c.Workspace,
		c.Environment, c.Tags, b2i(c.TLSSkipVerify), c.CreatedAt, c.UpdatedAt)
	return err
}

func (s *Store) UpdateConnection(ctx context.Context, c Connection) error {
	res, err := s.db.ExecContext(ctx, `
	  UPDATE kong_connections SET name=?,admin_api_url=?,base_url=?,auth_type=?,auth_secret_encrypted=?,
	    auth_header=?,oauth_token_url=?,oauth_client_id=?,oauth_client_secret_encrypted=?,
	    workspace=?,environment=?,tags=?,tls_skip_verify=?,updated_at=?
	  WHERE id=?`,
		c.Name, c.AdminAPIURL, c.BaseURL, c.AuthType, c.AuthSecret, c.AuthHeader,
		c.OAuthTokenURL, c.OAuthClientID, c.OAuthClientSecret, c.Workspace,
		c.Environment, c.Tags, b2i(c.TLSSkipVerify), time.Now().UTC().Format(time.RFC3339), c.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM canvas_layout WHERE connection_id=?`, id); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM apply_history WHERE connection_id=?`, id); err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM kong_connections WHERE id=?`, id)
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
		`SELECT entity_type,entity_id,pos_x,pos_y FROM canvas_layout WHERE connection_id=?`, connID)
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
	  VALUES (?,?,?,?,?)
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

func (s *Store) AddHistory(ctx context.Context, h HistoryEntry) error {
	_, err := s.db.ExecContext(ctx, `
	  INSERT INTO apply_history (id,connection_id,applied_at,plan_json,result_json,status,error_message,actor)
	  VALUES (?,?,?,?,?,?,?,?)`,
		h.ID, h.ConnectionID, h.AppliedAt, h.PlanJSON, h.ResultJSON, h.Status, h.ErrorMessage, h.Actor)
	return err
}

func (s *Store) ListHistory(ctx context.Context, connID string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
	  SELECT id,connection_id,applied_at,plan_json,result_json,status,error_message,actor
	  FROM apply_history WHERE connection_id=? ORDER BY applied_at DESC LIMIT ?`, connID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.ID, &h.ConnectionID, &h.AppliedAt, &h.PlanJSON, &h.ResultJSON,
			&h.Status, &h.ErrorMessage, &h.Actor); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
