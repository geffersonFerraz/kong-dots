// Command server runs the Kong Dots backend: REST API + WebSocket for the
// canvas UI, talking to one or more Kong Admin APIs.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gefferson/kong-dots/backend/internal/api"
	"github.com/gefferson/kong-dots/backend/internal/config"
	"github.com/gefferson/kong-dots/backend/internal/cryptox"
	"github.com/gefferson/kong-dots/backend/internal/store"
)

func main() {
	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	key, err := cryptox.LoadOrCreateKey(cfg.SecretKey, cfg.DataDir)
	if err != nil {
		log.Fatalf("secret key: %v", err)
	}
	cipher, err := cryptox.New(key)
	if err != nil {
		log.Fatalf("cipher: %v", err)
	}
	if cfg.SecretKey == "" {
		log.Printf("KONGDOTS_SECRET_KEY not set; using generated key at %s", filepath.Join(cfg.DataDir, "secret.key"))
	}

	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	hub := api.NewHub(cfg.CORS)
	staticDir := os.Getenv("KONGDOTS_STATIC_DIR")
	if staticDir != "" {
		log.Printf("serving UI from %s", staticDir)
	}
	handler := api.NewServer(st, cipher, hub).Router(cfg.CORS, staticDir)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("kong-dots backend listening on %s (db: %s)", cfg.Addr, store.Redact(cfg.DatabaseURL))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Println("shutdown complete")
}
