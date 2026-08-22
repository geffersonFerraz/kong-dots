// Command server runs the Kong Flow backend: REST API + WebSocket for the
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
	"strings"
	"syscall"
	"time"

	"github.com/gefferson/kong-flow/backend/internal/api"
	"github.com/gefferson/kong-flow/backend/internal/config"
	"github.com/gefferson/kong-flow/backend/internal/cryptox"
	"github.com/gefferson/kong-flow/backend/internal/store"
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
		log.Printf("KONGFLOW_SECRET_KEY not set; using generated key at %s", filepath.Join(cfg.DataDir, "secret.key"))
	}

	st, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	hub := api.NewHub(cfg.CORS)
	staticDir := os.Getenv("KONGFLOW_STATIC_DIR")
	if staticDir != "" {
		log.Printf("serving UI from %s", staticDir)
	}
	approval := api.Approval{Approvers: cfg.Approvers, Token: cfg.ApproverToken}
	if approval.Required() {
		log.Printf("approval queue on: changes are applied only by %s", describeApprovers(cfg))
	} else {
		log.Printf("approval queue off: every editor applies directly (set KONGFLOW_APPROVERS or KONGFLOW_APPROVER_TOKEN to require review)")
	}
	handler := api.NewServer(st, cipher, hub, approval).Router(cfg.CORS, staticDir)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("kong-flow backend listening on %s (db: %s)", cfg.Addr, store.Redact(cfg.DatabaseURL))
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

// describeApprovers says who may apply, without ever printing the token.
func describeApprovers(cfg config.Config) string {
	switch {
	case len(cfg.Approvers) > 0 && cfg.ApproverToken != "":
		return strings.Join(cfg.Approvers, ", ") + " (with the approval token)"
	case len(cfg.Approvers) > 0:
		return strings.Join(cfg.Approvers, ", ")
	default:
		return "whoever holds the approval token"
	}
}
