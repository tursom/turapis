package admin

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/tursom/turapis/internal/provider"
)

func (a *Admin) probeQuota(w http.ResponseWriter, r *http.Request) {
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
	if p.AuthMode != "oauth" {
		writeError(w, http.StatusBadRequest, "quota probe only supports oauth providers")
		return
	}
	at := provider.ExtractOAuthAccessToken(p.APIKey)
	if at == "" {
		writeError(w, http.StatusInternalServerError, "no access_token")
		return
	}

	payload, _ := json.Marshal(map[string]interface{}{
		"model":        "gpt-5.4",
		"instructions": "You are a helpful assistant.",
		"input": []map[string]interface{}{{
			"role": "user",
			"content": []map[string]interface{}{{"type": "input_text", "text": "hi"}},
		}},
		"stream":  true,
		"store":   false,
	})
	req, _ := http.NewRequest("POST", p.BaseURL+"/responses", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+at)
	req.Header.Set("Originator", "codex_cli_rs")
	req.Header.Set("Version", "0.101.0")
	req.Header.Set("User-Agent", "codex_cli_rs/0.101.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Connection", "Keep-Alive")
	req.Header.Set("OpenAI-Beta", "responses-2025-03-11")
	req.Header.Set("Session-Id", randomHex(16))

	tr := provider.SharedTransport()
	if p.Proxy != "" {
		tr = provider.NewTransportWithProxy(p.Proxy)
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		slog.Warn("quota_probe_failed", "provider", p.Name, "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))

	quota := provider.ParseQuota(resp.Header)
	result := map[string]interface{}{
		"provider": p.Name,
		"status":   fmt.Sprintf("http %d", resp.StatusCode),
		"quota":    quota,
	}
	if resp.StatusCode >= 400 {
		result["error"] = string(respBody)
	}

	// 持久化保存配额
	if len(quota) > 0 {
		if qj, err := json.Marshal(quota); err == nil {
			if err := a.store.SaveProviderQuota(p.ID, qj); err != nil {
				slog.Warn("save_quota_failed", "provider", p.Name, "error", err)
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

func (a *Admin) batchProbeQuota(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProviderIDs []int `json:"provider_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	providers, err := a.store.ListProviders()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	filter := make(map[int]bool)
	for _, id := range body.ProviderIDs {
		filter[id] = true
	}

	type result struct {
		Provider string                 `json:"provider"`
		Status   string                 `json:"status"`
		Quota    map[string]interface{} `json:"quota,omitempty"`
		Error    string                 `json:"error,omitempty"`
	}
	results := make([]result, 0)

	for _, p := range providers {
		if len(filter) > 0 && !filter[p.ID] {
			continue
		}
		if p.AuthMode != "oauth" {
			continue
		}
		at := provider.ExtractOAuthAccessToken(p.APIKey)
		if at == "" {
			continue
		}

		r := result{Provider: p.Name}
		payload, _ := json.Marshal(map[string]interface{}{
			"model":        "gpt-5.4",
			"instructions": "You are a helpful assistant.",
			"input":        []map[string]interface{}{{"role": "user", "content": []map[string]interface{}{{"type": "input_text", "text": "hi"}}}},
			"stream":       true,
			"store":        false,
		})
		req, _ := http.NewRequest("POST", p.BaseURL+"/responses", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+at)
		req.Header.Set("Originator", "codex_cli_rs")
		req.Header.Set("Version", "0.101.0")
		req.Header.Set("User-Agent", "codex_cli_rs/0.101.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Connection", "Keep-Alive")
		req.Header.Set("OpenAI-Beta", "responses-2025-03-11")
		req.Header.Set("Session-Id", randomHex(16))

		tr := provider.SharedTransport()
		if p.Proxy != "" {
			tr = provider.NewTransportWithProxy(p.Proxy)
		}
		resp, err := (&http.Client{Transport: tr, Timeout: 30 * time.Second}).Do(req)
		if err != nil {
			r.Error = err.Error()
			results = append(results, r)
			continue
		}
		resp.Body.Close()

		q := provider.ParseQuota(resp.Header)
		r.Status = fmt.Sprintf("http %d", resp.StatusCode)
		r.Quota = q

		if len(q) > 0 {
			if qj, err := json.Marshal(q); err == nil {
				if err := a.store.SaveProviderQuota(p.ID, qj); err != nil {
					slog.Warn("save_quota_failed", "provider", p.Name, "error", err)
				}
			}
		}
		results = append(results, r)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"results": results,
		"total":   len(results),
	})
}

func randomHex(n int) string {
	b := make([]byte, n/2)
	rand.Read(b)
	return hex.EncodeToString(b)
}
