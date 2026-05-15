package translate

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/tursom/turapis/internal/models"
)

// --- Anthropic Messages 请求/响应类型 ---

// AnthropicReq Anthropic Messages API 请求
type AnthropicReq struct {
	Model       string          `json:"model"`
	Messages    []AnthropicMsg  `json:"messages"`
	System      json.RawMessage `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	StopSeq     []string        `json:"stop_sequences,omitempty"`
	Stream      bool            `json:"stream"`

	// 高级特性
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	Thinking   json.RawMessage `json:"thinking,omitempty"`
}

// GetSystem 提取 system 字段，支持字符串和内容块数组两种格式
func (req *AnthropicReq) GetSystem() string {
	if len(req.System) == 0 {
		return ""
	}
	if req.System[0] == '"' {
		var s string
		json.Unmarshal(req.System, &s)
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(req.System, &blocks); err != nil {
		return ""
	}
	var out string
	for _, b := range blocks {
		if b.Type == "text" {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// AnthropicMsg Anthropic 消息
type AnthropicMsg struct {
	Role    string             `json:"role"`
	Content []AnthropicContent `json:"-"`
}

// UnmarshalJSON 处理 content 为字符串或数组两种格式
func (m *AnthropicMsg) UnmarshalJSON(data []byte) error {
	var raw struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role

	if len(raw.Content) == 0 {
		m.Content = nil
		return nil
	}

	if raw.Content[0] == '"' {
		var s string
		if err := json.Unmarshal(raw.Content, &s); err != nil {
			return err
		}
		m.Content = []AnthropicContent{{Type: "text", Text: s}}
	} else {
		if err := json.Unmarshal(raw.Content, &m.Content); err != nil {
			return err
		}
	}
	return nil
}

// MarshalJSON 处理 content 序列化
func (m AnthropicMsg) MarshalJSON() ([]byte, error) {
	type alias AnthropicMsg
	raw, err := json.Marshal(alias(m))
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// AnthropicContent Anthropic 内容块
type AnthropicContent struct {
	Type      string          `json:"type"`                 // "text", "tool_use", "tool_result"
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`         // tool_use
	Name      string          `json:"name,omitempty"`       // tool_use
	Input     json.RawMessage `json:"input,omitempty"`      // tool_use input
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   json.RawMessage `json:"content,omitempty"`    // tool_result content
	IsError   bool            `json:"is_error,omitempty"`   // tool_result
}

