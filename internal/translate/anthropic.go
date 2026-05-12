package translate

import (
	"encoding/json"
	"fmt"

	"github.com/tursom/turapis/internal/models"
)

// --- Anthropic Messages 请求/响应类型（手写结构体，v1 text-only） ---

// AnthropicReq Anthropic Messages API 请求
type AnthropicReq struct {
	Model       string         `json:"model"`
	Messages    []AnthropicMsg `json:"messages"`
	System      string         `json:"system,omitempty"`
	MaxTokens   int            `json:"max_tokens"`
	Temperature *float64       `json:"temperature,omitempty"`
	TopP        *float64       `json:"top_p,omitempty"`
	StopSeq     []string       `json:"stop_sequences,omitempty"`
	Stream      bool           `json:"stream"`

	// 高级特性检测用
	Tools       json.RawMessage `json:"tools,omitempty"`
	ToolChoice  json.RawMessage `json:"tool_choice,omitempty"`
	Thinking    json.RawMessage `json:"thinking,omitempty"`
}

// AnthropicMsg Anthropic 消息
type AnthropicMsg struct {
	Role    string              `json:"role"`
	Content []AnthropicContent  `json:"content"`
}

// AnthropicContent Anthropic 内容块
type AnthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AnthropicResp Anthropic Messages API 响应
type AnthropicResp struct {
	ID         string           `json:"id"`
	Model      string           `json:"model"`
	Role       string           `json:"role"`
	Content    []AnthropicContent `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      AnthropicUsage   `json:"usage"`
}

// AnthropicUsage Anthropic 用量
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// --- Anthropic SSE 事件类型 ---

// AnthropicSSEEvent Anthropic SSE 事件
type AnthropicSSEEvent struct {
	Event string          `json:"-"`
	Data  json.RawMessage `json:"-"`
}

// AnthropicSSEDelta content_block_delta 事件 data
type AnthropicSSEDelta struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

// AnthropicSSEStop message_stop 事件 data
type AnthropicSSEStop struct {
	Type string `json:"type"`
}

// --- 转换函数 ---

// AnthropicRequestToUnified 将 Anthropic 请求转为内部统一格式
// 检测高级特性（tools/thinking），返回 ErrUnsupportedFeature
func AnthropicRequestToUnified(req *AnthropicReq) (*models.UnifiedRequest, error) {
	// AC10: 检测不支持的高级特性
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("%w: tools/tool_use", models.ErrUnsupportedFeature)
	}
	if len(req.ToolChoice) > 0 {
		return nil, fmt.Errorf("%w: tool_choice", models.ErrUnsupportedFeature)
	}
	if len(req.Thinking) > 0 {
		return nil, fmt.Errorf("%w: thinking", models.ErrUnsupportedFeature)
	}
	// 检查 content 中的非 text 块
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.Type != "text" && block.Type != "" {
				return nil, fmt.Errorf("%w: content type %q", models.ErrUnsupportedFeature, block.Type)
			}
		}
	}

	unified := &models.UnifiedRequest{
		Model:       req.Model,
		System:      req.System,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSeq,
		Stream:      req.Stream,
	}

	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.Type == "text" || block.Type == "" {
				unified.Messages = append(unified.Messages, models.UnifiedMessage{
					Role:    msg.Role,
					Content: block.Text,
				})
			}
		}
	}

	return unified, nil
}

// AnthropicResponseFromUnified 将内部统一格式转为 Anthropic 响应
func AnthropicResponseFromUnified(resp *models.UnifiedResponse) *AnthropicResp {
	return &AnthropicResp{
		ID:    resp.ID,
		Model: resp.Model,
		Role:  resp.Role,
		Content: []AnthropicContent{
			{Type: "text", Text: resp.Content},
		},
		StopReason: mapAnthropicStopReason(resp.StopReason),
		Usage: AnthropicUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}
}

// AnthropicStreamEventToUnified 将 Anthropic SSE 原始事件转为 UnifiedStreamEvent
func AnthropicStreamEventToUnified(eventType string, data []byte) (*models.UnifiedStreamEvent, error) {
	switch eventType {
	case "content_block_delta":
		var delta AnthropicSSEDelta
		if err := json.Unmarshal(data, &delta); err != nil {
			return nil, err
		}
		if delta.Delta.Type == "text_delta" && delta.Delta.Text != "" {
			return &models.UnifiedStreamEvent{
				Type:    models.StreamEventDelta,
				Content: delta.Delta.Text,
			}, nil
		}
		return nil, nil // 非文本 delta 忽略

	case "content_block_stop":
		return nil, nil // 忽略

	case "message_delta":
		var msgDelta struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(data, &msgDelta); err != nil {
			return nil, err
		}
		if msgDelta.Delta.StopReason != "" {
			return &models.UnifiedStreamEvent{
				Type:       models.StreamEventDelta,
				StopReason: mapAnthropicStopReason(msgDelta.Delta.StopReason),
			}, nil
		}
		return nil, nil

	case "message_stop":
		return &models.UnifiedStreamEvent{Type: models.StreamEventStop}, nil

	case "error":
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &errBody); err != nil {
			return &models.UnifiedStreamEvent{Type: models.StreamEventError, Error: fmt.Errorf("upstream error: unknown")}, nil
		}
		return &models.UnifiedStreamEvent{Type: models.StreamEventError, Error: fmt.Errorf("upstream error: %s", errBody.Error.Message)}, nil

	default:
		return nil, nil
	}
}

func mapAnthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return reason
	}
}
