package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/sse"
	"github.com/tursom/turapis/internal/translate"
)

func (g *Gateway) handleMessages(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":"cannot read body"}`, http.StatusBadRequest)
		return
	}

	r = r.WithContext(models.WithClientHeaders(r.Context(), r.Header))

	if c := collectorFromContext(r.Context()); c != nil {
		c.SetClientBody(string(bodyBytes))
	}

	var anthropicReq translate.AnthropicReq
	if err := json.Unmarshal(bodyBytes, &anthropicReq); err != nil {
		slog.Warn("invalid_anthropic_request", "remote", r.RemoteAddr, "body", string(bodyBytes), "error", err)
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
		g.handleStreamMessages(w, r.WithContext(ctxWithAttemptRecorder(r.Context())), unified)
		return
	}

	if code, body := checkModelAllowed(r.Context(), unified.Model); code != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		w.Write([]byte(body))
		return
	}

	// 非流式路由
	result, err := g.router.Route(ctxWithAttemptRecorder(r.Context()), unified)
	if err != nil {
		slog.Error("route_failed", "path", "/v1/messages", "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
		http.Error(w, `{"error":{"type":"api_error","message":"`+err.Error()+`"}}`, http.StatusInternalServerError)
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

	// 转回 Anthropic 格式
	resp := translate.AnthropicResponseFromUnified(result.Response)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (g *Gateway) handleStreamMessages(w http.ResponseWriter, r *http.Request, unified *models.UnifiedRequest) {
	streamResult, err := g.router.RouteStream(ctxWithAttemptRecorder(r.Context()), unified)
	if err != nil {
		slog.Error("stream_route_failed", "path", "/v1/messages", "error", err)
		if c := collectorFromContext(r.Context()); c != nil {
			c.SetError(err.Error())
		}
		http.Error(w, `{"error":{"type":"api_error","message":"`+err.Error()+`"}}`, http.StatusInternalServerError)
		return
	}
	if c := collectorFromContext(r.Context()); c != nil {
		c.SetModel(unified.Model)
		c.SetProvider(streamResult.ProviderName)
		c.SetQuota(streamResult.QuotaBefore, streamResult.QuotaAfter)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// 发送 message_start
	sse.WriteSSEData(w, `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"`+unified.Model+`","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}}`)
	flusher.Flush()

	blockIndex := 0
	toolBlockIndices := make(map[string]int) // tool call ID -> block index
	var activeBlockType string               // "text" or "tool_use"
	anyBlock := false

	closeBlock := func() {
		if !anyBlock || activeBlockType == "" {
			return
		}
		// 找到当前活跃的 block index
		idx := blockIndex - 1
		sse.WriteSSEData(w, fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, idx))
		flusher.Flush()
		activeBlockType = ""
	}

	sendContentBlockStart := func(blockType, id, name string) {
		var b strings.Builder
		b.WriteString(fmt.Sprintf(`{"type":"content_block_start","index":%d,"content_block":{"type":"%s"`, blockIndex, blockType))
		if id != "" {
			b.WriteString(fmt.Sprintf(`,"id":"%s"`, id))
		}
		if name != "" {
			b.WriteString(fmt.Sprintf(`,"name":"%s"`, name))
		}
		b.WriteString("}}")
		sse.WriteSSEData(w, b.String())
		flusher.Flush()
		blockIndex++
	}

	for event := range streamResult.Events {
		select {
		case <-r.Context().Done():
			return
		default:
		}

		switch event.Type {
		case models.StreamEventDelta:
			// 处理 stop_reason
			if event.StopReason != "" {
				closeBlock()
				anyBlock = false
				sse.WriteSSEData(w, `{"type":"message_delta","delta":{"stop_reason":"`+mapStopReasonForSSE(event.StopReason)+`"},"usage":{"output_tokens":0}}`)
				flusher.Flush()
				continue
			}

			// 处理文本 delta
			if event.Content != "" {
				if activeBlockType != "text" {
					closeBlock()
					sendContentBlockStart("text", "", "")
					activeBlockType = "text"
				}
				sse.WriteSSEData(w, fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":"%s"}}`, blockIndex-1, escapeJSON(event.Content)))
				flusher.Flush()
				anyBlock = true
			}

			// 处理 tool call delta
			for _, tc := range event.ToolCalls {
				if tc.ID != "" && tc.Function != nil {
					// 新的 tool_use 块开始
					if activeBlockType != "" {
						closeBlock()
					}
					sendContentBlockStart("tool_use", tc.ID, tc.Function.Name)
					activeBlockType = "tool_use"
					anyBlock = true
					idx := blockIndex - 1
					toolBlockIndices[tc.ID] = idx
					// 如果有初始 arguments，发送 input_json_delta
					if tc.Function.Arguments != "" {
						sse.WriteSSEData(w, fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"%s"}}`, idx, escapeJSON(tc.Function.Arguments)))
						flusher.Flush()
					}
				} else if tc.Function != nil && tc.Function.Arguments != "" {
					// 已有 tool_use 块的 arguments delta
					idx := blockIndex - 1
					// 尝试从已注册的 toolBlockIndices 中查找
					// 默认使用最后一个 active block
					sse.WriteSSEData(w, fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":"%s"}}`, idx, escapeJSON(tc.Function.Arguments)))
					flusher.Flush()
				}
			}

		case models.StreamEventStop:
			closeBlock()
			anyBlock = false
			sse.WriteSSEData(w, `{"type":"message_stop"}`)
			flusher.Flush()
			return

		case models.StreamEventError:
			sse.WriteSSEError(w, event.Error)
			flusher.Flush()
			return
		}
	}

	// 通道提前关闭（上游断连等），发送 message_stop 作为兜底
	select {
	case <-r.Context().Done():
		return
	default:
	}
	closeBlock()
	sse.WriteSSEData(w, `{"type":"message_stop"}`)
	flusher.Flush()
}

func mapStopReasonForSSE(reason string) string {
	switch reason {
	case "tool_calls":
		return "tool_use"
	default:
		return reason
	}
}

func escapeJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}
