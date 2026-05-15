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
	Model           string         `json:"model"`
	Input           responsesInput `json:"input"`
	Instructions    string         `json:"instructions,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"top_p,omitempty"`
	Stream          bool           `json:"stream"`

	// 高级特性
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
}

// responsesInput 支持 string 或 array 两种形式
type responsesInput struct {
	StringVal string
	Msgs      []ResponseInputMsg
}

// ResponseInputMsg Responses API 输入消息
type ResponseInputMsg struct {
	Role       string                `json:"role"`
	Content    responsesInputContent `json:"content"`
	ToolCallID string                `json:"tool_call_id,omitempty"`
	Name       string                `json:"name,omitempty"`
	CallID     string                `json:"call_id,omitempty"`
	Arguments  string                `json:"arguments,omitempty"`
	Output     json.RawMessage       `json:"output,omitempty"`
}

type responsesInputContent struct {
	StringVal string
	Parts     []responsesContentPart
}

type responsesContentPart struct {
	Type       string          `json:"type"`                   // "input_text", "output_text", "tool_call", "tool_result"
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`           // tool_call
	Name       string          `json:"name,omitempty"`         // tool_call function name / tool_result name
	Arguments  string          `json:"arguments,omitempty"`    // tool_call arguments (JSON string)
	CallID     string          `json:"call_id,omitempty"`      // tool_result: 引用的 tool_use id
	Content    json.RawMessage `json:"content,omitempty"`      // tool_result
	IsError    bool            `json:"is_error,omitempty"`     // tool_result
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
	ID     string              `json:"id"`
	Model  string              `json:"model"`
	Output []responsesOutputItem `json:"output"`
	Usage  responsesUsage      `json:"usage"`
}

type responsesOutputItem struct {
	Type      string                 `json:"type"`
	Role      string                 `json:"role,omitempty"`
	Content   []responsesContentPart `json:"content,omitempty"`
	ID        string                 `json:"id,omitempty"`        // function_call
	CallID    string                 `json:"call_id,omitempty"`   // function_call
	Name      string                 `json:"name,omitempty"`      // function_call
	Arguments string                 `json:"arguments,omitempty"` // function_call
	Output    json.RawMessage        `json:"output,omitempty"`    // function_call output (for input)
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// --- 转换函数 ---

// ResponsesRequestToUnified 将 Responses API 请求转为统一格式
func ResponsesRequestToUnified(req *ResponsesReq) (*models.UnifiedRequest, error) {
	if len(req.Tools) > 0 {
		// 透传 tools，不再拒绝
	}

	system := req.Instructions
	if len(system) > 4000 {
		system = system[:4000]
	}
	system += "\n\nAlways give a brief text response before executing any tool. Tell the user what you are doing."

	unified := &models.UnifiedRequest{
		Model:       req.Model,
		System:      system,
		MaxTokens:   req.MaxOutputTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stream:      req.Stream,
		Tools:       req.Tools,
		ToolChoice:  req.ToolChoice,
	}

	if req.Input.StringVal != "" {
		unified.Messages = append(unified.Messages, models.UnifiedMessage{
			Role:    "user",
			Content: req.Input.StringVal,
		})
	}

	for _, msg := range req.Input.Msgs {
		if msg.Role == "developer" {
			content := extractContent(msg.Content)
			if content != "" {
				// 限制总 system prompt 长度，避免小模型被 codex 的大段指令压垮
				maxLen := 4000
				remaining := maxLen - len(unified.System)
				if remaining > 0 {
					if len(content) > remaining {
						content = content[:remaining]
					}
					if unified.System != "" {
						unified.System += "\n\n" + content
					} else {
						unified.System = content
					}
				}
			}
			continue
		}
		um := buildUnifiedMsgFromResponses(msg)
		unified.Messages = append(unified.Messages, um)
	}

	return unified, nil
}

func buildUnifiedMsgFromResponses(msg ResponseInputMsg) models.UnifiedMessage {
	um := models.UnifiedMessage{Role: msg.Role}

	// function_call 类型的输入项 (来自历史记录)
	if msg.Name != "" && msg.CallID != "" {
		um.Role = "assistant"
		um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
			Type:  "tool_use",
			ID:    msg.CallID,
			Name:  msg.Name,
			Input: json.RawMessage(msg.Arguments),
		})
		return um
	}

	// function_call output 类型的输入项 (工具执行结果)
	if msg.Output != nil && len(msg.Output) > 0 {
		um.Role = "user"
		um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
			Type:      "tool_result",
			ToolUseID: msg.CallID,
			Content:   msg.Output,
		})
		return um
	}

	if um.Role == "" {
		um.Role = "user"
	}

	// 普通消息：解析 content parts
	content := extractContent(msg.Content)
	um.Content = content

	for _, p := range msg.Content.Parts {
		switch p.Type {
		case "input_text", "output_text", "text", "":
			// 已在 Content 中
		case "tool_call":
			um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
				Type:  "tool_use",
				ID:    p.ID,
				Name:  p.Name,
				Input: json.RawMessage(p.Arguments),
			})
		case "tool_result":
			um.ContentBlocks = append(um.ContentBlocks, models.ContentBlock{
				Type:      "tool_result",
				ToolUseID: p.CallID,
				Content:   p.Content,
				IsError:   p.IsError,
			})
		}
	}

	if len(um.ContentBlocks) == 0 {
		um.ContentBlocks = nil
	}
	return um
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
	r := &ResponsesResp{
		ID:    resp.ID,
		Model: resp.Model,
		Usage: responsesUsage{
			InputTokens:  resp.Usage.InputTokens,
			OutputTokens: resp.Usage.OutputTokens,
		},
	}

	// 文本消息输出
	if resp.Content != "" || (len(resp.ToolCalls) == 0) {
		r.Output = append(r.Output, responsesOutputItem{
			Type: "message",
			Role: resp.Role,
			Content: []responsesContentPart{
				{Type: "output_text", Text: resp.Content},
			},
		})
	}

	// 工具调用输出
	for _, tc := range resp.ToolCalls {
		r.Output = append(r.Output, responsesOutputItem{
			Type:      "function_call",
			ID:        tc.ID,
			CallID:    tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	return r
}

// --- SSE 流式事件类型 ---

// ResponsesSSEDelta response.text.delta 事件
type ResponsesSSEDelta struct {
	Delta string `json:"delta"`
}

// ResponsesSSEOutputItemAdded response.output_item.added 事件
type ResponsesSSEOutputItemAdded struct {
	Item responsesOutputItem `json:"item"`
}

// ResponsesSSEFuncCallDelta response.function_call_arguments.delta 事件
type ResponsesSSEFuncCallDelta struct {
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
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	} `json:"response"`
}

// ResponsesStreamEventToUnified 将 Responses SSE 原始事件转为 UnifiedStreamEvent
func ResponsesStreamEventToUnified(eventType string, data []byte) (*models.UnifiedStreamEvent, error) {
	switch eventType {
	case "response.created",
		"response.in_progress":
		return nil, nil

	case "response.output_item.added":
		var d ResponsesSSEOutputItemAdded
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, nil
		}
		if d.Item.Type == "function_call" {
			return &models.UnifiedStreamEvent{
				Type: models.StreamEventDelta,
				ToolCalls: []models.ToolCallDelta{{
					Index: 0,
					ID:    d.Item.ID,
					Type:  "function",
					Function: &models.ToolCallFunctionDelta{
						Name:      d.Item.Name,
						Arguments: d.Item.Arguments,
					},
				}},
			}, nil
		}
		return nil, nil

	case "response.text.delta":
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

	case "response.function_call_arguments.delta":
		var d ResponsesSSEFuncCallDelta
		if err := json.Unmarshal(data, &d); err != nil {
			return nil, err
		}
		if d.Delta != "" {
			return &models.UnifiedStreamEvent{
				Type: models.StreamEventDelta,
				ToolCalls: []models.ToolCallDelta{{
					Index: 0,
					Function: &models.ToolCallFunctionDelta{
						Arguments: d.Delta,
					},
				}},
			}, nil
		}
		return nil, nil

	case "response.output_item.done",
		"response.text.done",
		"response.content_part.done":
		return nil, nil

	case "response.completed":
		return &models.UnifiedStreamEvent{Type: models.StreamEventStop}, nil

	case "response.failed",
		"response.incomplete":
		var errBody struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(data, &errBody); err == nil && errBody.Error.Message != "" {
			return &models.UnifiedStreamEvent{Type: models.StreamEventError, Error: fmt.Errorf("upstream error: %s", errBody.Error.Message)}, nil
		}
		return &models.UnifiedStreamEvent{Type: models.StreamEventError, Error: fmt.Errorf("upstream error: %s", eventType)}, nil

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
