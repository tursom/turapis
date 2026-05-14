package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

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

		_, err := g.store.ValidateAPIKey(key)
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
		next.ServeHTTP(w, r)
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
