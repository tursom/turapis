package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/router"
)

// Gateway HTTP 网关
type Gateway struct {
	router          *router.Router
	adminRoutes     http.Handler
	codexRoutes     http.Handler
	store           *config.Store
	accessLogWriter *accessLogWriter
	staticDir       string
	addr            string
}

// New 创建 Gateway
func New(r *router.Router, adminHandler http.Handler, store *config.Store, logStore *config.LogStore, staticDir, addr string) *Gateway {
	return &Gateway{
		router:          r,
		adminRoutes:     adminHandler,
		store:           store,
		accessLogWriter: newAccessLogWriter(logStore, 256),
		staticDir:       staticDir,
		addr:            addr,
	}
}

// SetCodexRoutes 设置 Codex 管理 API 的路由处理器。
// 若 handler 非 nil，SetupRoutes 会自动将其挂载到 /admin/codex。
// 若 handler 为 nil（默认），则/admin/codex 路由不会被注册。
func (g *Gateway) SetCodexRoutes(handler http.Handler) {
	g.codexRoutes = handler
}

// SetupRoutes 配置所有路由（AI API + Admin API 统一端口，PATH 区分）
func (g *Gateway) SetupRoutes() http.Handler {
	r := chi.NewRouter()

	// 中间件
	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.RequestSize(32 << 20)) // 32MB

	// AI API 端点 — 统一在 apiKeyAuth 组内（替代原有独立注册）
	r.Group(func(r chi.Router) {
		r.Use(g.apiKeyAuth)
		r.Use(g.accessLogMiddleware)
		r.Post("/v1/messages", g.handleMessages)
		r.Post("/v1/chat/completions", g.handleChatCompletions)
		r.Post("/v1/responses", g.handleResponses)
		r.Get("/v1/models", g.handleModels)
	})

	// Admin 端点（路径隔离）
	r.Mount("/admin", g.adminRoutes)

	if g.codexRoutes != nil {
		r.Mount("/admin/codex", g.codexRoutes)
	}

	// 健康检查
	r.Get("/health", g.handleHealth)

	// 静态文件服务 — 必须在所有 API 路由之后
	g.mountStatic(r)

	return r
}

// mountStatic 注册静态文件服务（含 SPA fallback）
func (g *Gateway) mountStatic(r chi.Router) {
	if g.staticDir == "" {
		return
	}
	staticFS := http.FileServer(http.Dir(g.staticDir))
	// chi 的 /* 不匹配 /，根路径单独注册
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(g.staticDir, "index.html"))
	})
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		fullPath := filepath.Join(g.staticDir, filepath.Clean(r.URL.Path))
		if _, err := os.Stat(fullPath); os.IsNotExist(err) {
			// SPA fallback: 不存在的路径返回 index.html
			http.ServeFile(w, r, filepath.Join(g.staticDir, "index.html"))
			return
		}
		staticFS.ServeHTTP(w, r)
	})
}

// ListenAndServe 启动服务器
func (g *Gateway) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:    g.addr,
		Handler: g.SetupRoutes(),
	}

	go func() {
		<-ctx.Done()
		slog.Info("shutting down server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	slog.Info("turapis starting", "addr", g.addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (g *Gateway) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (g *Gateway) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"object": "list",
		"data":   []interface{}{},
	})
}
