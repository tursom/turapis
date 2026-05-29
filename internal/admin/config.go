package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
)

var readOnlySettings = map[string]bool{
	"schema_version":    true,
	"codex_cli_version": true,
}

func (a *Admin) getConfig(w http.ResponseWriter, r *http.Request) {
	settings, err := a.store.GetAllSettings()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get settings: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (a *Admin) updateConfig(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if body.Key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if readOnlySettings[body.Key] {
		writeError(w, http.StatusBadRequest, "setting '"+body.Key+"' is read-only")
		return
	}

	switch body.Key {
	case "failover_error_cooldown_growth_seconds":
		v, err := strconv.Atoi(body.Value)
		if err != nil || v < 0 {
			writeError(w, http.StatusBadRequest, "failover_error_cooldown_growth_seconds must be a non-negative integer")
			return
		}
	case "default_priority_chain":
		if !json.Valid([]byte(body.Value)) {
			writeError(w, http.StatusBadRequest, "default_priority_chain must be valid JSON")
			return
		}
	case "access_log_save_bodies":
		if _, err := strconv.ParseBool(body.Value); err != nil {
			writeError(w, http.StatusBadRequest, "access_log_save_bodies must be a boolean")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "unknown setting: "+body.Key)
		return
	}

	if err := a.store.SetSetting(body.Key, body.Value); err != nil {
		writeError(w, http.StatusInternalServerError, "save setting: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}
