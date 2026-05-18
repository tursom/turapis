package models

import (
	"context"
	"encoding/json"
)

// ContentBlock 消息内容块，支持 text、tool_use、tool_result 三种类型
type ContentBlock struct {
	Type      string          `json:"type"`                 // "text", "tool_use", "tool_result"
	Text      string          `json:"text,omitempty"`       // text 内容
	ID        string          `json:"id,omitempty"`         // tool_use id
	Name      string          `json:"name,omitempty"`       // tool_use name / function name
	Input     json.RawMessage `json:"input,omitempty"`      // tool_use input (JSON object)
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result: 引用的 tool_use id
	Content   json.RawMessage `json:"content,omitempty"`    // tool_result: 结果内容
	IsError   bool            `json:"is_error,omitempty"`   // tool_result: 是否为错误
}

// UnifiedMessage 统一消息格式
// text-only 消息只填 Content；多块消息（tool_use/tool_result）填 ContentBlocks
type UnifiedMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ContentBlocks    []ContentBlock `json:"content_blocks,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
}

// UnifiedRequest 统一请求格式
type UnifiedRequest struct {
	Model       string           `json:"model"`
	Messages    []UnifiedMessage `json:"messages"`
	System      string           `json:"system,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Temperature *float64         `json:"temperature,omitempty"`
	TopP        *float64         `json:"top_p,omitempty"`
	Stop        []string         `json:"stop,omitempty"`
	Stream      bool             `json:"stream"`
	Tools       json.RawMessage  `json:"tools,omitempty"`
	ToolChoice  json.RawMessage  `json:"tool_choice,omitempty"`
	// WebSearchOptions 从 Responses API web_search 工具提取的搜索参数
	WebSearchOptions json.RawMessage `json:"web_search_options,omitempty"`
	// OriginalPath 原始请求路径，用于 provider 选择正确的上游端点
	// 例如 "/v1/responses" → 上游使用 /responses
	OriginalPath string `json:"-"`
}

type ctxKey int

const (
	ctxKeyRawBody ctxKey = iota
	ctxKeyCodexVersion
	ctxKeyRawProxy
)

func WithRawBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, ctxKeyRawBody, body)
}

func RawBodyFromContext(ctx context.Context) []byte {
	if b, ok := ctx.Value(ctxKeyRawBody).([]byte); ok {
		return b
	}
	return nil
}

func WithCodexVersion(ctx context.Context, v string) context.Context {
	return context.WithValue(ctx, ctxKeyCodexVersion, v)
}

func CodexVersionFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyCodexVersion).(string); ok {
		return v
	}
	return ""
}

func WithRawProxy(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKeyRawProxy, true)
}

func IsRawProxy(ctx context.Context) bool {
	v, _ := ctx.Value(ctxKeyRawProxy).(bool)
	return v
}

// UnifiedUsage 统一用量信息
type UnifiedUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// ToolCall 非流式响应中的工具调用
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"` // "function"
	Function ToolCallFunction `json:"function"`
}

// ToolCallFunction 工具调用函数详情
type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// UnifiedResponse 统一响应格式
type UnifiedResponse struct {
	ID         string       `json:"id"`
	Model      string       `json:"model"`
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []ToolCall   `json:"tool_calls,omitempty"`
	StopReason string       `json:"stop_reason"`
	Usage      UnifiedUsage `json:"usage"`
}

// StreamEventType 流式事件类型
type StreamEventType string

const (
	StreamEventDelta StreamEventType = "content_block_delta"
	StreamEventStop  StreamEventType = "message_stop"
	StreamEventUsage StreamEventType = "usage"
	StreamEventError StreamEventType = "error"
)

// ToolCallDelta 流式工具调用增量
type ToolCallDelta struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id,omitempty"`
	Type     string                 `json:"type,omitempty"` // "function"
	Function *ToolCallFunctionDelta `json:"function,omitempty"`
}

// ToolCallFunctionDelta 流式工具调用函数增量
type ToolCallFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"` // partial JSON string
}

// UnifiedStreamEvent 统一流式事件
type UnifiedStreamEvent struct {
	Type             StreamEventType `json:"type"`
	Content          string          `json:"content,omitempty"`
	ToolCalls        []ToolCallDelta `json:"tool_calls,omitempty"`
	StopReason       string          `json:"stop_reason,omitempty"`
	Usage            *UnifiedUsage   `json:"usage,omitempty"`
	Error            error           `json:"-"`
	ReasoningContent string          `json:"-"`
}

// ProtocolType 协议类型
type ProtocolType string

const (
	ProtocolOpenAI    ProtocolType = "openai"
	ProtocolAnthropic ProtocolType = "anthropic"
)

// ModelInfo 模型信息
type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}
