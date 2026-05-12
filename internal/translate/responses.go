package translate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tursom/turapis/internal/models"
)

// --- OpenAI Responses API 请求/响应类型 ---

// ResponsesReq OpenAI Responses API 请求
type ResponsesReq struct {
	Model          string       `json:"model"`
	Input          responsesInput `json:"input"`
	Instructions   string       `json:"instructions,omitempty"`
	MaxOutputTokens int         `json:"max_output_tokens,omitempty"`
	Temperature    *float64     `json:"temperature,omitempty"`
	TopP           *float64     `json:"top_p,omitempty"`
	Stream         bool         `json:"stream"`

	// 高级特性检测
	Tools  json.RawMessage `json:"tools,omitempty"`
}

// responsesInput 支持 string 或 array 两种形式
type responsesInput struct {
	StringVal string
	Msgs      []ResponseInputMsg
}

// ResponseInputMsg Responses API 输入消息
type ResponseInputMsg struct {
	Role    string                `json:"role"`
	Content responsesInputContent `json:"content"`
}

type responsesInputContent struct {
	StringVal string
	Parts     []responsesContentPart
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func (r *responsesInput) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &r.StringVal)
	}
	return json.Unmarshal(data, &r.Msgs)
}

func (r responsesInput) MarshalJSON() ([]byte, error) {
	if r.StringVal != "" {
		return json.Marshal(r.StringVal)
	}
	return json.Marshal(r.Msgs)
}

func (c *responsesInputContent) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &c.StringVal)
	}
	return json.Unmarshal(data, &c.Parts)
}

func (c responsesInputContent) MarshalJSON() ([]byte, error) {
	if c.StringVal != "" {
		return json.Marshal(c.StringVal)
	}
	return json.Marshal(c.Parts)
}

// ResponsesResp OpenAI Responses API 响应
type ResponsesResp struct {
	ID     string           `json:"id"`
	Model  string           `json:"model"`
	Output []responsesOutputItem `json:"output"`
	Usage  responsesUsage   `json:"usage"`
}

type responsesOutputItem struct {
	Type    string              `json:"type"`
	Role    string              `json:"role,omitempty"`
	Content []responsesContentPart `json:"content,omitempty"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// --- 转换函数 ---

// ResponsesRequestToUnified 将 Responses API 请求转为统一格式
func ResponsesRequestToUnified(req *ResponsesReq) (*models.UnifiedRequest, error) {
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("%w: tools", models.ErrUnsupportedFeature)
	}

	unified := &models.UnifiedRequest{
		Model:       req.Model,
		System:      req.Instructions,
		MaxTokens:   req.MaxOutputTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
	}

	// input 可以是纯字符串或消息数组
	if req.Input.StringVal != "" {
		unified.Messages = append(unified.Messages, models.UnifiedMessage{
			Role:    "user",
			Content: req.Input.StringVal,
		})
	}
	for _, msg := range req.Input.Msgs {
		content := extractContent(msg.Content)
		unified.Messages = append(unified.Messages, models.UnifiedMessage{
			Role:    msg.Role,
			Content: content,
		})
	}

	return unified, nil
}

func extractContent(c responsesInputContent) string {
	if c.StringVal != "" {
		return c.StringVal
	}
	var texts []string
	for _, p := range c.Parts {
		if p.Type == "input_text" || p.Type == "output_text" || p.Type == "text" || p.Type == "" {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
	}
	return strings.Join(texts, "\n")
}

// ResponsesResponseFromUnified 将统一格式转为 Responses API 响应
func ResponsesResponseFromUnified(resp *models.UnifiedResponse) *ResponsesResp {
	return &ResponsesResp{
		ID:    resp.ID,
		Model: resp.Model,
		Output: []responsesOutputItem{
			{
				Type: "message",
				Role: resp.Role,
				Content: []responsesContentPart{
					{Type: "output_text", Text: resp.Content},
				},
			},
		},
		Usage: responsesUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}
}

// --- SSE 流式事件类型 ---

// ResponsesSSEDelta response.output_text.delta 事件
type ResponsesSSEDelta struct {
	Delta string `json:"delta"`
}

// ResponsesSSECompleted response.completed 事件
type ResponsesSSECompleted struct {
	Response struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// ResponsesStreamEventToUnified 将 Responses SSE 原始事件转为 UnifiedStreamEvent
func ResponsesStreamEventToUnified(eventType string, data []byte) (*models.UnifiedStreamEvent, error) {
	switch eventType {
	case "response.output_text.delta":
		var d ResponsesSSEDelta
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		if d.Delta != "" {
			return &models.UnifiedStreamEvent{
				Type:    models.StreamEventDelta,
				Content: d.Delta,
			}, nil
		}
		return nil, nil

	case "response.completed":
		return &models.UnifiedStreamEvent{Type: models.StreamEventStop}, nil

	case "response.output_text.done":
		return nil, nil

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
