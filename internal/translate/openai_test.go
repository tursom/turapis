package translate

import (
	"encoding/json"
	"testing"

	"github.com/tursom/turapis/internal/models"
)

func TestOpenAIRequestToUnified_DeveloperRoleMapsToSystem(t *testing.T) {
	req := &OpenAIReq{
		Model: "gpt-4",
		Messages: []OpenAIMsg{
			{Role: "developer", Content: "You are a helpful assistant"},
			{Role: "user", Content: "hello"},
		},
	}
	unified, err := OpenAIRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unified.System != "You are a helpful assistant" {
		t.Errorf("expected developer content in System, got: %q", unified.System)
	}
	if len(unified.Messages) != 1 || unified.Messages[0].Role != "user" {
		t.Errorf("expected 1 user message, got: %+v", unified.Messages)
	}
}

func TestOpenAIRequestToUnified_MultipleSystemAndDeveloper(t *testing.T) {
	req := &OpenAIReq{
		Model: "gpt-4",
		Messages: []OpenAIMsg{
			{Role: "system", Content: "sys prompt"},
			{Role: "developer", Content: "dev prompt"},
		},
	}
	unified, err := OpenAIRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unified.System != "sys prompt\ndev prompt" {
		t.Errorf("expected merged system, got: %q", unified.System)
	}
}

func TestOpenAIRequestToUnified_ToolsPassthrough(t *testing.T) {
	toolsJSON := json.RawMessage(`[{"type":"function","function":{"name":"search","parameters":{"type":"object"}}}]`)
	req := &OpenAIReq{
		Model:    "gpt-4",
		Messages: []OpenAIMsg{{Role: "user", Content: "search for things"}},
		Tools:    jsonRaw(toolsJSON),
	}
	unified, err := OpenAIRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(unified.Tools) != string(toolsJSON) {
		t.Errorf("tools not passed through, got: %s", unified.Tools)
	}
}

func TestOpenAIRequestToUnified_ToolChoicePassthrough(t *testing.T) {
	tcJSON := json.RawMessage(`"auto"`)
	req := &OpenAIReq{
		Model:      "gpt-4",
		Messages:   []OpenAIMsg{{Role: "user", Content: "hi"}},
		ToolChoice: jsonRaw(tcJSON),
	}
	unified, err := OpenAIRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(unified.ToolChoice) != string(tcJSON) {
		t.Errorf("tool_choice not passed through, got: %s", unified.ToolChoice)
	}
}

func TestOpenAIRequestToUnified_FunctionsRejected(t *testing.T) {
	req := &OpenAIReq{
		Model:     "gpt-4",
		Messages:  []OpenAIMsg{{Role: "user", Content: "hi"}},
		Functions: jsonRaw(`[{"name":"foo"}]`),
	}
	_, err := OpenAIRequestToUnified(req)
	if err == nil {
		t.Fatal("expected error for functions, got nil")
	}
}

func TestOpenAIRequestToUnified_AssistantToolCalls(t *testing.T) {
	toolCallsJSON := `[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{\"q\":\"hello\"}"}}]`
	req := &OpenAIReq{
		Model: "gpt-4",
		Messages: []OpenAIMsg{
			{Role: "user", Content: "search hello"},
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(toolCallsJSON)},
		},
	}
	unified, err := OpenAIRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unified.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(unified.Messages))
	}
	asst := unified.Messages[1]
	if asst.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", asst.Role)
	}
	if len(asst.ContentBlocks) != 1 || asst.ContentBlocks[0].Type != "tool_use" {
		t.Errorf("expected tool_use content block, got: %+v", asst.ContentBlocks)
	}
	if asst.ContentBlocks[0].ID != "call_1" {
		t.Errorf("expected call_1 id, got %s", asst.ContentBlocks[0].ID)
	}
	if asst.ContentBlocks[0].Name != "search" {
		t.Errorf("expected search name, got %s", asst.ContentBlocks[0].Name)
	}
}

func TestOpenAIRequestToUnified_ToolResultMessages(t *testing.T) {
	req := &OpenAIReq{
		Model: "gpt-4",
		Messages: []OpenAIMsg{
			{Role: "user", Content: "search hello"},
			{Role: "assistant", Content: "", ToolCalls: json.RawMessage(
				`[{"id":"call_1","type":"function","function":{"name":"search","arguments":"{}"}}]`)},
			{Role: "tool", ToolCallID: "call_1", Content: "result content"},
		},
	}
	unified, err := OpenAIRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unified.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(unified.Messages))
	}
	toolMsg := unified.Messages[2]
	if toolMsg.Role != "user" {
		t.Errorf("expected tool message role=user, got %s", toolMsg.Role)
	}
	if len(toolMsg.ContentBlocks) != 1 || toolMsg.ContentBlocks[0].Type != "tool_result" {
		t.Errorf("expected tool_result block, got: %+v", toolMsg.ContentBlocks)
	}
	if toolMsg.ContentBlocks[0].ToolUseID != "call_1" {
		t.Errorf("expected tool_use_id=call_1, got %s", toolMsg.ContentBlocks[0].ToolUseID)
	}
}

func TestOpenAIResponseFromUnified_TextOnly(t *testing.T) {
	resp := &models.UnifiedResponse{
		ID:         "resp-1",
		Model:      "gpt-4",
		Role:       "assistant",
		Content:    "hello",
		StopReason: "stop",
		Usage:      models.UnifiedUsage{InputTokens: 10, OutputTokens: 5},
	}
	oaiResp := OpenAIResponseFromUnified(resp)
	if len(oaiResp.Choices) != 1 {
		t.Fatal("expected 1 choice")
	}
	c := oaiResp.Choices[0]
	if c.Message.Content != "hello" {
		t.Errorf("expected content hello, got %q", c.Message.Content)
	}
	if c.FinishReason != "stop" {
		t.Errorf("expected stop, got %s", c.FinishReason)
	}
	if len(c.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool_calls, got %d", len(c.Message.ToolCalls))
	}
}

func TestOpenAIResponseFromUnified_WithToolCalls(t *testing.T) {
	resp := &models.UnifiedResponse{
		ID:      "resp-2",
		Model:   "gpt-4",
		Role:    "assistant",
		Content: "",
		ToolCalls: []models.ToolCall{{
			ID:       "call_abc",
			Type:     "function",
			Function: models.ToolCallFunction{Name: "search", Arguments: `{"q":"test"}`},
		}},
		StopReason: "tool_calls",
		Usage:      models.UnifiedUsage{InputTokens: 20, OutputTokens: 30},
	}
	oaiResp := OpenAIResponseFromUnified(resp)
	if len(oaiResp.Choices) != 1 {
		t.Fatal("expected 1 choice")
	}
	c := oaiResp.Choices[0]
	if c.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason tool_calls, got %s", c.FinishReason)
	}
	if len(c.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(c.Message.ToolCalls))
	}
	tc := c.Message.ToolCalls[0]
	if tc.ID != "call_abc" || tc.Function.Name != "search" || tc.Function.Arguments != `{"q":"test"}` {
		t.Errorf("tool call mismatch: %+v", tc)
	}
}

func TestMapOpenAIStopReason(t *testing.T) {
	tests := []struct{ input, expected string }{
		{"stop", "stop"},
		{"length", "length"},
		{"tool_calls", "tool_calls"},
		{"tool_use", "tool_calls"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		got := mapOpenAIStopReason(tt.input)
		if got != tt.expected {
			t.Errorf("mapOpenAIStopReason(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
