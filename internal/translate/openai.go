package translate

import (
	"encoding/json"
	"fmt"

	"github.com/tursom/turapis/internal/models"
)

// --- OpenAI Chat Completions 请求/响应类型 ---

// OpenAIReq OpenAI Chat Completions 请求
type OpenAIReq struct {
	Model       string    `json:"model"`
	Messages    []OpenAIMsg `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Stream      bool      `json:"stream"`

	// 高级特性检测用
	Tools      jsonRaw `json:"tools,omitempty"`
	ToolChoice jsonRaw `json:"tool_choice,omitempty"`
	Functions  jsonRaw `json:"functions,omitempty"`
}

type jsonRaw []byte

func (j *jsonRaw) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && string(data) != "null" {
		*j = data
	}
	return nil
}

func (j jsonRaw) MarshalJSON() ([]byte, error) {
	if len(j) == 0 {
		return []byte("null"), nil
	}
	return j, nil
}

// OpenAIMsg OpenAI 消息
type OpenAIMsg struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	ToolCallID string          `json:"tool_call_id,omitempty"` // role=tool
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`   // role=assistant
}

// openaiToolCall 用于解析 tool_calls 内部结构
type openaiToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function openaiToolCallFunc `json:"function"`
}

type openaiToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// OpenAIResp OpenAI Chat Completions 响应
type OpenAIResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// --- 转换函数 ---

// OpenAIRequestToUnified 将 OpenAI 请求转为内部统一格式
func OpenAIRequestToUnified(req *OpenAIReq) (*models.UnifiedRequest, error) {
	unified := &models.UnifiedRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      req.Stream,
	}

	if len(req.Tools) > 0 {
		unified.Tools = json.RawMessage(req.Tools)
	}
	if len(req.ToolChoice) > 0 {
		unified.ToolChoice = json.RawMessage(req.ToolChoice)
	}
	if len(req.Functions) > 0 {
		return nil, fmt.Errorf("%w: functions", models.ErrUnsupportedFeature)
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system", "developer":
			if unified.System == "" {
				unified.System = msg.Content
			} else {
				unified.System += "\n" + msg.Content
			}
		case "user":
			um := models.UnifiedMessage{Role: "user", Content: msg.Content}
			unified.Messages = append(unified.Messages, um)
		case "assistant":
			um := models.UnifiedMessage{Role: "assistant", Content: msg.Content}
			if len(msg.ToolCalls) > 0 {
				var tcs []openaiToolCall
				if err := json.Unmarshal(msg.ToolCalls, &tcs); err == nil {
					for _, tc := range tcs {
						um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
							Type:  "tool_use",
							ID:    tc.ID,
							Name:  tc.Function.Name,
							Input: json.RawMessage(tc.Function.Arguments),
						})
					}
				}
			}
			unified.Messages = append(unified.Messages, um)
		case "tool":
			um := models.UnifiedMessage{
				Role: "user",
				ContentBlocks: []models.ContentBlock{{
					Type:      "tool_result",
					ToolUseID: msg.ToolCallID,
					Content:   json.RawMessage(jsonMarshalString(msg.Content)),
				}},
			}
			unified.Messages = append(unified.Messages, um)
		}
	}

	return unified, nil
}

func jsonMarshalString(s string) []byte {
	b, _ := json.Marshal(s)
	return b
}

// OpenAIResponseFromUnified 将内部统一格式转为 OpenAI 响应
func OpenAIResponseFromUnified(resp *models.UnifiedResponse) *OpenAIResp {
	oai := &OpenAIResp{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []struct {
			Message struct {
				Role      string           `json:"role"`
				Content   string           `json:"content"`
				ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Role      string           `json:"role"`
					Content   string           `json:"content"`
					ToolCalls []openaiToolCall `json:"tool_calls,omitempty"`
				}{
					Role:    resp.Role,
					Content: resp.Content,
				},
				FinishReason: mapOpenAIStopReason(resp.StopReason),
			},
		},
		Usage: struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		}{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
		},
	}

	for _, tc := range resp.ToolCalls {
		oai.Choices[0].Message.ToolCalls = append(oai.Choices[0].Message.ToolCalls, openaiToolCall{
			ID:   tc.ID,
			Type: tc.Type,
			Function: openaiToolCallFunc{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	return oai
}

func mapOpenAIStopReason(reason string) string {
	switch reason {
	case "stop":
		return "stop"
	case "length":
		return "length"
	case "tool_calls", "tool_use":
		return "tool_calls"
	default:
		return reason
	}
}