// AnthropicResp Anthropic Messages API 响应
type AnthropicResp struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	Role       string            `json:"role"`
	Content    []AnthropicContent `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      AnthropicUsage    `json:"usage"`
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
	Index int    `json:"index"`
	Delta struct {
		Type         string `json:"type"`
		Text         string `json:"text"`
		PartialJSON  string `json:"partial_json"`
	} `json:"delta"`
}

// AnthropicSSEBlockStart content_block_start 事件 data
type AnthropicSSEBlockStart struct {
	Type         string           `json:"type"`
	Index        int              `json:"index"`
	ContentBlock AnthropicContent `json:"content_block"`
}

// AnthropicSSEStop message_stop 事件 data
type AnthropicSSEStop struct {
	Type string `json:"type"`
}

// --- 转换函数 ---

// AnthropicRequestToUnified 将 Anthropic 请求转为内部统一格式
func AnthropicRequestToUnified(req *AnthropicReq) (*models.UnifiedRequest, error) {
	unified := &models.UnifiedRequest{
		Model:       req.Model,
		System:      req.GetSystem(),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSeq,
		Stream:      req.Stream,
	}

	if len(req.Tools) > 0 {
		unified.Tools = req.Tools
	}
	if len(req.ToolChoice) > 0 {
		unified.ToolChoice = req.ToolChoice
	}
	if len(req.Thinking) > 0 {
		slog.Warn("ignoring_unsupported_thinking")
	}

	for _, msg := range req.Messages {
		role := msg.Role
		if role == "developer" {
			role = "user"
		}
		if len(msg.Content) == 0 {
			continue
		}
		// 单文本块：使用旧格式
		if len(msg.Content) == 1 && msg.Content[0].Type == "text" {
			unified.Messages = append(unified.Messages, models.UnifiedMessage{
				Role:    role,
				Content: msg.Content[0].Text,
			})
			continue
		}
		// 多块消息：使用 ContentBlocks
		um := models.UnifiedMessage{Role: role}
		for _, block := range msg.Content {
			switch block.Type {
			case "text":
				um.Content = block.Text
				um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
					Type: "text",
					Text: block.Text,
				})
			case "tool_use":
				um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
					Type:  "tool_use",
					ID:    block.ID,
					Name:  block.Name,
					Input: block.Input,
				})
			case "tool_result":
				um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
					Type:      "tool_result",
					ToolUseID: block.ToolUseID,
					Content:   block.Content,
					IsError:   block.IsError,
				})
			default:
				slog.Warn("ignoring_unsupported_content_block", "type", block.Type)
			}
		}
		unified.Messages = append(unified.Messages, um)
	}

	return unified, nil
}

// AnthropicResponseFromUnified 将内部统一格式转为 Anthropic 响应
func AnthropicResponseFromUnified(resp *models.UnifiedResponse) *AnthropicResp {
	anth := &AnthropicResp{
		ID:         resp.ID,
		Model:      resp.Model,
		Role:       resp.Role,
		StopReason: mapAnthropicStopReason(resp.StopReason),
		Usage: AnthropicUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}

	if resp.Content != "" {
		anth.Content = append(anth.Content, AnthropicContent{Type: "text", Text: resp.Content})
	}
	for _, tc := range resp.ToolCalls {
		anth.Content = append(anth.Content, AnthropicContent{
			Type:  "tool_use",
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: json.RawMessage(tc.Function.Arguments),
		})
	}

	return anth
}

// AnthropicStreamEventToUnified 将 Anthropic SSE 原始事件转为 UnifiedStreamEvent 切片
func AnthropicStreamEventToUnified(eventType string, data []byte) ([]*models.UnifiedStreamEvent, error) {
	switch eventType {
	case "content_block_start":
		var start AnthropicSSEBlockStart
		if err := json.Unmarshal(data, &start); err != nil {
			return nil, err
		}
		if start.ContentBlock.Type == "tool_use" {
			args := ""
			if len(start.ContentBlock.Input) > 0 {
				args = string(start.ContentBlock.Input)
			}
			return []*models.UnifiedStreamEvent{{
				Type: models.StreamEventDelta,
				ToolCalls: []models.ToolCallDelta{{
					Index: start.Index,
					ID:    start.ContentBlock.ID,
					Type:  "function",
					Function: &models.ToolCallFunctionDelta{
						Name:      start.ContentBlock.Name,
						Arguments: args,
					},
				}},
			}}, nil
		}
		return nil, nil

	case "content_block_delta":
		var delta AnthropicSSEDelta
		if err := json.Unmarshal(data, &delta); err != nil {
			return nil, err
		}
		switch delta.Delta.Type {
		case "text_delta":
			if delta.Delta.Text != "" {
				return []*models.UnifiedStreamEvent{{
					Type:    models.StreamEventDelta,
					Content: delta.Delta.Text,
				}}, nil
			}
		case "input_json_delta":
			if delta.Delta.PartialJSON != "" {
				return []*models.UnifiedStreamEvent{{
					Type: models.StreamEventDelta,
					ToolCalls: []models.ToolCallDelta{{
						Index: delta.Index,
						Function: &models.ToolCallFunctionDelta{
							Arguments: delta.Delta.PartialJSON,
						},
					}},
				}}, nil
			}
		}
		return nil, nil

	case "content_block_stop":
		return nil, nil

	case "message_delta":
		var msgDelta struct {
			Delta struct {
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage,omitempty"`
		}
		if err := json.Unmarshal(data, &msgDelta); err != nil {
			return nil, err
		}
		var events []*models.UnifiedStreamEvent
		if msgDelta.Delta.StopReason != "" {
			events = append(events, &models.UnifiedStreamEvent{
				Type:       models.StreamEventDelta,
				StopReason: mapAnthropicStopReason(msgDelta.Delta.StopReason),
			})
		}
		if msgDelta.Usage != nil && msgDelta.Usage.OutputTokens > 0 {
			events = append(events, &models.UnifiedStreamEvent{
				Type:  models.StreamEventUsage,
				Usage: &models.UnifiedUsage{OutputTokens: msgDelta.Usage.OutputTokens},
			})
		}
		return events, nil

	case "message_stop":
		return []*models.UnifiedStreamEvent{{Type: models.StreamEventStop}}, nil

	case "error":
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &errBody); err != nil {
			return []*models.UnifiedStreamEvent{{Type: models.StreamEventError, Error: fmt.Errorf("upstream error: unknown")}}, nil
		}
		return []*models.UnifiedStreamEvent{{Type: models.StreamEventError, Error: fmt.Errorf("upstream error: %s", errBody.Error.Message)}}, nil

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
	case "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}
