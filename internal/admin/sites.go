package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/tursom/turapis/internal/config"
)

func (a *Admin) createSite(w http.ResponseWriter, r *http.Request) {
	var s config.Site
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if s.Name == "" || s.BaseURL == "" || s.Protocol == "" {
		writeError(w, http.StatusBadRequest, "name, base_url, protocol are required")
		return
	}
	if s.Protocol != "openai" && s.Protocol != "anthropic" && s.Protocol != "codex" {
		writeError(w, http.StatusBadRequest, "protocol must be 'openai', 'anthropic', or 'codex'")
		return
	}
	if s.AuthMode == "" {
		s.AuthMode = "api_key"
	}
	if err := a.store.CreateSite(&s); err != nil {
		writeError(w, http.StatusInternalServerError, "create site: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

func (a *Admin) listSites(w http.ResponseWriter, r *http.Request) {
	sites, err := a.store.ListSites()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Return sites with model counts
	type siteWithCount struct {
		config.Site
		ModelCount int `json:"model_count"`
	}
	result := make([]siteWithCount, 0, len(sites))
	for _, s := range sites {
		models, _ := a.store.GetSiteModels(s.ID)
		result = append(result, siteWithCount{
			Site:       s,
			ModelCount: len(models),
		})
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *Admin) getSite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	s, err := a.store.GetSite(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *Admin) updateSite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var s config.Site
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	s.ID = id
	if err := a.store.UpdateSite(&s); err != nil {
		writeError(w, http.StatusInternalServerError, "update site: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *Admin) deleteSite(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.store.DeleteSite(id); err != nil {
		writeError(w, http.StatusInternalServerError, "delete site: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) addSiteModel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		ModelID   string `json:"model_id"`
		ModelName string `json:"model_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.ModelID == "" || body.ModelName == "" {
		writeError(w, http.StatusBadRequest, "model_id and model_name are required")
		return
	}
	if err := a.store.AddSiteModel(id, body.ModelID, body.ModelName); err != nil {
		writeError(w, http.StatusInternalServerError, "add site model: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "added"})
}

func (a *Admin) listSiteModels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	models, err := a.store.GetSiteModels(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, models)
}

func (a *Admin) deleteSiteModel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	modelID, err := strconv.Atoi(chi.URLParam(r, "modelId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid modelId")
		return
	}
	_ = id // site id used for URL routing only
	if err := a.store.DeleteSiteModel(modelID); err != nil {
		writeError(w, http.StatusInternalServerError, "delete site model: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (a *Admin) createProviderFromSite(w http.ResponseWriter, r *http.Request) {
	siteID, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var body struct {
		NameOverride string          `json:"name_override"`
		APIKey       string          `json:"api_key"`
		OAuth        json.RawMessage `json:"oauth"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}

	// Validate auth matches site auth_mode
	site, err := a.store.GetSite(siteID)
	if err != nil {
		writeError(w, http.StatusNotFound, "site not found: "+err.Error())
		return
	}
	if site.AuthMode == "api_key" && body.APIKey == "" {
		writeError(w, http.StatusBadRequest, "api_key is required for this site")
		return
	}
	if site.AuthMode == "oauth" && len(body.OAuth) == 0 {
		writeError(w, http.StatusBadRequest, "oauth credentials are required for this site")
		return
	}

	provider, mappingsCreated, err := a.store.CreateProviderFromSite(siteID, body.NameOverride, body.APIKey, body.OAuth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create provider from site: "+err.Error())
		return
	}

	// Register to Registry
	a.registerProviderInstance(provider)

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"provider":         provider,
		"mappings_created": mappingsCreated,
	})
}
