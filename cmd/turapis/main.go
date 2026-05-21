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
	"github.com/tursom/turapis/internal/codexauth"
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

	codexAuthEnabled := flag.Bool("codex-auth", false, "启用 Codex OAuth 自动登录/注册/刷新/健康检查系统")
	codexAuthInterval := flag.Duration("codex-auth-interval", 1*time.Hour, "自动注册间隔")
	codexRefreshInterval := flag.Duration("codex-refresh-interval", 7*24*time.Hour, "Token 刷新间隔")
	codexHealthInterval := flag.Duration("codex-health-interval", 24*time.Hour, "健康检查间隔")
	codexAuthProxy := flag.String("codex-auth-proxy", "", "Codex Auth 操作代理地址")

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
			prov := po.New(p.ID, p.Name, p.BaseURL, apiKey, supportedTools, p.Proxy)
			if searxngURL := os.Getenv("SEARXNG_URL"); searxngURL != "" {
				prov.SetSearXNG(searxngURL)
			}
			registry.Register(prov)
		case "anthropic":
			registry.Register(pa.New(p.ID, p.Name, p.BaseURL, apiKey, supportedTools, p.Proxy))
		}
	}
	slog.Info("loaded providers from database", "count", len(dbProviders))

	r := router.New(store, registry)
	adminAuth := admin.NewAdminAuth(store)
	defer adminAuth.Shutdown()
	adm := admin.New(store, registry, adminAuth)
	gw := gateway.New(r, adm.Routes(), store, store.LogStore, *staticDir, *addr)

	// 条件性初始化 Codex OAuth 自动登录系统
	// 仅在 --codex-auth 标志启用时创建 codexauth 组件并挂载 /admin/codex 路由。
	// 当前 browserClient 为 nil（browserless 未主动创建），后续可按需注入。
	if *codexAuthEnabled {
		flowCfg := codexauth.FlowConfig{
			CallbackPort:   1455,
			PollInterval:   5 * time.Second,
			PollTimeout:    120 * time.Second,
			BrowserTimeout: 120 * time.Second,
		}
		flow := codexauth.NewAutoLoginFlow(flowCfg)
		reg := codexauth.NewRegistry(store, flow)

		lmCfg := codexauth.CodexAuthConfig{
			AutoLoginEnabled:    *codexAuthEnabled,
			AutoRefreshEnabled:  *codexAuthEnabled,
			AutoHealthEnabled:   *codexAuthEnabled,
			AutoLoginInterval:   *codexAuthInterval,
			RefreshInterval:     *codexRefreshInterval,
			HealthCheckInterval: *codexHealthInterval,
			MaxConcurrentLogins: 1,
			ProxyURL:            *codexAuthProxy,
		}
		prober := codexauth.NewHTTPCodexHealthProber(nil)
		lm := codexauth.NewLifecycleManager(lmCfg, reg, store, nil, prober)
		lm.Start(context.Background())
		defer lm.Shutdown()

		ca := codexauth.NewCodexAdmin(reg, adminAuth, lm, flow, nil)
		gw.SetCodexRoutes(ca.Routes())
		slog.Info("codex auth system enabled", "register_interval", *codexAuthInterval)
	}

	store.StartCleanup(context.Background(), 1*time.Hour, 30)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("turapis starting", "addr", *addr)
	if err := gw.ListenAndServe(ctx); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
