package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/provider"
)

// Admin API 管理
type Admin struct {
	store    *config.Store
	registry *provider.Registry
}

// New 创建 Admin API
func New(store *config.Store, registry *provider.Registry) *Admin {
	return &Admin{store: store, registry: registry}
}

// Routes 返回 Admin 子路由（供 Gateway 挂载）
func (a *Admin) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestSize(1 << 20)) // 1MB for admin

	// Provider 管理
	r.Post("/providers", a.createProvider)
	r.Get("/providers", a.listProviders)
	r.Get("/providers/{id}", a.getProvider)
	r.Put("/providers/{id}", a.updateProvider)
	r.Delete("/providers/{id}", a.deleteProvider)

	// 模型映射管理
	r.Post("/model-mappings", a.createModelMapping)
	r.Get("/model-mappings", a.listModelMappings)
	r.Put("/model-mappings/{id}", a.updateModelMapping)
	r.Delete("/model-mappings/{id}", a.deleteModelMapping)

	// 模型发现
	r.Post("/providers/{id}/discover", a.discoverModels)

	// 全局设置
	r.Get("/settings", a.getSettings)
	r.Put("/settings", a.updateSettings)

	// 状态
	r.Get("/status", a.getStatus)

	return r
}

// --- 辅助函数 ---

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
