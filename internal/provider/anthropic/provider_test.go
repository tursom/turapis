package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/translate"
)

func TestNormalizeToolsToAnthropic_AlreadyAnthropic(t *testing.T) {
	anthTools := json.RawMessage(`[{"name":"search","description":"desc","input_schema":{"type":"object"}}]`)
	result := normalizeToolsToAnthropic(anthTools)
	if string(result) != string(anthTools) {
		t.Errorf("Anthropic tools should pass through unchanged, got: %s", result)
	}
}

func TestNormalizeToolsToAnthropic_OpenAIToAnthropic(t *testing.T) {
	openaiTools := json.RawMessage(`[{"type":"function","function":{"name":"search","description":"find stuff","parameters":{"type":"object"}}}]`)
	result := normalizeToolsToAnthropic(openaiTools)

	var items []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("result not valid JSON: %v, body: %s", err, result)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "search" {
		t.Errorf("expected name=search, got %s", items[0].Name)
	}
	if items[0].Description != "find stuff" {
		t.Errorf("expected description, got %s", items[0].Description)
	}
}

func TestNormalizeToolsToAnthropic_Nil(t *testing.T) {
	result := normalizeToolsToAnthropic(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got: %s", result)
	}
}

func TestNormalizeToolsToAnthropic_Empty(t *testing.T) {
	result := normalizeToolsToAnthropic(json.RawMessage(``))
	if len(result) == 0 {
		return // OK
	}
}

func TestToAnthropicRequest_ToolsAndToolChoice(t *testing.T) {
	toolsJSON := json.RawMessage(`[{"name":"search","input_schema":{"type":"object"}}]`)
	tcJSON := json.RawMessage(`{"type":"auto"}`)
	req := &models.UnifiedRequest{
		Model:      "claude-3",
		System:     "system prompt",
		MaxTokens:  200,
		Tools:      toolsJSON,
		ToolChoice: tcJSON,
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "hello"},
		},
	}
	aReq := toAnthropicRequest(req)
	if string(aReq.Tools) != string(toolsJSON) {
		t.Errorf("tools mismatch: got %s, want %s", aReq.Tools, toolsJSON)
	}
	if string(aReq.ToolChoice) != string(tcJSON) {
		t.Errorf("tool_choice mismatch: got %s, want %s", aReq.ToolChoice, tcJSON)
	}
}

func TestToAnthropicRequest_TextMessages(t *testing.T) {
	req := &models.UnifiedRequest{
		Model:     "claude-3",
		System:    "be helpful",
		MaxTokens: 100,
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "hello"},
		},
	}
	aReq := toAnthropicRequest(req)
	if len(aReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(aReq.Messages))
	}
	for i, m := range aReq.Messages {
		if len(m.Content) != 1 || m.Content[0].Type != "text" {
			t.Errorf("message %d: expected single text block, got: %+v", i, m.Content)
		}
	}
	if aReq.Messages[0].Content[0].Text != "hi" {
		t.Errorf("msg 0 text mismatch")
	}
}

func TestToAnthropicRequest_ToolUseBlocks(t *testing.T) {
	req := &models.UnifiedRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "search things"},
			{Role: "assistant", Content: "",
				ContentBlocks: []models.ContentBlock{{
					Type: "tool_use", ID: "toolu_1", Name: "search",
					Input: json.RawMessage(`{"q":"test"}`),
				}},
			},
		},
	}
	aReq := toAnthropicRequest(req)
	if len(aReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(aReq.Messages))
	}
	asst := aReq.Messages[1]
	if asst.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", asst.Role)
	}
	if len(asst.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(asst.Content))
	}
	block := asst.Content[0]
	if block.Type != "tool_use" || block.ID != "toolu_1" || block.Name != "search" {
		t.Errorf("tool_use block mismatch: %+v", block)
	}
}

func TestToAnthropicRequest_ToolResultBlocks(t *testing.T) {
	req := &models.UnifiedRequest{
		Model:     "claude-3",
		MaxTokens: 100,
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "search things"},
			{Role: "user", Content: "",
				ContentBlocks: []models.ContentBlock{{
					Type:      "tool_result",
					ToolUseID: "toolu_1",
					Content:   json.RawMessage(`"result text"`),
					IsError:   false,
				}},
			},
		},
	}
	aReq := toAnthropicRequest(req)
	if len(aReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(aReq.Messages))
	}
	toolResult := aReq.Messages[1]
	if toolResult.Role != "user" {
		t.Errorf("expected user role for tool result, got %s", toolResult.Role)
	}
	if len(toolResult.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(toolResult.Content))
	}
	block := toolResult.Content[0]
	if block.Type != "tool_result" || block.ToolUseID != "toolu_1" {
		t.Errorf("tool_result block mismatch: %+v", block)
	}
}

func TestToUnifiedResponse_TextOnly(t *testing.T) {
	anthResp := &translate.AnthropicResp{
		ID:         "resp-1",
		Model:      "claude-3",
		Role:       "assistant",
		Content:    []translate.AnthropicContent{{Type: "text", Text: "hello world"}},
		StopReason: "end_turn",
		Usage:      translate.AnthropicUsage{InputTokens: 5, OutputTokens: 10},
	}
	unified := toUnifiedResponse(anthResp)
	if unified.Content != "hello world" {
		t.Errorf("content mismatch: %q", unified.Content)
	}
	if unified.StopReason != "end_turn" {
		t.Errorf("stop_reason mismatch: %s", unified.StopReason)
	}
	if len(unified.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(unified.ToolCalls))
	}
}

func TestToUnifiedResponse_WithToolUses(t *testing.T) {
	anthResp := &translate.AnthropicResp{
		ID:   "resp-2",
		Model: "claude-3",
		Role:  "assistant",
		Content: []translate.AnthropicContent{
			{Type: "text", Text: "using tool"},
			{Type: "tool_use", ID: "toolu_abc", Name: "search", Input: json.RawMessage(`{"q":"test"}`)},
		},
		StopReason: "tool_use",
		Usage:      translate.AnthropicUsage{InputTokens: 5, OutputTokens: 10},
	}
	unified := toUnifiedResponse(anthResp)
	if unified.Content != "using tool" {
		t.Errorf("content mismatch: %q", unified.Content)
	}
	if len(unified.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(unified.ToolCalls))
	}
	tc := unified.ToolCalls[0]
	if tc.ID != "toolu_abc" || tc.Function.Name != "search" || tc.Function.Arguments != `{"q":"test"}` {
		t.Errorf("tool call mismatch: %+v", tc)
	}
}
