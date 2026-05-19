package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/tursom/turapis/internal/models"
)

func (a *Admin) listUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.store.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (a *Admin) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	body.Role = strings.TrimSpace(body.Role)

	if body.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if body.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	if body.Role != "admin" && body.Role != "user" {
		writeError(w, http.StatusBadRequest, "role must be 'admin' or 'user'")
		return
	}

	id, err := a.store.CreateUser(body.Username, body.Password, body.Role)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	user, err := a.store.GetUser(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, user)
}

func (a *Admin) getUser(w http.ResponseWriter, r *http.Request) {
	param := chi.URLParam(r, "id")
	if param == "me" {
		u := models.SessionUserFromContext(r.Context())
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not authenticated")
			return
		}
		user, err := a.store.GetUser(u.UserID)
		if err != nil {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, user)
		return
	}

	id, err := strconv.ParseInt(param, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	user, err := a.store.GetUser(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (a *Admin) updateUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		Enabled  *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	existing, err := a.store.GetUser(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	username := existing.Username
	enabled := existing.Enabled
	role := existing.Role

	if body.Username != "" {
		username = strings.TrimSpace(body.Username)
	}
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.Role != "" {
		role = strings.TrimSpace(body.Role)
		if role != "admin" && role != "user" {
			writeError(w, http.StatusBadRequest, "role must be 'admin' or 'user'")
			return
		}
	}

	if err := a.store.UpdateUser(id, username, enabled, role, body.Password); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	updated, _ := a.store.GetUser(id)
	writeJSON(w, http.StatusOK, updated)
}

func (a *Admin) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	u := models.SessionUserFromContext(r.Context())
	if u != nil && u.UserID == id {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}

	if err := a.store.DeleteUser(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
