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
	auth     *AdminAuth
}

// New 创建 Admin API
func New(store *config.Store, registry *provider.Registry, auth *AdminAuth) *Admin {
	return &Admin{store: store, registry: registry, auth: auth}
}

// Routes 返回 Admin 子路由（供 Gateway 挂载）
func (a *Admin) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestSize(1 << 20)) // 1MB for admin

	r.Post("/login", a.auth.Login)
	r.Post("/logout", a.auth.Logout)

	r.Group(func(r chi.Router) {
		r.Use(a.auth.Middleware)

		r.Get("/providers", a.listProviders)
		r.Get("/providers/{id}", a.getProvider)
		r.Get("/model-mappings", a.listModelMappings)
		r.Get("/settings", a.getSettings)
		r.Get("/status", a.getStatus)
		r.Get("/api-keys", a.listAPIKeys)
		r.Get("/access-logs", a.listAccessLogs)
		r.Get("/access-logs/{id}", a.getAccessLog)
		r.Get("/sites", a.listSites)
		r.Get("/sites/{id}", a.getSite)
		r.Get("/sites/{id}/models", a.listSiteModels)
		r.Get("/users", a.listUsers)
		r.Get("/users/me", a.getUser)
	})

	r.Group(func(r chi.Router) {
		r.Use(a.auth.Middleware)
		r.Use(a.auth.RequireAdmin)

		r.Post("/providers", a.createProvider)
		r.Put("/providers/{id}", a.updateProvider)
		r.Delete("/providers/{id}", a.deleteProvider)

		r.Post("/model-mappings", a.createModelMapping)
		r.Put("/model-mappings/{id}", a.updateModelMapping)
		r.Delete("/model-mappings/{id}", a.deleteModelMapping)

		r.Post("/providers/{id}/quota", a.probeQuota)
		r.Post("/providers/batch-quota", a.batchProbeQuota)
		r.Post("/providers/refresh-tokens", a.refreshOAuthTokens)
		r.Post("/providers/batch-discover", a.discoverAllModels)
		r.Post("/providers/{id}/discover", a.discoverModels)

		r.Put("/settings", a.updateSettings)

		r.Post("/api-keys", a.createAPIKey)
		r.Put("/api-keys/{id}", a.updateAPIKey)
		r.Delete("/api-keys/{id}", a.revokeAPIKey)

		r.Post("/sites", a.createSite)
		r.Put("/sites/{id}", a.updateSite)
		r.Delete("/sites/{id}", a.deleteSite)
		r.Post("/sites/{id}/models", a.addSiteModel)
		r.Delete("/sites/{id}/models/{modelId}", a.deleteSiteModel)
		r.Post("/sites/{id}/create-provider", a.createProviderFromSite)

		r.Post("/users", a.createUser)
		r.Get("/users/{id}", a.getUser)
		r.Put("/users/{id}", a.updateUser)
		r.Delete("/users/{id}", a.deleteUser)
	})

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
