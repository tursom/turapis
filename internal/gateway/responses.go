package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/translate"
)

func (g *Gateway) handleResponses(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"cannot read body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	var respReq translate.ResponsesReq
	if err := json.Unmarshal(bodyBytes, &respReq); err != nil {
		slog.Warn("invalid_responses_request", "remote", r.RemoteAddr, "body", string(bodyBytes), "error", err)
		http.Error(w, `{"error":{"message":"invalid request body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	unified, err := translate.ResponsesRequestToUnified(&respReq)
	if err != nil {
		slog.Warn("unsupported_feature", "path", "/v1/responses", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"unsupported_feature"}}`, http.StatusBadRequest)
		return
	}

	if unified.Stream {
		g.handleStreamResponses(w, r, unified)
		return
	}

	result, err := g.router.Route(r.Context(), unified)
	if err != nil {
		slog.Error("route_failed", "path", "/v1/responses", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}

	resp := translate.ResponsesResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) handleStreamResponses(w http.ResponseWriter, r *http.Request, unified *models.UnifiedRequest) {
	events, err := g.router.RouteStream(r.Context(), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/responses", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// 发送初始生命周期事件
	writeSSEEvent(w, flusher, "response.created", `{"response":{"id":"resp_turapis","status":"in_progress"}}`)
	writeSSEEvent(w, flusher, "response.in_progress", `{}`)

	for event := range events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		switch event.Type {
		case models.StreamEventDelta:
			writeSSEEvent(w, flusher, "response.text.delta", `{"delta":`+mustMarshal(event.Content)+`}`)
		case models.StreamEventStop:
			writeSSEEvent(w, flusher, "response.text.done", `{}`)
			writeSSEEvent(w, flusher, "response.output_item.done", `{}`)
			writeSSEEvent(w, flusher, "response.completed", `{"response":{}}`)
			return
		case models.StreamEventError:
			writeSSEEvent(w, flusher, "error", `{"error":{"message":`+mustMarshal(event.Error.Error())+`}}`)
			return
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	w.Write([]byte("event: " + event + "\n"))
	w.Write([]byte("data: " + data + "\n\n"))
	flusher.Flush()
}

func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}
