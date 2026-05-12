package gateway

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/translate"
)

func (g *Gateway) handleResponses(w http.ResponseWriter, r *http.Request) {
	var respReq translate.ResponsesReq
	if err := json.NewDecoder(r.Body).Decode(&respReq); err != nil {
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

	for event := range events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		switch event.Type {
		case models.StreamEventDelta:
			writeSSELine(w, "event: response.output_text.delta")
			writeSSELine(w, "data: "+mustMarshal(map[string]string{"delta": event.Content}))
			writeSSELine(w, "")
			flusher.Flush()
		case models.StreamEventStop:
			writeSSELine(w, "event: response.completed")
			writeSSELine(w, "data: {\"response\":{}}")
			writeSSELine(w, "")
			flusher.Flush()
			return
		case models.StreamEventError:
			writeSSELine(w, "event: error")
			writeSSELine(w, "data: {\"error\":{\"message\":\""+escapeJSON(event.Error.Error())+"\"}}")
			writeSSELine(w, "")
			flusher.Flush()
			return
		}
	}
}

func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func writeSSELine(w http.ResponseWriter, s string) {
	if s == "" {
		w.Write([]byte("\n"))
	} else {
		w.Write([]byte(s + "\n"))
	}
}
