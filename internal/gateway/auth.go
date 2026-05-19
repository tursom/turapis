package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
)

type ctxKey int

const (
	ctxKeyApiKey ctxKey = iota
	ctxKeyCollector
)

var reCodexVersion = regexp.MustCompile(`codex_cli_rs/(\d+\.\d+\.\d+)`)

func withApiKey(ctx context.Context, k *config.APIKey) context.Context {
	return context.WithValue(ctx, ctxKeyApiKey, k)
}

func apiKeyFromContext(ctx context.Context) *config.APIKey {
	if k, ok := ctx.Value(ctxKeyApiKey).(*config.APIKey); ok {
		return k
	}
	return nil
}

// apiKeyAuth 中间件：对 AI API 端点的可选 Bearer 鉴权
// 无 Authorization header 时放行（向后兼容），有则验证
func (g *Gateway) apiKeyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := extractBearerToken(r)
		if key == "" {
			w.Header().Set("X-Api-Key-Auth", "missing")
			next.ServeHTTP(w, r)
			return
		}

		if strings.HasPrefix(key, "eyJ") {
			w.Header().Set("X-Api-Key-Auth", "jwt-passthrough")
			ua := r.Header.Get("User-Agent")
			if m := reCodexVersion.FindStringSubmatch(ua); len(m) > 1 {
				_ = g.store.SetSetting("codex_cli_version", m[1])
				r = r.WithContext(models.WithCodexVersion(r.Context(), m[1]))
			}
			r = r.WithContext(models.WithRawProxy(r.Context()))
			slog.Info("jwt_raw_proxy_enabled", "remote", r.RemoteAddr)
			next.ServeHTTP(w, r)
			return
		}

		apiKey, err := g.store.ValidateAPIKey(key)
		if err != nil {
			slog.Warn("invalid api key", "remote", r.RemoteAddr, "token_prefix", key[:min(8, len(key))])
			w.Header().Set("X-Api-Key-Auth", "invalid")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "invalid api key",
			})
			return
		}

		w.Header().Set("X-Api-Key-Auth", "valid")
		ctx := withApiKey(r.Context(), apiKey)

		// 将 API Key 限制注入 context，供路由层检查
		perms := apiKey.ParsePermissions()
		if perms != nil {
			ctx = models.WithKeyPermissions(ctx, &models.KeyPermissions{
				AllowedModels:    perms.AllowedModels,
				AllowedProviders: perms.AllowedProviders,
			})
		}

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return ""
	}
	if !strings.HasPrefix(auth, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(auth, "Bearer ")
}

// checkModelAllowed checks if the model is permitted by the API key in context.
// Returns an HTTP status code and JSON error body if forbidden, or empty strings if allowed.
func checkModelAllowed(ctx context.Context, model string) (int, string) {
	if model == "" {
		return 0, ""
	}
	perms := models.KeyPermissionsFromContext(ctx)
	if perms == nil || perms.ModelAllowed(model) {
		return 0, ""
	}
	return http.StatusForbidden, `{"error":{"message":"model ` + model + ` is not allowed for this API key","type":"forbidden"}}`
}
