package sse

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tursom/turapis/internal/models"
)

// SSEEvent 原始 SSE 事件
type SSEEvent struct {
	Event string // event: 行
	Data  string // data: 行（可能多行合并）
}

// DecodeSSEStream 从 io.Reader 解码 SSE 流，返回事件 channel
// 支持多行 data 事件：累积非空行，遇到空行触发完整事件
func DecodeSSEStream(r io.Reader) <-chan SSEEvent {
	ch := make(chan SSEEvent, 64)
	go func() {
		defer close(ch)
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB buffer

		var eventType string
		var dataLines []string

		for scanner.Scan() {
			line := scanner.Text()

			if line == "" {
				// 空行触发完整事件
				if len(dataLines) > 0 {
					ch <- SSEEvent{
						Event: eventType,
						Data:  strings.Join(dataLines, "\n"),
					}
				}
				eventType = ""
				dataLines = nil
				continue
			}

			if strings.HasPrefix(line, "event: ") {
				eventType = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				dataLines = append(dataLines, strings.TrimPrefix(line, "data: "))
			}
		}

		// flush 未完成的最后一个事件
		if len(dataLines) > 0 {
			ch <- SSEEvent{
				Event: eventType,
				Data:  strings.Join(dataLines, "\n"),
			}
		}
	}()
	return ch
}

// WriteSSEData 写入 SSE data 行
func WriteSSEData(w io.Writer, content string) error {
	_, err := fmt.Fprintf(w, "data: %s\n\n", content)
	return err
}

// WriteSSEDone 写入 SSE 结束标记
func WriteSSEDone(w io.Writer) error {
	_, err := fmt.Fprint(w, "data: [DONE]\n\n")
	return err
}

// WriteSSEError 写入 SSE 错误事件
func WriteSSEError(w io.Writer, err error) error {
	msg := err.Error()
	if msg == "" {
		msg = "unknown error"
	}
	_, writeErr := fmt.Fprintf(w, "event: error\ndata: {\"error\": %q}\n\n", msg)
	return writeErr
}

// FlushWriter 支持 Flush 的 writer（http.ResponseWriter 实现 http.Flusher 时）
type FlushWriter interface {
	http.ResponseWriter
	http.Flusher
}

// StreamEvents 将 UnifiedStreamEvent channel 写入 SSE HTTP 响应
func StreamEvents(w http.ResponseWriter, r *http.Request, events <-chan models.UnifiedStreamEvent) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	for event := range events {
		select {
		case <-r.Context().Done():
			return nil
		default:
		}

		switch event.Type {
		case models.StreamEventDelta:
			WriteSSEData(w, formatDelta(event.Content, event.StopReason, event.ToolCalls))
			flusher.Flush()
		case models.StreamEventStop:
			WriteSSEDone(w)
			flusher.Flush()
			return nil
		case models.StreamEventError:
			WriteSSEError(w, event.Error)
			flusher.Flush()
			return event.Error
		}
	}

	// 通道提前关闭（上游断连等），发送 [DONE] 作为兜底
	select {
	case <-r.Context().Done():
		return nil
	default:
	}
	WriteSSEDone(w)
	flusher.Flush()
	return nil
}

type sseChunk struct {
	Choices []sseChoice `json:"choices"`
}

type sseChoice struct {
	Index        int                    `json:"index"`
	Delta        sseDelta              `json:"delta"`
	FinishReason string                 `json:"finish_reason,omitempty"`
}

type sseDelta struct {
	Role      string                 `json:"role,omitempty"`
	Content   string                 `json:"content,omitempty"`
	ToolCalls []models.ToolCallDelta `json:"tool_calls,omitempty"`
}

func formatDelta(content, stopReason string, toolCalls []models.ToolCallDelta) string {
	chunk := sseChunk{
		Choices: []sseChoice{{
			Index: 0,
			Delta: sseDelta{
				Content:   content,
				ToolCalls: toolCalls,
			},
		}},
	}
	if stopReason != "" {
		chunk.Choices[0].FinishReason = stopReason
	}
	b, _ := json.Marshal(chunk)
	return string(b)
}
