// Command server is a LanguageTool-compatible grammar-checking HTTP server
// backed by harper-ls (offline, privacy-first). No AI/LLM — pure rule-based.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"grammar-server/internal/api"
	"grammar-server/internal/config"
	"grammar-server/internal/engine"
	"grammar-server/internal/rewrite"
)

func main() {
	var (
		port    = flag.Int("port", -1, "listen port")
		host    = flag.String("host", "", "listen host (default 127.0.0.1; 0.0.0.0 exposes it on the network)")
		dialect = flag.String("dialect", "", "default harper dialect")
		harper  = flag.String("harper", "", "path to harper-ls binary")
		cfgPath = flag.String("config", "", "path to YAML config file")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if *port >= 0 {
		cfg.Port = *port
	}
	if *host != "" {
		cfg.Host = *host
	}
	if *dialect != "" {
		cfg.Dialect = *dialect
	}
	if *harper != "" {
		cfg.Harper = *harper
	}
	if cfg.Harper == "harper-ls" {
		cfg.Harper = resolveHarper("harper-ls")
		os.Setenv("HARPER_CLI", resolveHarper("harper-cli"))
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config: %v", err)
	}
	if cfg.LogFmt == "json" {
		log.SetFlags(0)
	}

	h, err := engine.NewHarper(cfg.Harper, cfg.Dialect, nil)
	if err != nil {
		log.Fatalf("engine: %v", err)
	}
	defer h.Close()

	srv := api.NewServer(h)

	// Local rewriting for POST /v2/rewrite. Optional by design: with the backend
	// stopped, every other endpoint behaves exactly as before.
	//
	// The config file sets the default backend; a backend chosen from the UI
	// (POST /v1/ai, saved in the user's config directory) outranks it, because a
	// setting that silently reverts on restart is worse than no setting.
	provider, rwURL, model := cfg.RewriteProvider, cfg.RewriteURL, cfg.RewriteModel
	if saved, ok := api.LoadSavedAI(); ok {
		if saved.Provider == rewrite.ProviderNone {
			provider, model = rewrite.ProviderNone, ""
		} else if saved.Provider != "" {
			provider, model = saved.Provider, saved.Model
			if saved.URL != "" {
				rwURL = saved.URL
			}
		}
	}
	if model != "" && provider != rewrite.ProviderNone {
		srv.SetRewrite(provider, rwURL, model)
		log.Printf("rewriting with %s (%s) via %s", model, provider, rwURL)
	} else {
		log.Printf("rewriting is off: POST /v2/rewrite answers 503 (choose a backend with GET/POST /v1/ai)")
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpSrv := &http.Server{
		Addr:        addr,
		Handler:     corsMiddleware(srv.Handler()),
		ReadTimeout: 15 * time.Second,
		// Must exceed the worst case maxTextChars allows (100 KB ≈ 18 s here) with
		// headroom for a hot machine: a body that outlives its own connection is
		// worse than an honest refusal.
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	log.Printf("grammar-server listening on %s (harper-ls: %s, dialect: %s)", addr, cfg.Harper, cfg.Dialect)

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

// resolveHarper finds a harper binary: own directory first, then PATH, then
// the cache directory (old embedded-binary location).
func resolveHarper(name string) string {
	// 1. same directory as this binary (portable bundle)
	exe, _ := os.Executable()
	if exe != "" {
		dir := filepath.Dir(exe)
		if p := filepath.Join(dir, name); fileExists(p) {
			return p
		}
	}
	// 2. user cache (old embedded extraction)
	cache, _ := os.UserCacheDir()
	if cache != "" {
		if p := filepath.Join(cache, "grammar-server", name); fileExists(p) {
			return p
		}
	}
	// 3. system PATH
	return name
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
