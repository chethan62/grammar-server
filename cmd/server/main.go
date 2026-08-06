// Command server is a LanguageTool-compatible grammar-checking HTTP server
// backed by harper-ls (offline, privacy-first).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"grammar-server/internal/api"
	"grammar-server/internal/config"
	"grammar-server/internal/engine"
)

func main() {
	// CLI flags — override config file values.
	var (
		port    = flag.Int("port", 0, "listen port")
		dialect = flag.String("dialect", "", "default harper dialect")
		harper  = flag.String("harper", "", "path to harper-ls binary")
		cfgPath = flag.String("config", "", "path to YAML config file")
	)
	flag.Parse()

	// Load config (defaults → file → flags).
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *port > 0 {
		cfg.Port = *port
	}
	if *dialect != "" {
		cfg.Dialect = *dialect
	}
	if *harper != "" {
		cfg.Harper = *harper
	}
	if cfg.LogFmt == "json" {
		log.SetFlags(0) // structured JSON log lines
	}

	// Start the harper-ls engine.
	h, err := engine.NewHarper(cfg.Harper, cfg.Dialect, nil)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}
	defer h.Close()

	// Build the server.
	srv := api.NewServer(h)
	addr := fmt.Sprintf(":%d", cfg.Port)
	httpSrv := &http.Server{
		Addr:         addr,
		Handler:      corsMiddleware(srv.Handler()),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	log.Printf("grammar-server listening on %s (harper-ls: %s, dialect: %s)", addr, cfg.Harper, cfg.Dialect)

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Printf("shutting down gracefully (timeout 10s)…")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(shutCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("listen: %v", err)
	}
	log.Println("server stopped")
}

// corsMiddleware adds permissive CORS headers for local use.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
