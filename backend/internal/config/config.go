package config

import (
	"os"
	"strings"
)

type Config struct {
	Addr        string
	DataDir     string
	DatabaseURL string
	SecretKey   string
	CORS        []string
	// Approvers are the people allowed to push a change to a real Kong, by the
	// display name their browser sends. ApproverToken is a shared secret that
	// grants the same right to whoever holds it — a name alone is self-declared,
	// so it identifies, it does not authenticate.
	//
	// While both are empty there is nothing to approve against, so every editor
	// applies directly: a single-operator install keeps working as it did.
	Approvers     []string
	ApproverToken string
}

// ApprovalRequired reports whether changes have to be queued for review.
func (c Config) ApprovalRequired() bool {
	return len(c.Approvers) > 0 || c.ApproverToken != ""
}

// DefaultDatabaseURL points at a local PostgreSQL, matching the compose stack.
const DefaultDatabaseURL = "postgres://kongflow:kongflow@localhost:5432/kongflow?sslmode=disable"

func Load() Config {
	c := Config{
		Addr:      env("KONGFLOW_ADDR", ":8080"),
		DataDir:   env("KONGFLOW_DATA_DIR", "./data"),
		SecretKey: os.Getenv("KONGFLOW_SECRET_KEY"),
	}
	c.DatabaseURL = env("KONGFLOW_DATABASE_URL", DefaultDatabaseURL)
	c.CORS = list(env("KONGFLOW_CORS_ORIGINS", "http://localhost:5173"))
	c.Approvers = list(os.Getenv("KONGFLOW_APPROVERS"))
	c.ApproverToken = os.Getenv("KONGFLOW_APPROVER_TOKEN")
	return c
}

// list splits a comma-separated environment variable, dropping blanks.
func list(v string) []string {
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
