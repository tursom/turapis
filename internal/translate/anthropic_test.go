package translate

import (
	"encoding/json"
	"testing"

	"github.com/tursom/turapis/internal/models"
)

func TestAnthropicRequestToUnified_DeveloperRoleNormalized(t *testing.T) {
	req := &AnthropicReq{
		Model:    "claude-3",
		MaxTokens: 100,
		Messages: []AnthropicMsg{{
			Role: "developer",
			Content: []AnthropicContent{
				{Type: "text", Text: "developer instruction"},
			},
		}},
	}
	unified, err := AnthropicRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unified.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(unified.Messages))
	}
	if unified.Messages[0].Role != "user" {
		t.Errorf("expected developer→user, got %s", unified.Messages[0].Role)
	}
	if unified.Messages[0].Content != "developer instruction" {
		t.Errorf("expected content, got %q", unified.Messages[0].Content)
	}
}

func TestAnthropicRequestToUnified_ToolsPassthrough(t *testing.T) {
	toolsJSON := json.RawMessage(`[{"name":"search","input_schema":{"type":"object"}}]`)
	req := &AnthropicReq{
		Model:    "claude-3",
		MaxTokens: 100,
		Messages: []AnthropicMsg{{
			Role: "user",
			Content: []AnthropicContent{
				{Type: "text", Text: "search for things"},
			},
		}},
		Tools: toolsJSON,
	}
	unified, err := AnthropicRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(unified.Tools) != string(toolsJSON) {
		t.Errorf("tools not passed through, got: %s", unified.Tools)
	}
}

func TestAnthropicRequestToUnified_ToolChoicePassthrough(t *testing.T) {
	tcJSON := json.RawMessage(`{"type":"auto"}`)
	req := &AnthropicReq{
		Model:      "claude-3",
		MaxTokens:  100,
		Messages:   []AnthropicMsg{{Role: "user", Content: []AnthropicContent{{Type: "text", Text: "hi"}}}},
		ToolChoice: tcJSON,
	}
	unified, err := AnthropicRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(unified.ToolChoice) != string(tcJSON) {
		t.Errorf("tool_choice not passed through, got: %s", unified.ToolChoice)
	}
}

func TestAnthropicRequestToUnified_ToolUseInAssistantMessage(t *testing.T) {
	reqBody := `{
		"model": "claude-3",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": "search for things"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "toolu_1", "name": "search", "input": {"q": "hello"}}]}
		]
	}`
	var req AnthropicReq
	if err := json.Unmarshal([]byte(reqBody), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	unified, err := AnthropicRequestToUnified(&req)
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
		t.Errorf("expected tool_use block, got: %+v", asst.ContentBlocks)
	}
	if asst.ContentBlocks[0].ID != "toolu_1" {
		t.Errorf("expected id toolu_1, got %s", asst.ContentBlocks[0].ID)
	}
	if asst.ContentBlocks[0].Name != "search" {
		t.Errorf("expected name search, got %s", asst.ContentBlocks[0].Name)
	}
}

func TestAnthropicRequestToUnified_ToolResultInUserMessage(t *testing.T) {
	reqBody := `{
		"model": "claude-3",
		"max_tokens": 100,
		"messages": [
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "toolu_1", "content": "result text"}]}
		]
	}`
	var req AnthropicReq
	if err := json.Unmarshal([]byte(reqBody), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	unified, err := AnthropicRequestToUnified(&req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unified.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(unified.Messages))
	}
	um := unified.Messages[0]
	if um.Role != "user" {
		t.Errorf("expected user role, got %s", um.Role)
	}
	if len(um.ContentBlocks) != 1 || um.ContentBlocks[0].Type != "tool_result" {
		t.Errorf("expected tool_result block, got: %+v", um.ContentBlocks)
	}
	if um.ContentBlocks[0].ToolUseID != "toolu_1" {
		t.Errorf("expected tool_use_id toolu_1, got %s", um.ContentBlocks[0].ToolUseID)
	}
}

