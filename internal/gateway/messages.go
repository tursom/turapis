package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/sse"
	"github.com/tursom/turapis/internal/translate"
)

func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	var anthropicReq translate.AnthropicReq
	if err := json.NewDecoder(r.Body).Decode(&anthropicReq); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	// 转为统一格式
	unified, err := translate.AnthropicRequestToUnified(&anthropicReq)
	if err != nil {
		slog.Warn("unsupported_feature", "path", "/v1/messages", "error", err)
		http.Error(w, `{"error":{"type":"unsupported_feature","message":"`+err.Error()+`"}}`, http.StatusBadRequest)
		return
	}

	if unified.Stream {
		g.handleStreamMessages(w, r, unified)
		return
	}

	// 非流式路由
	result, err := g.router.Route(r.Context(), unified)
	if err != nil {
		slog.Error("route_failed", "path", "/v1/messages", "error", err)
		http.Error(w, `{"error":{"type":"api_error","message":"`+err.Error()+`"}}`, http.StatusInternalServerError)
		return
	}

	// 转回 Anthropic 格式
	resp := translate.AnthropicResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) handleStreamMessages(w http.ResponseWriter, r *http.Request, unified *models.UnifiedRequest) {
	events, err := g.router.RouteStream(r.Context(), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/messages", "error", err)
		http.Error(w, `{"error":{"type":"api_error","message":"`+err.Error()+`"}}`, http.StatusInternalServerError)
		return
	}

	// 流式输出 Anthropic SSE 格式
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	for event := range events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		switch event.Type {
		case models.StreamEventDelta:
			if event.StopReason != "" {
				sse.WriteSSEData(w, `{"type":"message_delta","delta":{"stop_reason":"`+event.StopReason+`"}}`)
				flusher.Flush()
			} else {
				sse.WriteSSEData(w, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"`+escapeJSON(event.Content)+`"}}`)
				flusher.Flush()
			}
		case models.StreamEventStop:
			sse.WriteSSEData(w, `{"type":"message_stop"}`)
			flusher.Flush()
			return
		case models.StreamEventError:
			sse.WriteSSEError(w, event.Error)
			flusher.Flush()
			return
		}
	}
}

func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}
