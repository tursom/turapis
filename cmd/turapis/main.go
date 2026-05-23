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
	"github.com/tursom/turapis/internal/browser"
	"github.com/tursom/turapis/internal/codexauth"
	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/email"
	"github.com/tursom/turapis/internal/gateway"
	"github.com/tursom/turapis/internal/provider"
	"github.com/tursom/turapis/internal/sms"
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

	// 初始化 Codex OAuth 自动登录系统
	// browserClient 为 nil（browserless 未主动创建），注册/重登录不可用，但不影响管理功能。
	flowCfg := codexauth.FlowConfig{
		CallbackPort:   1455,
		PollInterval:   5 * time.Second,
		PollTimeout:    120 * time.Second,
		BrowserTimeout: 120 * time.Second,
	}
	flow := codexauth.NewAutoLoginFlow(flowCfg)
	reg := codexauth.NewRegistry(store, flow)

	// 从数据库加载 codex auth 配置，Db 无配置时使用默认值
	lmCfg := codexauth.DefaultCodexAuthConfig()
	if raw, err := store.GetSetting("codex_auth_config"); err == nil && raw != "" {
		if err := json.Unmarshal([]byte(raw), &lmCfg); err != nil {
			slog.Warn("parse_codex_auth_config_failed", "error", err)
		}
	}
	var bc codexauth.BrowserClient
	if lmCfg.BrowserURL != "" {
		bc = browser.NewBrowserlessClient(lmCfg.BrowserURL, codexauth.DefaultBrowserTimeout)
		flow.SetBrowserClient(bc)
	}

	var ep email.EmailProvider
	switch lmCfg.DefaultEmailProvider {
	case "tempmail_lol":
		ep = email.NewTempmailLOL(epConfig(lmCfg, "tempmail_lol"))
	case "mailondeck":
		ep = email.NewMailondeck(epConfig(lmCfg, "mailondeck"))
	case "mailondeck_browserless":
		if bc != nil {
			if blc, ok := bc.(*browser.BrowserlessClient); ok {
				ep = email.NewMailondeckBrowserless(blc)
			}
		}
	}
	if ep != nil {
		flow.SetEmailProvider(ep)
	}

	if lmCfg.DefaultSMSProvider != "" && lmCfg.SMSProviderSettings != nil {
		sp := sms.NewFiveSim(sms.SMSProviderConfig{
			ProxyURL: lmCfg.ProxyURL,
			APIKey:   lmCfg.SMSProviderSettings.APIKey,
		})
		flow.SetSMSProvider(sp)
	}

	prober := codexauth.NewHTTPCodexHealthProber(nil)
	lm := codexauth.NewLifecycleManager(lmCfg, reg, store, nil, prober)
	lm.Start(context.Background())
	defer lm.Shutdown()

	newBC := func(wsURL string, timeout time.Duration) codexauth.BrowserClient {
		return browser.NewBrowserlessClient(wsURL, timeout)
	}

	newEP := func(name, proxyURL string, eps codexauth.EmailProviderSettings, bc codexauth.BrowserClient) email.EmailProvider {
		cfg := email.EmailProviderConfig{ProxyURL: proxyURL, APIKey: eps.APIKey, Domain: eps.Domain}
		switch name {
		case "tempmail_lol":
			return email.NewTempmailLOL(cfg)
		case "mailondeck":
			return email.NewMailondeck(cfg)
		case "mailondeck_browserless":
			if bc != nil {
				if blc, ok := bc.(*browser.BrowserlessClient); ok {
					return email.NewMailondeckBrowserless(blc)
				}
			}
		}
		return nil
	}

	ca := codexauth.NewCodexAdmin(reg, adminAuth, lm, flow, bc, store, newBC, newEP)
	gw.SetCodexRoutes(ca.Routes())

	store.StartCleanup(context.Background(), 1*time.Hour, 30)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	slog.Info("turapis starting", "addr", *addr)
	if err := gw.ListenAndServe(ctx); err != nil {
		slog.Error("server stopped", "error", err)
	}
}

func epConfig(cfg codexauth.CodexAuthConfig, provider string) email.EmailProviderConfig {
	ec := email.EmailProviderConfig{ProxyURL: cfg.ProxyURL}
	if eps, ok := cfg.EmailProviders[provider]; ok {
		ec.APIKey = eps.APIKey
		ec.Domain = eps.Domain
	}
	return ec
}
