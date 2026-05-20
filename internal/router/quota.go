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

func (r *Router) getProviderQuotaJSON(providerID int) string {
	p, err := r.store.GetProvider(providerID)
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
	id := p.ID()
	r.saveQuota(id, qp.LastQuota())
}

func (r *Router) saveQuotaFromHeaders(providerID int, h http.Header) {
	r.saveQuota(providerID, provider.ParseQuota(h))
}

func (r *Router) saveQuota(providerID int, quota map[string]interface{}) {
	if r == nil || r.store == nil || providerID == 0 || len(quota) == 0 {
		return
	}
	p, err := r.store.GetProvider(providerID)
	if err != nil || p == nil {
		slog.Warn("save_quota_provider_lookup_failed", "provider_id", providerID, "error", err)
		return
	}
	qj, err := json.Marshal(quota)
	if err != nil {
		slog.Warn("save_quota_marshal_failed", "provider_id", providerID, "error", err)
		return
	}
	if err := r.store.SaveProviderQuota(p.ID, qj); err != nil {
		slog.Warn("save_quota_failed", "provider_id", providerID, "error", err)
	}
}
