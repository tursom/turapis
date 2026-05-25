package router

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/tursom/turapis/internal/provider"
)

const oauthRefreshSkew = 5 * time.Minute

var refreshOAuthToken = provider.RefreshCodexToken

func (r *Router) refreshOAuthProviderIfNeeded(p provider.Provider, force bool) (bool, error) {
	if r == nil || r.store == nil || p == nil {
		return false, nil
	}
	cfg, err := r.store.GetProvider(p.ID())
	if err != nil {
		return false, err
	}
	if cfg.AuthMode != "oauth" {
		return false, nil
	}
	if !force && !provider.OAuthAccessTokenExpiresSoon(cfg.APIKey, time.Now(), oauthRefreshSkew) {
		return false, nil
	}
	if err := refreshOAuthToken(r.store, cfg.ID, cfg.Proxy, r.registry); err != nil {
		return false, err
	}
	if force {
		slog.Info("oauth_token_refreshed_after_auth_error", "provider", p.Name(), "provider_id", p.ID())
	} else {
		slog.Info("oauth_token_refreshed_before_expiry", "provider", p.Name(), "provider_id", p.ID())
	}
	return true, nil
}

func isAuthStatus(statusCode int) bool {
	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
}
