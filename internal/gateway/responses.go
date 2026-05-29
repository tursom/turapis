package gateway

import (
	"bytes"
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

	r = r.WithContext(models.WithClientHeaders(r.Context(), r.Header))

	if c := collectorFromContext(r.Context()); c != nil {
		c.SetClientBody(string(bodyBytes))
	}

	if shouldUseRawResponsesProxy(r) {
		g.handleRawResponsesProxy(w, r, bodyBytes)
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

	if code, body := checkModelAllowed(r.Context(), unified.Model); code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
		return
	}

	if unified.Stream {
		g.handleStreamResponses(w, r.WithContext(ctxWithAttemptRecorder(models.WithRawBody(r.Context(), bodyBytes))), unified)
		return
	}

	ctx := ctxWithAttemptRecorder(models.WithRawBody(r.Context(), bodyBytes))
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
		c.SetQuota(result.QuotaBefore, result.QuotaAfter)
		if b, err := json.Marshal(result.Response); err == nil {
			c.SetUpstreamResp(string(b))
		}
	}

	resp := translate.ResponsesResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func shouldUseRawResponsesProxy(r *http.Request) bool {
	return r.URL.Path == "/v1/responses" && models.CodexVersionFromContext(r.Context()) != ""
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
	streamResult, err := g.router.RouteStream(ctxWithAttemptRecorder(r.Context()), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/responses", "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}

	// codex Responses API 原始 SSE 透传路径：
	// 当 ChatCompletionStream 检测到上游是 codex 且请求路径为 /v1/responses 时，
	// 会将上游原始 SSE 响应体存入 StreamRouteResult.RawBody。
	// 此处检测到 RawBody 后直接 pipe 给客户端，不再走 unified event 解析-重建，
	// 避免 event 丢失导致 codex CLI 工具执行失败。
	if streamResult.RawBody != nil {
		defer streamResult.RawBody.Close()
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetModel(unified.Model)
			c.SetProvider(streamResult.ProviderName)
			c.SetQuota(streamResult.QuotaBefore, streamResult.QuotaAfter)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		var flusher http.Flusher
		if f, ok := w.(http.Flusher); ok {
			flusher = f
		}
		if err := copyRawResponsesProxy(w, flusher, streamResult.RawBody, collectorFromContext(r.Context())); err != nil {
			slog.Warn("raw_stream_copy_failed", "provider", streamResult.ProviderName, "error", err)
		}
		return
	}

	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(unified.Model)
		c.SetProvider(streamResult.ProviderName)
		c.SetQuota(streamResult.QuotaBefore, streamResult.QuotaAfter)
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

	ctx := ctxWithAttemptRecorder(r.Context())

	result, err := g.router.RouteRawStream(ctx, model, bodyBytes)
	if err != nil {
		slog.Error("raw_proxy_failed", "error", err)
		http.Error(w, `{"error":{"message":"`+err.Error()+`","type":"api_error"}}`, http.StatusInternalServerError)
		return
	}
	defer result.Body.Close()
	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(model)
		c.SetProvider(result.ProviderName)
		c.SetQuota(result.QuotaBefore, result.QuotaAfter)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	var flusher http.Flusher
	if f, ok := w.(http.Flusher); ok {
		flusher = f
	}
	if err := copyRawResponsesProxy(w, flusher, result.Body, collectorFromContext(r.Context())); err != nil {
		slog.Warn("raw_proxy_copy_failed", "provider", result.ProviderName, "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
	}
}

func copyRawResponsesProxy(w io.Writer, flusher http.Flusher, body io.Reader, collector *AccessLogCollector) error {
	parser := rawResponsesUsageParser{collector: collector}
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			parser.Write(chunk)
			if _, err := w.Write(chunk); err != nil {
				parser.Close()
				return err
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			parser.Close()
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

type rawResponsesUsageParser struct {
	collector *AccessLogCollector
	line      []byte
	event     string
	dataLines []string
}

func (p *rawResponsesUsageParser) Write(chunk []byte) {
	if p.collector == nil || len(chunk) == 0 {
		return
	}
	for len(chunk) > 0 {
		idx := bytes.IndexByte(chunk, '\n')
		if idx < 0 {
			p.line = append(p.line, chunk...)
			return
		}
		p.line = append(p.line, chunk[:idx]...)
		p.processLine(p.line)
		p.line = p.line[:0]
		chunk = chunk[idx+1:]
	}
}

func (p *rawResponsesUsageParser) Close() {
	if p.collector == nil {
		return
	}
	if len(p.line) > 0 {
		p.processLine(p.line)
		p.line = nil
	}
	p.dispatch()
}

func (p *rawResponsesUsageParser) processLine(raw []byte) {
	line := strings.TrimSuffix(string(raw), "\r")
	if line == "" {
		p.dispatch()
		return
	}
	if strings.HasPrefix(line, "event:") {
		p.event = trimSSEFieldValue(strings.TrimPrefix(line, "event:"))
		return
	}
	if strings.HasPrefix(line, "data:") {
		p.dataLines = append(p.dataLines, trimSSEFieldValue(strings.TrimPrefix(line, "data:")))
	}
}

func (p *rawResponsesUsageParser) dispatch() {
	if len(p.dataLines) == 0 {
		p.event = ""
		return
	}
	data := strings.Join(p.dataLines, "\n")
	p.captureUsage(p.event, data)
	p.event = ""
	p.dataLines = nil
}

func (p *rawResponsesUsageParser) captureUsage(event, data string) {
	if data == "" || data == "[DONE]" {
		return
	}
	if event != "" && event != "response.completed" {
		return
	}
	if event == "" && !strings.Contains(data, `"response.completed"`) {
		return
	}
	var payload struct {
		Type     string `json:"type"`
		Response struct {
			Model string `json:"model"`
			Usage *struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage,omitempty"`
		} `json:"response"`
	}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return
	}
	if payload.Type != "" && payload.Type != "response.completed" && event != "response.completed" {
		return
	}
	if payload.Response.Usage == nil {
		return
	}
	if payload.Response.Model != "" {
		p.collector.SetModel(payload.Response.Model)
	}
	p.collector.SetTokens(payload.Response.Usage.InputTokens, payload.Response.Usage.OutputTokens)
}

func trimSSEFieldValue(v string) string {
	if strings.HasPrefix(v, " ") {
		return v[1:]
	}
	return v
}
