package store_test

import (
	"context"
	"testing"

	"github.com/gefferson/kong-dots/backend/internal/store"
	"github.com/gefferson/kong-dots/backend/internal/storetest"
)

func TestOAuthFieldsRoundTrip(t *testing.T) {
	st := storetest.New(t)

	ctx := context.Background()
	want := store.Connection{
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
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Errorf("timestamps should come back as RFC3339: %+v", got)
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
	dsn, _ := storetest.NewSchema(t)
	legacy := storetest.Open(t, dsn)
	_, err := legacy.Exec(`
	CREATE TABLE kong_connections (
	  id TEXT PRIMARY KEY, name TEXT NOT NULL, admin_api_url TEXT NOT NULL,
	  auth_type TEXT NOT NULL DEFAULT 'none', auth_secret_encrypted TEXT NOT NULL DEFAULT '',
	  auth_header TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
	  environment TEXT NOT NULL DEFAULT 'dev', tags TEXT NOT NULL DEFAULT '',
	  tls_skip_verify BOOLEAN NOT NULL DEFAULT FALSE,
	  created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL);
	INSERT INTO kong_connections (id,name,admin_api_url,auth_type,created_at,updated_at)
	  VALUES ('old','legacy kong','http://kong:8001','key','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Close()

	st, err := store.Open(dsn)
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
	err = st.CreateConnection(context.Background(), store.Connection{
		ID: "new", Name: "oauth", AdminAPIURL: "http://kong:8001", AuthType: "oauth2",
		OAuthTokenURL: "https://idp/token", OAuthClientID: "id", OAuthClientSecret: "blob",
	})
	if err != nil {
		t.Fatalf("insert after migration: %v", err)
	}

	// Opening again must be a no-op, not a duplicate-column error.
	st.Close()
	if st2, err := store.Open(dsn); err != nil {
		t.Fatalf("re-opening a migrated database failed: %v", err)
	} else {
		st2.Close()
	}
}

// Layout and history are what the canvas reloads on every open; they must
// survive an upsert and come back in the order the UI expects.
func TestLayoutUpsertAndHistoryOrder(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()

	if err := st.SaveLayout(ctx, "c1", []store.LayoutPos{
		{EntityType: "service", EntityID: "s1", X: 10, Y: 20},
		{EntityType: "route", EntityID: "r1", X: 30, Y: 40},
	}); err != nil {
		t.Fatal(err)
	}
	// Same key again: the position moves, it does not duplicate.
	if err := st.SaveLayout(ctx, "c1", []store.LayoutPos{
		{EntityType: "service", EntityID: "s1", X: 11, Y: 21},
	}); err != nil {
		t.Fatal(err)
	}
	layout, err := st.GetLayout(ctx, "c1")
	if err != nil {
		t.Fatal(err)
	}
	if len(layout) != 2 || layout["service:s1"].X != 11 || layout["route:r1"].Y != 40 {
		t.Fatalf("layout not upserted: %+v", layout)
	}

	for _, h := range []store.HistoryEntry{
		{ID: "h1", ConnectionID: "c1", AppliedAt: "2026-01-01T00:00:00Z", PlanJSON: "{}", Status: "success"},
		{ID: "h2", ConnectionID: "c1", AppliedAt: "2026-02-01T00:00:00Z", PlanJSON: "{}", Status: "failed"},
	} {
		if err := st.AddHistory(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := st.ListHistory(ctx, "c1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "h2" {
		t.Fatalf("history should come back newest first: %+v", entries)
	}
	if entries[0].AppliedAt != "2026-02-01T00:00:00Z" {
		t.Errorf("applied_at should round-trip as RFC3339: %q", entries[0].AppliedAt)
	}

	// Deleting the connection takes its layout and history with it.
	if err := st.CreateConnection(ctx, store.Connection{ID: "c1", Name: "k", AdminAPIURL: "http://kong:8001"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteConnection(ctx, "c1"); err != nil {
		t.Fatal(err)
	}
	if layout, _ := st.GetLayout(ctx, "c1"); len(layout) != 0 {
		t.Errorf("layout outlived the connection: %+v", layout)
	}
	if entries, _ := st.ListHistory(ctx, "c1", 10); len(entries) != 0 {
		t.Errorf("history outlived the connection: %+v", entries)
	}
}
