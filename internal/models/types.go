package models

// UnifiedMessage 统一消息格式（v1: text-only）
type UnifiedMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
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
}

// UnifiedUsage 统一用量信息
type UnifiedUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// UnifiedResponse 统一响应格式
type UnifiedResponse struct {
	ID         string       `json:"id"`
	Model      string       `json:"model"`
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	StopReason string       `json:"stop_reason"`
	Usage      UnifiedUsage `json:"usage"`
}

// StreamEventType 流式事件类型
type StreamEventType string

const (
	StreamEventDelta StreamEventType = "content_block_delta"
	StreamEventStop  StreamEventType = "message_stop"
	StreamEventError StreamEventType = "error"
)

// UnifiedStreamEvent 统一流式事件
type UnifiedStreamEvent struct {
	Type       StreamEventType `json:"type"`
	Content    string          `json:"content,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Error      error           `json:"-"`
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
