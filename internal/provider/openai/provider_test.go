package openai

import (
	"encoding/json"
	"testing"

	"github.com/tursom/turapis/internal/models"
)

func TestNormalizeRole_Developer(t *testing.T) {
	if got := normalizeRole("developer"); got != "system" {
		t.Errorf("expected developer→system, got %s", got)
	}
}

func TestNormalizeRole_Passthrough(t *testing.T) {
	for _, role := range []string{"system", "user", "assistant", "tool"} {
		if got := normalizeRole(role); got != role {
			t.Errorf("normalizeRole(%s) = %s, want %s", role, got, role)
		}
	}
}

func TestNormalizeToolsToOpenAI_AlreadyOpenAI(t *testing.T) {
	openaiTools := json.RawMessage(`[{"type":"function","function":{"name":"search","description":"desc","parameters":{"type":"object"}}}]`)
	result := normalizeToolsToOpenAI(openaiTools)
	if string(result) != string(openaiTools) {
		t.Errorf("OpenAI tools should pass through unchanged, got: %s", result)
	}
}

func TestNormalizeToolsToOpenAI_AnthropicToOpenAI(t *testing.T) {
	anthTools := json.RawMessage(`[{"name":"search","description":"find things","input_schema":{"type":"object"}}]`)
	result := normalizeToolsToOpenAI(anthTools)

	var items []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("result not valid JSON: %v, body: %s", err, result)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Type != "function" {
		t.Errorf("expected type=function, got %s", items[0].Type)
	}
	if items[0].Function.Name != "search" {
		t.Errorf("expected name=search, got %s", items[0].Function.Name)
	}
	if items[0].Function.Description != "find things" {
		t.Errorf("expected description, got %s", items[0].Function.Description)
	}
}

func TestNormalizeToolsToOpenAI_Nil(t *testing.T) {
	result := normalizeToolsToOpenAI(nil)
	if result != nil {
		t.Errorf("expected nil for nil input, got: %s", result)
	}
}

func TestNormalizeToolsToOpenAI_Empty(t *testing.T) {
	result := normalizeToolsToOpenAI(json.RawMessage(``))
	if len(result) == 0 {
		return // OK
	}
}

func TestToOpenAIMessages_DeveloperRoleInMessages(t *testing.T) {
	req := &models.UnifiedRequest{
		Model: "gpt-4",
		Messages: []models.UnifiedMessage{
			{Role: "developer", Content: "should become system"},
			{Role: "user", Content: "hello"},
		},
	}
	msgs := toOpenAIMessages(req)
	// developer → system
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (developer→system + user), got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "should become system" {
		t.Errorf("developer message not normalized: role=%s content=%q", msgs[0].Role, msgs[0].Content)
	}
	if msgs[1].Role != "user" {
		t.Errorf("expected user role, got %s", msgs[1].Role)
	}
}

func TestToOpenAIMessages_SystemField(t *testing.T) {
	req := &models.UnifiedRequest{
		Model:  "gpt-4",
		System: "system prompt",
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "hello"},
		},
	}
	msgs := toOpenAIMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (system + user), got %d", len(msgs))
	}
	if msgs[0].Role != "system" || msgs[0].Content != "system prompt" {
		t.Errorf("system message mismatch: %+v", msgs[0])
	}
}

func TestToOpenAIMessages_AssistantToolCalls(t *testing.T) {
	req := &models.UnifiedRequest{
		Model: "gpt-4",
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "search things"},
			{Role: "assistant", Content: "",
				ContentBlocks: []models.ContentBlock{{
					Type: "tool_use", ID: "call_1", Name: "search",
					Input: json.RawMessage(`{"q":"test"}`),
				}},
			},
		},
	}
	msgs := toOpenAIMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	asst := msgs[1]
	if asst.Role != "assistant" {
		t.Errorf("expected assistant role, got %s", asst.Role)
	}
	if len(asst.ToolCalls) == 0 {
		t.Fatal("expected non-empty tool_calls")
	}
}

func TestToOpenAIMessages_ToolResultMessage(t *testing.T) {
	req := &models.UnifiedRequest{
		Model: "gpt-4",
		Messages: []models.UnifiedMessage{
			{Role: "user", Content: "hi"},
			{Role: "user", Content: "",
				ContentBlocks: []models.ContentBlock{{
					Type:      "tool_result",
					ToolUseID: "call_1",
					Content:   json.RawMessage(`"result value"`),
				}},
			},
		},
	}
	msgs := toOpenAIMessages(req)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	toolMsg := msgs[1]
	if toolMsg.Role != "tool" {
		t.Errorf("expected tool role, got %s", toolMsg.Role)
	}
	if toolMsg.ToolCallID != "call_1" {
		t.Errorf("expected tool_call_id=call_1, got %s", toolMsg.ToolCallID)
	}
	// content should be the unquoted string value
	if toolMsg.Content != "result value" {
		t.Errorf("expected content='result value', got %q", toolMsg.Content)
	}
}

func TestBuildOpenAIMsg_DeveloperRole(t *testing.T) {
	m := models.UnifiedMessage{
		Role:    "developer",
		Content: "dev instructions",
	}
	msg := buildOpenAIMsg(m)
	if msg.Role != "system" {
		t.Errorf("expected developer→system, got %s", msg.Role)
	}
	if msg.Content != "dev instructions" {
		t.Errorf("content mismatch: %q", msg.Content)
	}
}

