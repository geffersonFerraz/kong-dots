package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestOAuthFieldsRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	ctx := context.Background()
	want := Connection{
		ID: "c1", Name: "oauth kong", AdminAPIURL: "https://kong.internal:8444",
		AuthType: "oauth2", OAuthTokenURL: "https://idp.internal/oauth2/token",
		OAuthClientID: "kong-dots", OAuthClientSecret: "encrypted-blob",
		Environment: "prod", TLSSkipVerify: true,
	}
	if err := st.CreateConnection(ctx, want); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetConnection(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got.OAuthTokenURL != want.OAuthTokenURL || got.OAuthClientID != want.OAuthClientID ||
		got.OAuthClientSecret != want.OAuthClientSecret {
		t.Fatalf("oauth fields not stored: %+v", got)
	}
	if !got.TLSSkipVerify || got.AuthType != "oauth2" {
		t.Errorf("connection not stored faithfully: %+v", got)
	}

	got.OAuthClientID = "rotated"
	got.OAuthClientSecret = "new-blob"
	if err := st.UpdateConnection(ctx, got); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetConnection(ctx, "c1")
	if after.OAuthClientID != "rotated" || after.OAuthClientSecret != "new-blob" {
		t.Errorf("oauth fields not updated: %+v", after)
	}

	list, err := st.ListConnections(ctx)
	if err != nil || len(list) != 1 || list[0].OAuthTokenURL != want.OAuthTokenURL {
		t.Errorf("list lost the oauth fields: %+v %v", list, err)
	}
}

// A database created before OAuth support must gain the new columns on open,
// keeping the connections already registered in it.
func TestMigrationAddsOAuthColumnsToAnOlderDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
	CREATE TABLE kong_connections (
	  id TEXT PRIMARY KEY, name TEXT NOT NULL, admin_api_url TEXT NOT NULL,
	  auth_type TEXT NOT NULL DEFAULT 'none', auth_secret_encrypted TEXT NOT NULL DEFAULT '',
	  auth_header TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
	  environment TEXT NOT NULL DEFAULT 'dev', tags TEXT NOT NULL DEFAULT '',
	  tls_skip_verify INTEGER NOT NULL DEFAULT 0,
	  created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
	INSERT INTO kong_connections (id,name,admin_api_url,auth_type,created_at,updated_at)
	  VALUES ('old','legacy kong','http://kong:8001','key','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("opening an older database should migrate it: %v", err)
	}
	defer st.Close()

	got, err := st.GetConnection(context.Background(), "old")
	if err != nil {
		t.Fatalf("the existing connection should survive the migration: %v", err)
	}
	if got.Name != "legacy kong" || got.AuthType != "key" {
		t.Errorf("migrated row is wrong: %+v", got)
	}
	if got.OAuthTokenURL != "" || got.OAuthClientID != "" {
		t.Errorf("new columns should default to empty: %+v", got)
	}

	// And the migrated database accepts an OAuth2 connection.
	err = st.CreateConnection(context.Background(), Connection{
		ID: "new", Name: "oauth", AdminAPIURL: "http://kong:8001", AuthType: "oauth2",
		OAuthTokenURL: "https://idp/token", OAuthClientID: "id", OAuthClientSecret: "blob",
	})
	if err != nil {
		t.Fatalf("insert after migration: %v", err)
	}

	// Opening again must be a no-op, not a duplicate-column error.
	st.Close()
	if st2, err := Open(path); err != nil {
		t.Fatalf("re-opening a migrated database failed: %v", err)
	} else {
		st2.Close()
	}
}
