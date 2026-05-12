package translate

import (
	"fmt"

	"github.com/tursom/turapis/internal/models"
)

// --- OpenAI Chat Completions 请求/响应类型（手写结构体，v1 text-only） ---

// OpenAIReq OpenAI Chat Completions 请求
type OpenAIReq struct {
	Model       string       `json:"model"`
	Messages    []OpenAIMsg  `json:"messages"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	Temperature *float64     `json:"temperature,omitempty"`
	TopP        *float64     `json:"top_p,omitempty"`
	Stop        []string     `json:"stop,omitempty"`
	Stream      bool         `json:"stream"`

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
	Role    string      `json:"role"`
	Content string      `json:"content"`
}

// OpenAIResp OpenAI Chat Completions 响应
type OpenAIResp struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
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
// 检测高级特性（tools/functions），返回 ErrUnsupportedFeature
func OpenAIRequestToUnified(req *OpenAIReq) (*models.UnifiedRequest, error) {
	// AC10: 检测不支持的高级特性
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("%w: tools/function_calling", models.ErrUnsupportedFeature)
	}
	if len(req.ToolChoice) > 0 {
		return nil, fmt.Errorf("%w: tool_choice", models.ErrUnsupportedFeature)
	}
	if len(req.Functions) > 0 {
		return nil, fmt.Errorf("%w: functions", models.ErrUnsupportedFeature)
	}

	unified := &models.UnifiedRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      req.Stream,
	}

	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			// 提取 system message 到顶层字段
			if unified.System == "" {
				unified.System = msg.Content
			} else {
				unified.System += "\n" + msg.Content
			}
		case "user", "assistant":
			unified.Messages = append(unified.Messages, models.UnifiedMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	return unified, nil
}

// OpenAIResponseFromUnified 将内部统一格式转为 OpenAI 响应
func OpenAIResponseFromUnified(resp *models.UnifiedResponse) *OpenAIResp {
	return &OpenAIResp{
		ID:    resp.ID,
		Model: resp.Model,
		Choices: []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Role    string `json:"role"`
					Content string `json:"content"`
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
}

func mapOpenAIStopReason(reason string) string {
	switch reason {
	case "stop":
		return "stop"
	case "length":
		return "length"
	default:
		return reason
	}
}