func TestToUnifiedResponse_WithToolCalls(t *testing.T) {
	toolCallJSON := `[{"id":"call_x","type":"function","function":{"name":"calc","arguments":"{}"}}]`
	oaiResp := &chatCompletionResponse{}
	oaiResp.ID = "resp-1"
	oaiResp.Model = "gpt-4"
	oaiResp.Choices = append(oaiResp.Choices, struct {
		Message struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	}{
		Message: struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
		}{
			Role:      "assistant",
			Content:   "",
			ToolCalls: json.RawMessage(toolCallJSON),
		},
		FinishReason: "tool_calls",
	})
	oaiResp.Usage.PromptTokens = 10
	oaiResp.Usage.CompletionTokens = 20

	resp := toUnifiedResponse(oaiResp)
	if resp.StopReason != "tool_calls" {
		t.Errorf("expected stop_reason=tool_calls, got %s", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_x" || tc.Function.Name != "calc" || tc.Function.Arguments != "{}" {
		t.Errorf("tool call mismatch: %+v", tc)
	}
}

func TestConvertResponsesToOpenAI_NamespaceUnwrapping(t *testing.T) {
	responsesTools := json.RawMessage(`[
		{"type":"namespace","name":"mcp__mysql__","description":"MySQL tools","tools":[
			{"type":"function","name":"query","description":"Run SQL","parameters":{"type":"object","properties":{"sql":{"type":"string"}},"required":["sql"]}},
			{"type":"function","name":"insert","description":"Insert row","parameters":{"type":"object"}}
		]},
		{"type":"function","name":"native_search","description":"Search","parameters":{"type":"object"}}
	]`)
	result := normalizeToolsToOpenAI(responsesTools)

	var items []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("result not valid JSON: %v, body: %s", err, result)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items (2 from namespace + 1 native), got %d", len(items))
	}
	if items[0].Function.Name != "mcp__mysql__query" {
		t.Errorf("expected mcp__mysql__query, got %s", items[0].Function.Name)
	}
	if items[1].Function.Name != "mcp__mysql__insert" {
		t.Errorf("expected mcp__mysql__insert, got %s", items[1].Function.Name)
	}
	if items[2].Function.Name != "native_search" {
		t.Errorf("expected native_search, got %s", items[2].Function.Name)
	}
}

func TestExtractWebSearchFromTools_HasWebSearch(t *testing.T) {
	tools := json.RawMessage(`[
		{"type":"web_search","search_context_size":"high","user_location":{"type":"approximate","country":"US"}},
		{"type":"function","name":"search","parameters":{"type":"object"}}
	]`)
	result := extractWebSearchFromTools(tools)
	if result == nil {
		t.Fatal("expected web_search_options, got nil")
	}
	var opts map[string]interface{}
	if err := json.Unmarshal(result, &opts); err != nil {
		t.Fatalf("result not valid JSON: %v", err)
	}
	if opts["search_context_size"] != "high" {
		t.Errorf("expected search_context_size=high, got %v", opts["search_context_size"])
	}
	if opts["user_location"] == nil {
		t.Error("expected user_location to be present")
	}
}

func TestExtractWebSearchFromTools_NoWebSearch(t *testing.T) {
	tools := json.RawMessage(`[{"type":"function","name":"search","parameters":{"type":"object"}}]`)
	result := extractWebSearchFromTools(tools)
	if result != nil {
		t.Errorf("expected nil for no web_search tools, got %s", result)
	}
}

func TestNormalizeToolsToOpenAI_ResponsesFormat(t *testing.T) {
	responsesTools := json.RawMessage(`[{"type":"function","name":"search","description":"find things","parameters":{"type":"object"}}]`)
	result := normalizeToolsToOpenAI(responsesTools)

	var items []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		} `json:"function"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		t.Fatalf("result not valid JSON: %v, body: %s", err, result)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Function.Name != "search" {
		t.Errorf("expected name=search, got %s", items[0].Function.Name)
	}
	if items[0].Function.Parameters == nil || string(items[0].Function.Parameters) == "null" {
		t.Error("expected non-null parameters")
	}
}

func TestFlattenNamespaceContainers_Mixed(t *testing.T) {
	items := []json.RawMessage{
		json.RawMessage(`{"type":"namespace","name":"mcp__srv__","description":"...","tools":[{"type":"function","name":"tool1","parameters":{}},{"type":"function","name":"tool2","parameters":{}}]}`),
		json.RawMessage(`{"type":"function","name":"native","parameters":{}}`),
		json.RawMessage(`{"type":"web_search","search_context_size":"medium"}`),
	}
	flat := flattenNamespaceContainers(items)
	// namespace unwrapped to 2 + 1 native + 1 web_search = 4
	if len(flat) != 4 {
		t.Fatalf("expected 4 items, got %d", len(flat))
	}
	// Check namespace tools are prefixed
	var t1 struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	json.Unmarshal(flat[0], &t1)
	if t1.Name != "mcp__srv__tool1" {
		t.Errorf("expected mcp__srv__tool1, got %s", t1.Name)
	}
}
