package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/translate"
)

func (g *Gateway) handleResponses(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"cannot read body","type":"invalid_request_error"}}`, http.StatusBadRequest)
		return
	}

	if c := collectorFromContext(r.Context()); c != nil {
		c.SetClientBody(string(bodyBytes))
	}

	if models.IsRawProxy(r.Context()) || strings.HasPrefix(r.URL.Path, "/v1/responses") {
		g.handleRawResponsesProxy(w, r.WithContext(models.WithRawBody(r.Context(), bodyBytes)), bodyBytes)
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
	unified.OriginalPath = r.URL.Path

	if unified.Stream {
		g.handleStreamResponses(w, r.WithContext(models.WithRawBody(r.Context(), bodyBytes)), unified)
		return
	}

	ctx := models.WithRawBody(r.Context(), bodyBytes)
	result, err := g.router.Route(ctx, unified)
	if err != nil {
		slog.Error("route_failed", "path", "/v1/responses", "error", err)
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
		if b, err := json.Marshal(unified); err == nil {
			c.SetUpstreamReq(string(b))
		}
		if b, err := json.Marshal(result.Response); err == nil {
			c.SetUpstreamResp(string(b))
		}
	}

	resp := translate.ResponsesResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type streamState struct {
	model          string
	inTok          int
	outTok         int
	outputIdx      int
	msgItemID      string
	gotText        bool
	textBuf        string
	activeCallID   string
	activeCallName string
	activeCallNS   string
	argBuf         string
	argFlushed     int
	completedCalls []map[string]interface{}
}

func (g *Gateway) handleStreamResponses(w http.ResponseWriter, r *http.Request, unified *models.UnifiedRequest) {
	streamResult, err := g.router.RouteStream(r.Context(), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/responses", "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}
	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(unified.Model)
		c.SetProvider(streamResult.ProviderName)
	}

	state := &streamState{
		model:     unified.Model,
		msgItemID: "msg_turapis_" + fmt.Sprint(time.Now().UnixNano()),
	}
	respID := "resp_turapis_" + fmt.Sprint(time.Now().UnixNano())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	respObj := map[string]interface{}{"id": respID, "status": "in_progress"}
	writeResponsesEvent(w, flusher, "response.created", map[string]interface{}{"response": respObj})
	writeResponsesEvent(w, flusher, "response.in_progress", map[string]interface{}{"response": respObj})

	msgAdded := false
	sentDone := false
	for event := range streamResult.Events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		switch event.Type {
		case models.StreamEventUsage:
			if event.Usage != nil {
				state.inTok = event.Usage.InputTokens
				state.outTok = event.Usage.OutputTokens
				if c := collectorFromContext(r.Context()); c != nil {
					c.SetTokens(event.Usage.InputTokens, event.Usage.OutputTokens)
				}
			}
		case models.StreamEventDelta:
			if event.Content != "" {
				state.gotText = true
				state.textBuf += event.Content
				if !msgAdded {
					msgAdded = true
					writeResponsesEvent(w, flusher, "response.output_item.added", map[string]interface{}{
						"output_index": state.outputIdx,
						"item": map[string]interface{}{
							"type":    "message",
							"id":      state.msgItemID,
							"role":    "assistant",
							"status":  "in_progress",
							"content": []interface{}{},
						},
					})
					writeResponsesEvent(w, flusher, "response.content_part.added", map[string]interface{}{
						"output_index":  state.outputIdx,
						"content_index": 0,
						"part": map[string]interface{}{
							"type": "output_text",
							"text": "",
						},
					})
				}
				writeResponsesEvent(w, flusher, "response.text.delta", map[string]interface{}{
					"item_id":       state.msgItemID,
					"output_index":  state.outputIdx,
					"content_index": 0,
					"delta":         event.Content,
				})
			}
			for _, tc := range event.ToolCalls {
				if tc.ID != "" && tc.Function != nil {
					isNew := state.activeCallID != tc.ID
					if isNew {
						state.activeCallID = tc.ID
						state.argBuf = ""
						state.argFlushed = 0
					}
					if tc.Function.Name != "" {
						state.activeCallName, state.activeCallNS = splitNamespaceName(tc.Function.Name)
					}
					state.argBuf += tc.Function.Arguments
					if !isNew && tc.Function.Name == "" {
						flushArgDelta(w, flusher, state, false)
						continue
					}
					state.argFlushed = 0
					item := map[string]interface{}{
						"type":      "function_call",
						"call_id":   tc.ID,
						"name":      state.activeCallName,
						"arguments": "",
						"status":    "in_progress",
					}
					if state.activeCallNS != "" {
						item["namespace"] = state.activeCallNS
					}
					writeResponsesEvent(w, flusher, "response.output_item.added", map[string]interface{}{
						"output_index": state.outputIdx,
						"item":         item,
					})
					flushArgDelta(w, flusher, state, false)
				} else if tc.Function != nil && tc.Function.Arguments != "" {
					state.argBuf += tc.Function.Arguments
					flushArgDelta(w, flusher, state, false)
				}
			}
			if event.StopReason != "" && state.activeCallID != "" {
				state.activeCallName, state.argBuf = normalizeCodexTool(state.activeCallName, state.argBuf)
				flushArgDelta(w, flusher, state, true)
				writeResponsesEvent(w, flusher, "response.function_call_arguments.done", map[string]interface{}{
					"item_id":      state.activeCallID,
					"output_index": state.outputIdx,
					"arguments":    state.argBuf,
				})
				item := map[string]interface{}{
					"type":      "function_call",
					"id":        state.activeCallID,
					"name":      state.activeCallName,
					"arguments": state.argBuf,
					"status":    "completed",
				}
				if state.activeCallNS != "" {
					item["namespace"] = state.activeCallNS
				}
				writeResponsesEvent(w, flusher, "response.output_item.done", map[string]interface{}{
					"output_index": state.outputIdx,
					"item": map[string]interface{}{
						"type":      "function_call",
						"call_id":   state.activeCallID,
						"name":      state.activeCallName,
						"arguments": state.argBuf,
						"status":    "completed",
					},
				})
				if state.activeCallNS != "" {
					item["namespace"] = state.activeCallNS
				}
				state.completedCalls = append(state.completedCalls, map[string]interface{}{
					"type":      "function_call",
					"call_id":   state.activeCallID,
					"name":      state.activeCallName,
					"arguments": state.argBuf,
				})
				state.outputIdx++
				state.activeCallID = ""
				state.activeCallName = ""
				state.activeCallNS = ""
				state.argBuf = ""
				state.argFlushed = 0
			}
		case models.StreamEventStop:
			if state.activeCallID != "" {
				state.activeCallName, state.argBuf = normalizeCodexTool(state.activeCallName, state.argBuf)
				flushArgDelta(w, flusher, state, true)
				writeResponsesEvent(w, flusher, "response.function_call_arguments.done", map[string]interface{}{
					"item_id": state.activeCallID, "output_index": state.outputIdx, "arguments": state.argBuf,
				})
				item2 := map[string]interface{}{
					"type": "function_call", "id": state.activeCallID,
					"name": state.activeCallName, "arguments": state.argBuf, "status": "completed",
				}
				if state.activeCallNS != "" {
					item2["namespace"] = state.activeCallNS
				}
				writeResponsesEvent(w, flusher, "response.output_item.done", map[string]interface{}{
					"output_index": state.outputIdx,
					"item":         item2,
				})
				state.outputIdx++
			}
			if state.gotText && !sentDone {
				writeResponsesEvent(w, flusher, "response.text.done", map[string]interface{}{
					"output_index":  0,
					"content_index": 0,
					"text":          state.textBuf,
				})
				writeResponsesEvent(w, flusher, "response.content_part.done", map[string]interface{}{
					"output_index":  0,
					"content_index": 0,
					"part": map[string]interface{}{
						"type": "output_text",
						"text": state.textBuf,
					},
				})
				writeResponsesEvent(w, flusher, "response.output_item.done", map[string]interface{}{
					"output_index": 0,
					"item": map[string]interface{}{
						"type":   "message",
						"id":     state.msgItemID,
						"role":   "assistant",
						"status": "completed",
						"content": []map[string]interface{}{{
							"type": "output_text",
							"text": state.textBuf,
						}},
					},
				})
				sentDone = true
			}
			writeResponsesEvent(w, flusher, "response.completed", buildCompleted(state, respID))
			return
		case models.StreamEventError:
			writeResponsesEvent(w, flusher, "error", map[string]interface{}{
				"error": map[string]interface{}{
					"message": event.Error.Error(),
				},
			})
			return
		}
	}

	select {
	case <-r.Context().Done():
		return
	default:
	}
	if state.gotText && !sentDone {
		writeResponsesEvent(w, flusher, "response.text.done", map[string]interface{}{
			"output_index": 0, "content_index": 0, "text": state.textBuf,
		})
		writeResponsesEvent(w, flusher, "response.content_part.done", map[string]interface{}{
			"output_index": 0, "content_index": 0,
			"part": map[string]interface{}{"type": "output_text", "text": state.textBuf},
		})
		writeResponsesEvent(w, flusher, "response.output_item.done", map[string]interface{}{
			"output_index": 0,
			"item": map[string]interface{}{
				"type": "message", "id": state.msgItemID, "role": "assistant", "status": "completed",
				"content": []map[string]interface{}{{"type": "output_text", "text": state.textBuf}},
			},
		})
	}
	writeResponsesEvent(w, flusher, "response.completed", buildCompleted(state, respID))
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event, data string) {
	w.Write([]byte("event: " + event + "\n"))
	w.Write([]byte("data: " + data + "\n\n"))
	flusher.Flush()
}

func writeResponsesEvent(w http.ResponseWriter, flusher http.Flusher, event string, fields map[string]interface{}) {
	fields["type"] = event
	payload, _ := json.Marshal(fields)
	w.Write([]byte("event: " + event + "\n"))
	w.Write([]byte("data: " + string(payload) + "\n\n"))
	flusher.Flush()
}

func mustMarshal(v interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func buildCompleted(state *streamState, respID string) map[string]interface{} {
	resp := map[string]interface{}{
		"id":    respID,
		"model": state.model,
		"usage": map[string]interface{}{
			"input_tokens":  state.inTok,
			"output_tokens": state.outTok,
			"total_tokens":  state.inTok + state.outTok,
		},
		"status": "completed",
	}
	if len(state.completedCalls) > 0 {
		resp["output"] = state.completedCalls
	}
	return map[string]interface{}{"response": resp}
}

func normalizeCodexTool(name, args string) (string, string) {
	return name, args
}

func flushArgDelta(w http.ResponseWriter, flusher http.Flusher, state *streamState, force bool) {
	if state.argFlushed >= len(state.argBuf) {
		return
	}
	if !force {
		return
	}
	delta := state.argBuf[state.argFlushed:]
	writeResponsesEvent(w, flusher, "response.function_call_arguments.delta", map[string]interface{}{
		"item_id":      state.activeCallID,
		"output_index": state.outputIdx,
		"delta":        delta,
	})
	state.argFlushed = len(state.argBuf)
}

func splitNamespaceName(fullName string) (name, ns string) {
	idx := strings.LastIndex(fullName, "__")
	if idx < 0 {
		return fullName, ""
	}
	if idx == 0 {
		return fullName[2:], ""
	}
	return fullName[:idx], fullName[idx+2:]
}

func (g *Gateway) handleRawResponsesProxy(w http.ResponseWriter, r *http.Request, bodyBytes []byte) {
	model := "gpt-5.4"
	var probe struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(bodyBytes, &probe) == nil && probe.Model != "" {
		model = probe.Model
	}
	body, providerName, err := g.router.RouteRawStream(r.Context(), model, bodyBytes)
	if err != nil {
		slog.Error("raw_proxy_failed", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}
	defer body.Close()
	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(model)
		c.SetProvider(providerName)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	io.Copy(w, body)
}
