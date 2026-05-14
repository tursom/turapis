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

	if unified.Stream {
		g.handleStreamCompletions(w, r, unified)
		return
	}

	// 非流式路由
	result, err := g.router.Route(r.Context(), unified)
	if err != nil {
		slog.Error("route_failed", "path", "/v1/chat/completions", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}

	// 转回 OpenAI 格式
	resp := translate.OpenAIResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) handleStreamCompletions(w http.ResponseWriter, r *http.Request, unified *models.UnifiedRequest) {
	events, err := g.router.RouteStream(r.Context(), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/chat/completions", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}

	if err := sse.StreamEvents(w, r, events); err != nil {
		slog.Error("stream_events_failed", "error", err)
	}
}
