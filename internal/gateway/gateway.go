package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/router"
)

// Gateway HTTP 网关
type Gateway struct {
	router      *router.Router
	adminRoutes http.Handler
	addr        string
}

// New 创建 Gateway
func New(r *router.Router, adminHandler http.Handler, addr string) *Gateway {
	return &Gateway{router: r, adminRoutes: adminHandler, addr: addr}
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

	// AI API 端点
	r.Post("/v1/messages", g.handleMessages)
	r.Post("/v1/chat/completions", g.handleChatCompletions)
	r.Post("/v1/responses", g.handleResponses)
	r.Get("/v1/models", g.handleModels)

	// Admin 端点（路径隔离）
	r.Mount("/admin", g.adminRoutes)

	// 健康检查
	r.Get("/health", g.handleHealth)

	return r
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
