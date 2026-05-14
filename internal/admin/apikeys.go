package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

// POST /admin/api-keys
func (a *Admin) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	key, err := a.store.CreateAPIKey(body.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "create api key: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, key)
}

// GET /admin/api-keys
func (a *Admin) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := a.store.ListAPIKeys()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 脱敏：key 字段只显示前 5 后 4 字符
	type maskedKey struct {
		ID        int    `json:"id"`
		Key       string `json:"key"`
		Name      string `json:"name"`
		Enabled   bool   `json:"enabled"`
		CreatedAt string `json:"created_at"`
	}

	masked := make([]maskedKey, len(keys))
	for i, k := range keys {
		keyPreview := k.Key
		if len(keyPreview) > 12 {
			keyPreview = keyPreview[:5] + "****" + keyPreview[len(keyPreview)-4:]
		}
		masked[i] = maskedKey{
			ID:        k.ID,
			Key:       keyPreview,
			Name:      k.Name,
			Enabled:   k.Enabled,
			CreatedAt: k.CreatedAt,
		}
	}

	writeJSON(w, http.StatusOK, masked)
}

// DELETE /admin/api-keys/{id}
func (a *Admin) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	if err := a.store.RevokeAPIKey(id); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke api key: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