func TestAnthropicResponseFromUnified_TextOnly(t *testing.T) {
	resp := &models.UnifiedResponse{
		ID:         "msg_001",
		Model:      "claude-3",
		Role:       "assistant",
		Content:    "hello world",
		StopReason: "end_turn",
		Usage:      models.UnifiedUsage{InputTokens: 5, OutputTokens: 2},
	}
	anth := AnthropicResponseFromUnified(resp)
	if anth.Role != "assistant" {
		t.Errorf("expected role assistant, got %s", anth.Role)
	}
	if len(anth.Content) != 1 || anth.Content[0].Text != "hello world" {
		t.Errorf("expected text content, got: %+v", anth.Content)
	}
	if anth.StopReason != "stop" {
		t.Errorf("expected stop (from end_turn), got %s", anth.StopReason)
	}
}

func TestAnthropicResponseFromUnified_WithToolCalls(t *testing.T) {
	resp := &models.UnifiedResponse{
		ID:         "msg_002",
		Model:      "claude-3",
		Role:       "assistant",
		Content:    "using tool",
		ToolCalls: []models.ToolCall{{
			ID:       "toolu_abc",
			Type:     "function",
			Function: models.ToolCallFunction{Name: "search", Arguments: `{"q":"hi"}`},
		}},
		StopReason: "tool_use",
		Usage:      models.UnifiedUsage{InputTokens: 10, OutputTokens: 20},
	}
	anth := AnthropicResponseFromUnified(resp)
	if len(anth.Content) != 2 {
		t.Fatalf("expected 2 content blocks (text + tool_use), got %d", len(anth.Content))
	}
	if anth.Content[0].Type != "text" || anth.Content[0].Text != "using tool" {
		t.Errorf("expected text block first, got: %+v", anth.Content[0])
	}
	if anth.Content[1].Type != "tool_use" || anth.Content[1].ID != "toolu_abc" || anth.Content[1].Name != "search" {
		t.Errorf("expected tool_use block, got: %+v", anth.Content[1])
	}
	if anth.StopReason != "tool_calls" {
		t.Errorf("expected tool_calls (from tool_use), got %s", anth.StopReason)
	}
}

func TestAnthropicStreamEventToUnified_ToolUseBlockStart(t *testing.T) {
	data := []byte(`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"search","input":{"q":"hello"}}}`)
	events, err := AnthropicStreamEventToUnified("content_block_start", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0] == nil {
		t.Fatal("expected 1 event")
	}
	ev := events[0]
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call delta, got %d", len(ev.ToolCalls))
	}
	tc := ev.ToolCalls[0]
	if tc.Index != 1 || tc.ID != "toolu_1" || tc.Function == nil {
		t.Errorf("tool call delta mismatch: %+v", tc)
	}
	if tc.Function.Name != "search" {
		t.Errorf("expected function name search, got %s", tc.Function.Name)
	}
	if tc.Function.Arguments != `{"q":"hello"}` {
		t.Errorf("expected arguments, got %s", tc.Function.Arguments)
	}
}

func TestAnthropicStreamEventToUnified_InputJSONDelta(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"foo\""}}`)
	events, err := AnthropicStreamEventToUnified("content_block_delta", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0] == nil {
		t.Fatal("expected 1 event")
	}
	ev := events[0]
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call delta, got %d", len(ev.ToolCalls))
	}
	if ev.ToolCalls[0].Function.Arguments != `"foo"` {
		t.Errorf("expected partial_json, got %s", ev.ToolCalls[0].Function.Arguments)
	}
}

func TestAnthropicStreamEventToUnified_TextDelta(t *testing.T) {
	data := []byte(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`)
	events, err := AnthropicStreamEventToUnified("content_block_delta", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0] == nil {
		t.Fatal("expected 1 event")
	}
	if events[0].Content != "hello" {
		t.Errorf("expected content hello, got %q", events[0].Content)
	}
}

func TestMapAnthropicStopReason(t *testing.T) {
	tests := []struct{ input, expected string }{
		{"end_turn", "stop"},
		{"max_tokens", "length"},
		{"tool_use", "tool_calls"},
		{"stop_sequence", "stop_sequence"},
	}
	for _, tt := range tests {
		got := mapAnthropicStopReason(tt.input)
		if got != tt.expected {
			t.Errorf("mapAnthropicStopReason(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}
