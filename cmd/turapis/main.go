package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

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
	flag.Parse()

	store, err := config.NewStore(*dbPath)
	if err != nil {
		log.Fatalf("init store: %v", err)
	}
	defer store.Close()

	registry := provider.NewRegistry()

	// 从数据库加载已启用的 Provider 实例到 Registry
	dbProviders, err := store.ListEnabledProviders()
	if err != nil {
		log.Fatalf("list providers: %v", err)
	}
	for i := range dbProviders {
		p := &dbProviders[i]
		switch p.Protocol {
		case "openai":
			registry.Register(po.New(p.Name, p.BaseURL, p.APIKey))
		case "anthropic":
			registry.Register(pa.New(p.Name, p.BaseURL, p.APIKey))
		}
	}
	slog.Info("loaded providers from database", "count", len(dbProviders))

	r := router.New(store, registry)
	adm := admin.New(store, registry)
	gw := gateway.New(r, adm.Routes(), *addr)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("turapis starting", "addr", *addr)
	if err := gw.ListenAndServe(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
