// Command portal is the gharp multi-tenant Portal control plane.
// Boot order: load config → open store → assemble components → listen.
// All component wiring lives in internal/wiring; main stays a thin entrypoint.
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gharp/portal/internal/config"
	"github.com/gharp/portal/internal/store"
	"github.com/gharp/portal/internal/wiring"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := store.Open(cfg.StoreDSN)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer db.Close()

	handler, mgr := wiring.Assemble(cfg, db)

	// Background health sweep: reconcile each running gharp's reachability.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if _, err := mgr.Health(ctx); err != nil {
				log.Printf("health sweep: %v", err)
			}
			cancel()
		}
	}()

	addr := cfg.BindAddr + ":" + cfg.Port
	log.Printf("portal listening on %s (base_url=%s)", addr, cfg.BaseURL)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
