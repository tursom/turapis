package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tursom/turapis/internal/admin"
	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/gateway"
	"github.com/tursom/turapis/internal/provider"
	pa "github.com/tursom/turapis/internal/provider/anthropic"
	po "github.com/tursom/turapis/internal/provider/openai"
	"github.com/tursom/turapis/internal/router"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	dbPath := flag.String("db", "turapis.db", "SQLite database path")
	staticDir := flag.String("static-dir", "", "static file directory (leave empty to disable)")
	logFile := flag.String("log-file", "", "log file path (leave empty for stderr only)")
	flag.Parse()

	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		defer f.Close()
		w := io.MultiWriter(os.Stderr, f)
		slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	store, err := config.NewStore(*dbPath, *dbPath+".logs")
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer store.Close()

	if err := store.SeedBuiltinSites(); err != nil {
		slog.Warn("seed_builtin_sites_failed", "error", err)
	}

	registry := provider.NewRegistry()

	// 从数据库加载已启用的 Provider 实例到 Registry
	dbProviders, err := store.ListEnabledProviders()
	if err != nil {
		log.Fatalf("list providers: %v", err)
	}
	for i := range dbProviders {
		p := &dbProviders[i]
		apiKey := p.APIKey
		if p.AuthMode == "oauth" {
			var creds map[string]interface{}
			if err := json.Unmarshal([]byte(p.APIKey), &creds); err == nil {
				if tokens, ok := creds["tokens"].(map[string]interface{}); ok {
					if at, ok := tokens["access_token"].(string); ok {
						apiKey = at
					}
				}
			}
		}
		var supportedTools []string
		if p.SupportedTools != "" {
			json.Unmarshal([]byte(p.SupportedTools), &supportedTools)
		}
		switch p.Protocol {
		case "openai":
			prov := po.New(p.Name, p.BaseURL, apiKey, supportedTools, p.Proxy)
			if searxngURL := os.Getenv("SEARXNG_URL"); searxngURL != "" {
				prov.SetSearXNG(searxngURL)
			}
			registry.Register(prov)
		case "anthropic":
			registry.Register(pa.New(p.Name, p.BaseURL, apiKey, supportedTools, p.Proxy))
		}
	}
	slog.Info("loaded providers from database", "count", len(dbProviders))

	r := router.New(store, registry)
	adminAuth := admin.NewAdminAuth(store)
	defer adminAuth.Shutdown()
	adm := admin.New(store, registry, adminAuth)
	gw := gateway.New(r, adm.Routes(), store, store.LogStore, *staticDir, *addr)

	store.StartCleanup(context.Background(), 1*time.Hour, 30)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("turapis starting", "addr", *addr)
	if err := gw.ListenAndServe(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
