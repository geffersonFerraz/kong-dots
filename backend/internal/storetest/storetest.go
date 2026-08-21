// Package storetest hands tests a Store backed by a real PostgreSQL, isolated
// per test in its own schema. Everything the store does — upserts, timestamptz
// round-trips, ON CONFLICT — is server behaviour, so faking it would test
// nothing.
//
// The database is the one `make test-db` starts; point somewhere else with
// KONGDOTS_TEST_DATABASE_URL. When none is reachable the tests skip rather than
// fail, so `go test ./...` still works on a machine without Docker.
package storetest

import (
	"database/sql"
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/gefferson/kong-dots/backend/internal/store"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const DefaultDSN = "postgres://kongdots:kongdots@localhost:5433/kongdots_test?sslmode=disable"

// DSN is the test database's connection string.
func DSN() string {
	if v := os.Getenv("KONGDOTS_TEST_DATABASE_URL"); v != "" {
		return v
	}
	return DefaultDSN
}

// New returns a Store on a schema of its own, dropped when the test ends.
func New(t *testing.T) *store.Store {
	t.Helper()
	dsn, _ := NewSchema(t)
	st, err := store.Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// NewSchema creates an empty schema and returns a DSN scoped to it, for tests
// that need the raw *sql.DB before the store opens it.
func NewSchema(t *testing.T) (dsn, schema string) {
	t.Helper()
	base := DSN()
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Skipf("PostgreSQL not reachable at %s (%v); start one with `make test-db`", store.Redact(base), err)
	}
	schema = schemaName(t)
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE; CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`)
		admin.Close()
	})
	return withSearchPath(t, base, schema), schema
}

// Open connects to a DSN from NewSchema, for tests that write their own SQL.
func Open(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", store.Redact(dsn), err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test dsn: %v", err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// schemaName turns a test name into a unique identifier PostgreSQL accepts.
func schemaName(t *testing.T) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			return r
		case r >= 'A' && r <= 'Z':
			return r + 32
		default:
			return '_'
		}
	}, t.Name())
	if len(safe) > 40 {
		safe = safe[:40]
	}
	return fmt.Sprintf("kdtest_%s_%d", safe, rand.Int31())
}
