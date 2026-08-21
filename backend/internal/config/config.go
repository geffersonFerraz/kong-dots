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
}

// DefaultDatabaseURL points at a local PostgreSQL, matching the compose stack.
const DefaultDatabaseURL = "postgres://kongdots:kongdots@localhost:5432/kongdots?sslmode=disable"

func Load() Config {
	c := Config{
		Addr:      env("KONGDOTS_ADDR", ":8080"),
		DataDir:   env("KONGDOTS_DATA_DIR", "./data"),
		SecretKey: os.Getenv("KONGDOTS_SECRET_KEY"),
	}
	c.DatabaseURL = env("KONGDOTS_DATABASE_URL", DefaultDatabaseURL)
	origins := env("KONGDOTS_CORS_ORIGINS", "http://localhost:5173")
	for _, o := range strings.Split(origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			c.CORS = append(c.CORS, o)
		}
	}
	return c
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
