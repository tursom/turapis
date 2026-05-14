package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/provider"
	"github.com/tursom/turapis/internal/provider/anthropic"
	"github.com/tursom/turapis/internal/provider/openai"
)

func (a *Admin) createProvider(w http.ResponseWriter, r *http.Request) {
	var p config.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if p.Name == "" || p.BaseURL == "" || p.APIKey == "" {
		writeError(w, http.StatusBadRequest, "name, base_url, api_key are required")
		return
	}
	if p.Protocol != "openai" && p.Protocol != "anthropic" {
		writeError(w, http.StatusBadRequest, "protocol must be 'openai' or 'anthropic'")
		return
	}

	if err := a.store.CreateProvider(&p); err != nil {
		writeError(w, http.StatusInternalServerError, "create provider: "+err.Error())
		return
	}

	// 同步注册 Provider 实例到 Registry
	a.registerProviderInstance(&p)

	writeJSON(w, http.StatusCreated, p)
}

func (a *Admin) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := a.store.ListProviders()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, providers)
}

func (a *Admin) getProvider(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	p, err := a.store.GetProvider(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *Admin) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var p config.Provider
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	p.ID = id

	if err := a.store.UpdateProvider(&p); err != nil {
		writeError(w, http.StatusInternalServerError, "update provider: "+err.Error())
		return
	}

	// 同步更新 Registry（删除旧的，注册新的）
	a.registry.Delete(p.Name)
	a.registerProviderInstance(&p)

	writeJSON(w, http.StatusOK, p)
}

func (a *Admin) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	// 先获取 Provider 名称用于 Registry 删除
	p, err := a.store.GetProvider(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	if err := a.store.DeleteProvider(id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete provider: "+err.Error())
		return
	}

	a.registry.Delete(p.Name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) registerProviderInstance(p *config.Provider) {
	if !p.Enabled {
		return
	}
	apiKey := p.APIKey
	if p.AuthMode == "oauth" {
		var creds map[string]interface{}
		if err := json.Unmarshal([]byte(p.APIKey), &creds); err == nil {
			if tokens, ok := creds["tokens"].(map[string]interface{}); ok {
				if at, ok := tokens["access_token"].(string); ok {
					apiKey = at
				}
			}
		}
	}
	var prov provider.Provider
	switch p.Protocol {
	case "openai":
		prov = openai.New(p.Name, p.BaseURL, apiKey)
	case "anthropic":
		prov = anthropic.New(p.Name, p.BaseURL, apiKey)
	default:
		return
	}
	a.registry.Register(prov)
}
