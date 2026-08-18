package config

import (
	"os"
	"strings"
)

type Config struct {
	Addr      string
	DataDir   string
	DBPath    string
	SecretKey string
	CORS      []string
}

func Load() Config {
	c := Config{
		Addr:      env("KONGDOTS_ADDR", ":8080"),
		DataDir:   env("KONGDOTS_DATA_DIR", "./data"),
		SecretKey: os.Getenv("KONGDOTS_SECRET_KEY"),
	}
	c.DBPath = env("KONGDOTS_DB_PATH", c.DataDir+"/kong-dots.db")
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
