package admin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tursom/turapis/internal/config"
)

func (a *Admin) createModelMapping(w http.ResponseWriter, r *http.Request) {
	var m config.ModelMapping
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if m.ModelName == "" || m.ProviderID == 0 {
		writeError(w, http.StatusBadRequest, "model_name and provider_id are required")
		return
	}

	if err := a.store.CreateModelMapping(&m); err != nil {
		writeError(w, http.StatusInternalServerError, "create model mapping: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, m)
}

func (a *Admin) listModelMappings(w http.ResponseWriter, r *http.Request) {
	mappings, err := a.store.ListModelMappings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mappings)
}

func (a *Admin) updateModelMapping(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var m config.ModelMapping
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	m.ID = id

	if err := a.store.UpdateModelMapping(&m); err != nil {
		writeError(w, http.StatusInternalServerError, "update model mapping: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, m)
}

func (a *Admin) deleteModelMapping(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := a.store.DeleteModelMapping(id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete model mapping: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) discoverModels(w http.ResponseWriter, r *http.Request) {
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

	prov, ok := a.registry.Get(p.Name)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "provider not registered")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	modelInfos, err := prov.ListModels(ctx)
	if err != nil {
		slog.Warn("model_discovery_failed", "provider", p.Name, "error", err)
		writeError(w, http.StatusInternalServerError, "discover models: "+err.Error())
		return
	}

	for _, m := range modelInfos {
		if err := a.store.AddProviderModel(id, m.ID, m.Name); err != nil {
			slog.Warn("add_provider_model_failed", "provider", p.Name, "model", m.ID, "error", err)
		}
	}

	models, _ := a.store.GetProviderModels(id)
	slog.Info("model_discovery_complete", "provider", p.Name, "count", len(models))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"provider": p.Name,
		"models":   models,
		"count":    len(models),
	})
}

func (a *Admin) getStatus(w http.ResponseWriter, r *http.Request) {
	providers, _ := a.store.ListEnabledProviders()
	status := map[string]interface{}{
		"status":          "ok",
		"provider_count":  len(providers),
		"registered_count": len(a.registry.List()),
	}

	// 检查每个 Provider 模型发现状态
	providerStatuses := make([]map[string]interface{}, 0, len(providers))
	for _, p := range providers {
		ps := map[string]interface{}{
			"name":    p.Name,
			"enabled": p.Enabled,
		}
		if models, err := a.store.GetProviderModels(p.ID); err == nil {
			ps["discovered_models"] = len(models)
		}
		_, registered := a.registry.Get(p.Name)
		ps["registered"] = registered
		providerStatuses = append(providerStatuses, ps)
	}
	status["providers"] = providerStatuses

	writeJSON(w, http.StatusOK, status)
}

func (a *Admin) getSettings(w http.ResponseWriter, r *http.Request) {
	val, _ := a.store.GetSetting("default_priority_chain")
	writeJSON(w, http.StatusOK, map[string]string{"default_priority_chain": val})
}

func (a *Admin) updateSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]string
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	if chain, ok := body["default_priority_chain"]; ok {
		if err := a.store.SetSetting("default_priority_chain", chain); err != nil {
			writeError(w, http.StatusInternalServerError, "save setting: "+err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
