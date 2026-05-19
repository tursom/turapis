package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/tursom/turapis/internal/config"
)

func (a *Admin) getAccessLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	log, err := a.store.GetAccessLog(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, log)
}

func (a *Admin) listAccessLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := config.AccessLogQuery{}

	if v := q.Get("key_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			params.ApiKeyID = &id
		}
	}
	params.Model = q.Get("model")
	if v := q.Get("status"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			params.Status = &s
		}
	}
	params.StartAt = q.Get("from")
	params.EndAt = q.Get("to")

	params.Page = 1
	if v := q.Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			params.Page = p
		}
	}
	params.PerPage = 20
	if v := q.Get("per_page"); v != "" {
		if pp, err := strconv.Atoi(v); err == nil && pp > 0 && pp <= 100 {
			params.PerPage = pp
		}
	}

	logs, total, err := a.store.QueryAccessLogs(params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query access logs: "+err.Error())
		return
	}
	for i := range logs {
		logs[i].ClientReq = ""
		logs[i].ClientResp = ""
		logs[i].UpstreamReq = ""
		logs[i].UpstreamResp = ""
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"logs":     logs,
		"total":    total,
		"page":     params.Page,
		"per_page": params.PerPage,
	})
}
