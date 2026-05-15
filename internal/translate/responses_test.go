package translate

import (
	"encoding/json"
	"testing"

	"github.com/tursom/turapis/internal/models"
)

func TestResponsesRequestToUnified_ToolsPassthrough(t *testing.T) {
	toolsJSON := json.RawMessage(`[{"type":"function","name":"search","parameters":{"type":"object"}}]`)
	req := &ResponsesReq{
		Model: "gpt-4",
		Input: responsesInput{StringVal: "search for things"},
		Tools: toolsJSON,
	}
	unified, err := ResponsesRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(unified.Tools) != string(toolsJSON) {
		t.Errorf("tools not passed through, got: %s", unified.Tools)
	}
}

func TestResponsesRequestToUnified_ToolsEmptyIsOK(t *testing.T) {
	req := &ResponsesReq{
		Model: "gpt-4",
		Input: responsesInput{StringVal: "hello"},
	}
	unified, err := ResponsesRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unified.Tools != nil {
		t.Errorf("expected nil tools, got: %s", unified.Tools)
	}
}

func TestResponsesRequestToUnified_TextInput(t *testing.T) {
	req := &ResponsesReq{
		Model: "gpt-4",
		Input: responsesInput{StringVal: "hello world"},
	}
	unified, err := ResponsesRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unified.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(unified.Messages))
	}
	if unified.Messages[0].Role != "user" || unified.Messages[0].Content != "hello world" {
		t.Errorf("message mismatch: %+v", unified.Messages[0])
	}
}

func TestResponsesRequestToUnified_InstructionsAsSystem(t *testing.T) {
	req := &ResponsesReq{
		Model:        "gpt-4",
		Instructions: "be helpful",
		Input:        responsesInput{StringVal: "hi"},
	}
	unified, err := ResponsesRequestToUnified(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if unified.System != "be helpful" {
		t.Errorf("expected system=be helpful, got %q", unified.System)
	}
}

func TestResponsesRequestToUnified_FunctionCallInput(t *testing.T) {
	reqBody := `{
		"model": "gpt-4",
		"input": [
			{"role": "user", "content": "do things"},
			{"role": "assistant", "name": "search", "call_id": "call_1", "arguments": "{\"q\":\"test\"}", "content": [{"type": "output_text", "text": ""}]}
		]
	}`
	var req ResponsesReq
	if err := json.Unmarshal([]byte(reqBody), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	unified, err := ResponsesRequestToUnified(&req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(unified.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(unified.Messages))
	}
	asst := unified.Messages[1]
	if len(asst.ContentBlocks) != 1 || asst.ContentBlocks[0].Type != "tool_use" {
		t.Errorf("expected tool_use block, got: %+v", asst.ContentBlocks)
	}
	if asst.ContentBlocks[0].ID != "call_1" || asst.ContentBlocks[0].Name != "search" {
		t.Errorf("tool_use mismatch: id=%s name=%s", asst.ContentBlocks[0].ID, asst.ContentBlocks[0].Name)
	}
}

func TestResponsesResponseFromUnified_TextOnly(t *testing.T) {
	resp := &models.UnifiedResponse{
		ID:      "resp-1",
		Model:   "gpt-4",
		Role:    "assistant",
		Content: "hello there",
		Usage:   models.UnifiedUsage{InputTokens: 5, OutputTokens: 2},
	}
	r := ResponsesResponseFromUnified(resp)
	if len(r.Output) != 1 || r.Output[0].Type != "message" {
		t.Errorf("expected 1 message output, got: %+v", r.Output)
	}
	if len(r.Output[0].Content) != 1 || r.Output[0].Content[0].Text != "hello there" {
		t.Errorf("content mismatch: %+v", r.Output[0].Content)
	}
}

func TestResponsesResponseFromUnified_WithToolCalls(t *testing.T) {
	resp := &models.UnifiedResponse{
		ID:      "resp-2",
		Model:   "gpt-4",
		Role:    "assistant",
		Content: "using tool",
		ToolCalls: []models.ToolCall{{
			ID:       "call_1",
			Type:     "function",
			Function: models.ToolCallFunction{Name: "search", Arguments: `{"q":"test"}`},
		}},
		Usage: models.UnifiedUsage{InputTokens: 10, OutputTokens: 20},
	}
	r := ResponsesResponseFromUnified(resp)
	if len(r.Output) != 2 {
		t.Fatalf("expected 2 outputs (message + function_call), got %d", len(r.Output))
	}
	if r.Output[0].Type != "message" {
		t.Errorf("expected message first, got %s", r.Output[0].Type)
	}
	fc := r.Output[1]
	if fc.Type != "function_call" || fc.Name != "search" || fc.Arguments != `{"q":"test"}` {
		t.Errorf("function_call mismatch: %+v", fc)
	}
}

func TestResponsesRequestToUnified_ToolResultInput(t *testing.T) {
	reqBody := `{
		"model": "gpt-4",
		"input": [
			{"role": "user", "content": "do things"},
			{"role": "assistant", "name": "search", "call_id": "call_1", "arguments": "{}"},
			{"role": "assistant", "call_id": "call_1", "output": "\"result content\""}
		]
	}`
	var req ResponsesReq
	if err := json.Unmarshal([]byte(reqBody), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	unified, err := ResponsesRequestToUnified(&req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// third message should be tool_result with role=user
	if len(unified.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(unified.Messages))
	}
	toolResult := unified.Messages[2]
	if toolResult.Role != "user" {
		t.Errorf("expected tool result role=user, got %s", toolResult.Role)
	}
	if len(toolResult.ContentBlocks) != 1 || toolResult.ContentBlocks[0].Type != "tool_result" {
		t.Errorf("expected tool_result block, got: %+v", toolResult.ContentBlocks)
	}
	if toolResult.ContentBlocks[0].ToolUseID != "call_1" {
		t.Errorf("expected tool_use_id=call_1, got %s", toolResult.ContentBlocks[0].ToolUseID)
	}
}

func TestResponsesStreamEventToUnified_FunctionCallOutputItemAdded(t *testing.T) {
	data := []byte(`{"item":{"type":"function_call","id":"fc_1","name":"search","arguments":"{\"q\":\"hi\"}"}}`)
	ev, err := ResponsesStreamEventToUnified("response.output_item.added", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev == nil {
		t.Fatal("expected event, got nil")
	}
	if len(ev.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call delta, got %d", len(ev.ToolCalls))
	}
	if ev.ToolCalls[0].ID != "fc_1" || ev.ToolCalls[0].Function.Name != "search" || ev.ToolCalls[0].Function.Arguments != `{"q":"hi"}` {
		t.Errorf("tool call delta mismatch: %+v", ev.ToolCalls[0])
	}
}

func TestResponsesStreamEventToUnified_FunctionCallArgumentsDelta(t *testing.T) {
	data := []byte(`{"delta":" \"foo\""}`)
	ev, err := ResponsesStreamEventToUnified("response.function_call_arguments.delta", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev == nil {
		t.Fatal("expected event, got nil")
	}
	if len(ev.ToolCalls) != 1 || ev.ToolCalls[0].Function.Arguments != ` "foo"` {
		t.Errorf("expected arguments delta, got: %+v", ev.ToolCalls)
	}
}

func TestResponsesStreamEventToUnified_TextDelta(t *testing.T) {
	data := []byte(`{"delta":"hello"}`)
	ev, err := ResponsesStreamEventToUnified("response.text.delta", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev == nil {
		t.Fatal("expected event, got nil")
	}
	if ev.Content != "hello" {
		t.Errorf("expected content hello, got %q", ev.Content)
	}
}
