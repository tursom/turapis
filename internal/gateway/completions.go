package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/sse"
	"github.com/tursom/turapis/internal/translate"
)

func (g *Gateway) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"cannot read body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	r = r.WithContext(models.WithClientHeaders(r.Context(), r.Header))

	if c := collectorFromContext(r.Context()); c != nil {
		c.SetClientBody(string(bodyBytes))
	}

	var openaiReq translate.OpenAIReq
	if err := json.Unmarshal(bodyBytes, &openaiReq); err != nil {
		slog.Warn("invalid_openai_request", "remote", r.RemoteAddr, "body", string(bodyBytes), "error", err)
		http.Error(w, `{"error":{"message":"invalid request body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	// 转为统一格式
	unified, err := translate.OpenAIRequestToUnified(&openaiReq)
	if err != nil {
		slog.Warn("unsupported_feature", "path", "/v1/chat/completions", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"unsupported_feature"}}`, http.StatusBadRequest)
		return
	}
	unified.OriginalPath = r.URL.Path

	if code, body := checkModelAllowed(r.Context(), unified.Model); code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
		return
	}

	if unified.Stream {
		g.handleStreamCompletions(w, r.WithContext(ctxWithAttemptRecorder(models.WithRawBody(r.Context(), bodyBytes))), unified)
		return
	}

	ctx := ctxWithAttemptRecorder(models.WithRawBody(r.Context(), bodyBytes))
	result, err := g.router.Route(ctx, unified)
	if err != nil {
		slog.Error("route_failed", "path", "/v1/chat/completions", "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}
	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(unified.Model)
		c.SetProvider(result.UsedProvider)
		c.SetTokens(result.Response.Usage.InputTokens, result.Response.Usage.OutputTokens)
		c.SetQuota(result.QuotaBefore, result.QuotaAfter)
		if b, err := json.Marshal(result.Response); err == nil {
			c.SetUpstreamResp(string(b))
		}
	}

	// 转回 OpenAI 格式
	resp := translate.OpenAIResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) handleStreamCompletions(w http.ResponseWriter, r *http.Request, unified *models.UnifiedRequest) {
	streamResult, err := g.router.RouteStream(ctxWithAttemptRecorder(r.Context()), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/chat/completions", "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}
	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(unified.Model)
		c.SetProvider(streamResult.ProviderName)
		c.SetQuota(streamResult.QuotaBefore, streamResult.QuotaAfter)
	}
	filtered := make(chan models.UnifiedStreamEvent, 8)
	go func() {
		defer close(filtered)
		for ev := range streamResult.Events {
			if ev.Type == models.StreamEventUsage {
				if c := collectorFromContext(r.Context()); c != nil && ev.Usage != nil {
					c.SetTokens(ev.Usage.InputTokens, ev.Usage.OutputTokens)
				}
				continue
			}
			filtered <- ev
		}
	}()
	if err := sse.StreamEvents(w, r, filtered); err != nil {
		slog.Error("stream_events_failed", "error", err)
	}
}
