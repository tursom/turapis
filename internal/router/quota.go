package router

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/provider"
)

type quotaProvider interface {
	LastQuota() map[string]interface{}
}

func (r *Router) getProviderQuotaJSON(providerName string) string {
	p, err := r.store.GetProviderByName(providerName)
	if err != nil || p == nil {
		return ""
	}
	q := config.ParseProviderQuota(p.APIKey)
	if q == nil {
		return ""
	}
	return string(*q)
}

func (r *Router) saveQuotaFromProvider(p provider.Provider) {
	qp, ok := p.(quotaProvider)
	if !ok {
		return
	}
	r.saveQuota(p.Name(), qp.LastQuota())
}

func (r *Router) saveQuotaFromHeaders(providerName string, h http.Header) {
	r.saveQuota(providerName, provider.ParseQuota(h))
}

func (r *Router) saveQuota(providerName string, quota map[string]interface{}) {
	if r == nil || r.store == nil || providerName == "" || len(quota) == 0 {
		return
	}
	p, err := r.store.GetProviderByName(providerName)
	if err != nil || p == nil {
		slog.Warn("save_quota_provider_lookup_failed", "provider", providerName, "error", err)
		return
	}
	qj, err := json.Marshal(quota)
	if err != nil {
		slog.Warn("save_quota_marshal_failed", "provider", providerName, "error", err)
		return
	}
	if err := r.store.SaveProviderQuota(p.ID, qj); err != nil {
		slog.Warn("save_quota_failed", "provider", providerName, "error", err)
	}
}
