package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tursom/turapis/internal/config"
)

type ctxKey int

const (
	ctxKeyApiKey ctxKey = iota
	ctxKeyCollector
)

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

		apiKey, err := g.store.ValidateAPIKey(key)
		if err != nil {
			slog.Warn("invalid api key", "remote", r.RemoteAddr)
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
