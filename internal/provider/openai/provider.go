package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
	"github.com/tursom/turapis/internal/search"
)

// OpenAIProvider 实现 OpenAI 协议的 Provider
type OpenAIProvider struct {
	name           string
	url            string
	apiKey         string
	client         *http.Client
	supportedTools map[string]bool
	searxngURL     string
	nsMap          map[string]string
}

func New(name, baseURL, apiKey string, supportedTools []string) *OpenAIProvider {
	st := make(map[string]bool, len(supportedTools))
	for _, t := range supportedTools {
		st[t] = true
	}
	return &OpenAIProvider{
		name:           name,
		url:            strings.TrimSuffix(baseURL, "/"),
		apiKey:         apiKey,
		client: &http.Client{
			Transport: provider.SharedTransport(),
			Timeout:   60 * time.Second,
		},
		supportedTools: st,
	}
}

func (p *OpenAIProvider) Name() string                 { return p.name }
func (p *OpenAIProvider) Protocol() models.ProtocolType { return models.ProtocolOpenAI }
func (p *OpenAIProvider) SupportsTool(name string) bool {
	if p.supportedTools == nil {
		return true
	}
	return p.supportedTools[name]
}

func (p *OpenAIProvider) buildNamespaceMap(tools json.RawMessage) {
	if len(tools) == 0 {
		return
	}
	var items []json.RawMessage
	if json.Unmarshal(tools, &items) != nil {
		return
	}
	p.nsMap = make(map[string]string)
	for _, item := range items {
		var ns struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Tools json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(item, &ns) != nil || ns.Type != "namespace" {
			continue
		}
		var nested []struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(ns.Tools, &nested) != nil {
			continue
		}
		for _, child := range nested {
			if child.Name != "" {
				p.nsMap[child.Name] = ns.Name
			}
		}
	}
}

func (p *OpenAIProvider) resolveToolName(name string) string {
	if p.nsMap == nil {
		return name
	}
	if prefix, ok := p.nsMap[name]; ok {
		return prefix + name
	}
	return name
}

func (p *OpenAIProvider) SetSearXNG(url string) { p.searxngURL = url }

func (p *OpenAIProvider) injectSearchIfNeeded(ctx context.Context, req *models.UnifiedRequest) {
	if p.searxngURL == "" || !hasWebSearchTool(req.Tools) || p.SupportsTool("web_search") {
		return
	}
	query := extractUserQuery(req)
	if query == "" {
		return
	}
	slog.Info("injecting_local_web_search", "provider", p.name, "query", query)
	sc := search.NewClient(p.searxngURL, 30*time.Second)
	results, err := sc.Search(ctx, search.SearchInput{Query: query, Limit: 5})
	if err != nil {
		slog.Warn("local_web_search_failed", "error", err)
		return
	}
	context := results.FormatAsContext()
	req.System = req.System + context
	req.Tools = removeWebSearchTool(req.Tools)
}

func hasWebSearchTool(tools json.RawMessage) bool {
	if len(tools) == 0 {
		return false
	}
	var items []json.RawMessage
	if json.Unmarshal(tools, &items) != nil {
		return false
	}
	for _, item := range items {
		var t struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &t) == nil && (t.Type == "web_search" || t.Type == "web_search_preview") {
			return true
		}
	}
	return false
}

func extractUserQuery(req *models.UnifiedRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			if req.Messages[i].Content != "" {
				return req.Messages[i].Content
			}
		}
	}
	return ""
}

func removeWebSearchTool(tools json.RawMessage) json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(tools, &items) != nil {
		return tools
	}
	var filtered []json.RawMessage
	for _, item := range items {
		var t struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &t) == nil && (t.Type == "web_search" || t.Type == "web_search_preview") {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return nil
	}
	out, _ := json.Marshal(filtered)
	return out
}

// ChatCompletion 发送非流式请求
func (p *OpenAIProvider) ChatCompletion(ctx context.Context, req *models.UnifiedRequest) (*models.UnifiedResponse, error) {
	p.injectSearchIfNeeded(ctx, req)
	p.buildNamespaceMap(req.Tools)
	body := chatCompletionRequest{
		Model:            req.Model,
		Messages:         toOpenAIMessages(req),
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		Stop:             req.Stop,
		Stream:           false,
		Tools:            normalizeToolsToOpenAI(req.Tools),
		ToolChoice:       req.ToolChoice,
		WebSearchOptions: extractWebSearchFromTools(req.Tools),
	}

	resp, err := p.doRequest(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		return nil, &models.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	var oaiResp chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&oaiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return toUnifiedResponse(&oaiResp), nil
}

// ChatCompletionStream 发送流式请求
func (p *OpenAIProvider) ChatCompletionStream(ctx context.Context, req *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error) {
	p.injectSearchIfNeeded(ctx, req)
	p.buildNamespaceMap(req.Tools)
	body := chatCompletionRequest{
		Model:       req.Model,
		Messages:    toOpenAIMessages(req),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      true,
		StreamOptions: &streamOptions{
			IncludeUsage: true,
		},
		Tools:            normalizeToolsToOpenAI(req.Tools),
		ToolChoice:       req.ToolChoice,
		WebSearchOptions: extractWebSearchFromTools(req.Tools),
	}

	resp, err := p.doRequest(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		resp.Body.Close()
		return nil, &models.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	ch := make(chan models.UnifiedStreamEvent, 64)
	go func() {
		defer close(ch)
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 1<<20), 1<<20)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()
			if line == "" || !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				select {
				case ch <- models.UnifiedStreamEvent{Type: models.StreamEventStop}:
				case <-ctx.Done():
				}
				return
			}

			var chunk chatCompletionStreamChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			if len(chunk.Choices) > 0 {
				c := chunk.Choices[0]
				emitEvent := false
				event := models.UnifiedStreamEvent{Type: models.StreamEventDelta}

			if c.Delta.Content != "" {
				event.Content = c.Delta.Content
				emitEvent = true
			}
			if len(c.Delta.ToolCalls) > 0 {
				var streamTCs []streamToolCallDelta
				if json.Unmarshal(c.Delta.ToolCalls, &streamTCs) == nil {
					for _, tc := range streamTCs {
						tcd := models.ToolCallDelta{Index: tc.Index, ID: tc.ID, Type: tc.Type}
						if tc.Function != nil {
							tcd.Function = &models.ToolCallFunctionDelta{
								Name:      p.resolveToolName(tc.Function.Name),
								Arguments: tc.Function.Arguments,
							}
						}
							event.ToolCalls = append(event.ToolCalls, tcd)
							emitEvent = true
						}
					}
				}
				if c.FinishReason != "" {
					event.StopReason = c.FinishReason
					emitEvent = true
				}
				if emitEvent {
					select {
					case ch <- event:
					case <-ctx.Done():
						return
					}
				}
			}

			if chunk.Usage != nil && (chunk.Usage.PromptTokens > 0 || chunk.Usage.CompletionTokens > 0) {
				select {
				case ch <- models.UnifiedStreamEvent{
					Type: models.StreamEventUsage,
					Usage: &models.UnifiedUsage{
						InputTokens:  chunk.Usage.PromptTokens,
						OutputTokens: chunk.Usage.CompletionTokens,
					},
				}:
				case <-ctx.Done():
					return
				}
			}
		}

		select {
		case ch <- models.UnifiedStreamEvent{Type: models.StreamEventStop}:
		case <-ctx.Done():
		}
	}()

	return ch, nil
}

// ListModels 列出可用模型
func (p *OpenAIProvider) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	resp, err := p.doGet(ctx, "/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		return nil, &models.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	var result modelsListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	infos := make([]models.ModelInfo, len(result.Data))
	for i, m := range result.Data {
		infos[i] = models.ModelInfo{
			ID:       m.ID,
			Name:     m.ID,
			Provider: p.name,
		}
	}
	return infos, nil
}

func (p *OpenAIProvider) doRequest(ctx context.Context, path string, body interface{}) (*http.Response, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	fullURL := p.url + path
	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: ctx_err=%v err=%w", fullURL, ctx.Err(), err)
	}
	return resp, nil
}

func (p *OpenAIProvider) doGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.url+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	return p.client.Do(req)
}

// --- OpenAI 协议类型 ---

type chatCompletionRequest struct {
	Model            string          `json:"model"`
	Messages         []openaiMsg     `json:"messages"`
	MaxTokens        int             `json:"max_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	Stop             []string        `json:"stop,omitempty"`
	Stream           bool            `json:"stream"`
	StreamOptions    *streamOptions  `json:"stream_options,omitempty"`
	Tools            json.RawMessage `json:"tools,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	WebSearchOptions json.RawMessage `json:"web_search_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiMsg struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ReasoningContent string          `json:"reasoning_content"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string          `json:"role"`
			Content   string          `json:"content"`
			ToolCalls json.RawMessage `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type chatCompletionStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

type modelsListResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// streamToolCallDelta OpenAI 流式 tool_calls 中的单个 element
type streamToolCallDelta struct {
	Index    int                   `json:"index"`
	ID       string                `json:"id,omitempty"`
	Type     string                `json:"type,omitempty"`
	Function *streamToolCallFunc   `json:"function,omitempty"`
}

type streamToolCallFunc struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// toolCallObj 非流式响应中 tool_calls element
type toolCallObj struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function toolCallFuncObj `json:"function"`
}

type toolCallFuncObj struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toOpenAIMessages(req *models.UnifiedRequest) []openaiMsg {
	msgs := make([]openaiMsg, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openaiMsg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		role := normalizeRole(m.Role)
		oai := openaiMsg{Role: role, Content: m.Content}
		if len(m.ContentBlocks) > 0 {
			oai = buildOpenAIMsg(m)
		}
		msgs = append(msgs, oai)
	}
	return msgs
}

func buildOpenAIMsg(m models.UnifiedMessage) openaiMsg {
	oai := openaiMsg{Role: normalizeRole(m.Role), Content: m.Content, ReasoningContent: ""}
	if m.Role == "user" {
		// tool_result 消息: role 改为 tool
		for _, block := range m.ContentBlocks {
			if block.Type == "tool_result" {
				oai.Role = "tool"
				oai.ToolCallID = block.ToolUseID
				if block.Content != nil {
					// Content 是 string 的 JSON 表示，去掉外层引号
					var s string
					if json.Unmarshal(block.Content, &s) == nil {
						oai.Content = s
					} else {
						oai.Content = string(block.Content)
					}
				}
				return oai
			}
		}
		return oai
	}
	if m.Role == "assistant" {
		var tcs []toolCallObj
		for _, block := range m.ContentBlocks {
			if block.Type == "tool_use" {
				tcs = append(tcs, toolCallObj{
					ID:   block.ID,
					Type: "function",
					Function: toolCallFuncObj{
						Name:      block.Name,
						Arguments: string(block.Input),
					},
				})
			}
		}
		if len(tcs) > 0 {
			b, _ := json.Marshal(tcs)
			oai.ToolCalls = json.RawMessage(b)
		}
	}
	return oai
}

// normalizeToolsToOpenAI 将 tools 转换为 OpenAI Chat Completions 格式
// 支持三种输入格式:
//   - OpenAI Chat Completions: {"type":"function","function":{"name":"...","parameters":{...}}}
//   - OpenAI Responses API:   {"type":"function","name":"...","description":"...","parameters":{...}}
//   - Anthropic:              {"name":"...","description":"...","input_schema":{...}}
func normalizeToolsToOpenAI(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 {
		return tools
	}
	var items []json.RawMessage
	if err := json.Unmarshal(tools, &items); err != nil || len(items) == 0 {
		return tools
	}
	// 检查第一个元素是否有 function 字段 (OpenAI Chat Completions 格式)
	var check struct {
		Function json.RawMessage `json:"function"`
	}
	if json.Unmarshal(items[0], &check) == nil && check.Function != nil {
		// 已是 OpenAI Chat 格式，过滤空 name 的工具
		return filterOpenAITools(items)
	}
	// 检查是否是 Responses API 格式: type="function" 或 "namespace", 有 name，无 function 包裹
	var responsesCheck struct {
		Type       string          `json:"type"`
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if json.Unmarshal(items[0], &responsesCheck) == nil && (responsesCheck.Type == "function" || responsesCheck.Type == "namespace") {
		// Responses API → OpenAI Chat Completions 转换（包裹 function 字段）
		return convertResponsesToOpenAI(items)
	}
	// 检查是否是 Anthropic 格式：有 name 和 input_schema
	var anthCheck struct {
		Name        string          `json:"name"`
		InputSchema json.RawMessage `json:"input_schema"`
		Description string          `json:"description"`
	}
	if json.Unmarshal(items[0], &anthCheck) == nil && anthCheck.Name != "" {
		// Anthropic → OpenAI Chat Completions 转换
		var result []json.RawMessage
		for _, item := range items {
			var at struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"input_schema"`
			}
			if err := json.Unmarshal(item, &at); err != nil {
				return tools // 无法识别，原样返回
			}
			if at.Name == "" {
				slog.Warn("skipping_tool_with_empty_name", "index", len(result))
				continue
			}
			oai := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":        at.Name,
					"description": at.Description,
					"parameters":  at.InputSchema,
				},
			}
			b, _ := json.Marshal(oai)
			result = append(result, b)
		}
		if len(result) == 0 {
			return nil
		}
		out, _ := json.Marshal(result)
		return out
	}
	return tools
}

// convertResponsesToOpenAI 将 Responses API 格式转换为 OpenAI Chat Completions 格式
// Responses API:  {"type":"function","name":"...","description":"...","parameters":{...}}
// Chat Completions: {"type":"function","function":{"name":"...","description":"...","parameters":{...}}}
func convertResponsesToOpenAI(items []json.RawMessage) json.RawMessage {
	flat := flattenNamespaceContainers(items)
	var result []json.RawMessage
	for _, item := range flat {
		var rt struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
			Format      json.RawMessage `json:"format"`
		}
		if err := json.Unmarshal(item, &rt); err != nil {
			slog.Warn("skipping_unparsable_tool", "index", len(result), "raw", string(item))
			return nil
		}
		if rt.Type != "function" || rt.Name == "" {
			allFields := collectToolFields(item)
			if rt.Type == "web_search" || rt.Type == "web_search_preview" {
				slog.Info("skipping_web_search_tool_extracted_as_options",
					"index", len(result), "fields", allFields,
				)
			} else if rt.Type == "custom" && rt.Name != "" {
				oai := convertCustomTool(rt.Name, rt.Description, rt.Format)
				if oai != nil {
					result = append(result, oai)
					slog.Info("converted_custom_tool_to_function", "name", rt.Name)
				} else {
					slog.Warn("skipping_non_function_tool_in_responses_format",
						"type", rt.Type, "name", rt.Name, "index", len(result),
						"fields", allFields, "raw", string(item),
					)
				}
			} else {
				slog.Warn("skipping_non_function_tool_in_responses_format",
					"type", rt.Type, "name", rt.Name, "index", len(result),
					"fields", allFields, "raw", string(item),
				)
			}
			continue
		}
		if len(rt.Parameters) == 0 || string(rt.Parameters) == "null" {
			rt.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		oai := map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        rt.Name,
				"description": rt.Description,
				"parameters":  rt.Parameters,
			},
		}
		b, _ := json.Marshal(oai)
		result = append(result, b)
	}
	if len(result) == 0 {
		return nil
	}
	out, _ := json.Marshal(result)
	return out
}

// convertCustomTool 将 codex 自定义工具（lark 语法格式）转为标准 OpenAI function
// 例如 apply_patch: 把 lark 语法转成 string 参数 + 格式描述
func convertCustomTool(name, description string, format json.RawMessage) json.RawMessage {
	if name == "" {
		return nil
	}
	var cf struct {
		Type       string `json:"type"`
		Syntax     string `json:"syntax"`
		Definition string `json:"definition"`
	}
	if err := json.Unmarshal(format, &cf); err != nil || cf.Definition == "" {
		return nil
	}
	desc := description
	if desc == "" {
		desc = fmt.Sprintf("Use the `%s` tool.", name)
	}
	desc += fmt.Sprintf("\n\nOutput format (must match exactly):\n```\n%s\n```", cf.Definition)

	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patch": map[string]interface{}{
				"type":        "string",
				"description": "The patch content following the format above",
			},
		},
		"required": []string{"patch"},
	}
	oai := map[string]interface{}{
		"type": "function",
		"function": map[string]interface{}{
			"name":        name,
			"description": desc,
			"parameters":  params,
		},
	}
	b, _ := json.Marshal(oai)
	return b
}

func flattenNamespaceContainers(items []json.RawMessage) []json.RawMessage {
	var flat []json.RawMessage
	for _, item := range items {
		var ns struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Tools json.RawMessage `json:"tools"`
		}
		if err := json.Unmarshal(item, &ns); err != nil || ns.Type != "namespace" {
			flat = append(flat, item)
			continue
		}
		var nested []json.RawMessage
		if err := json.Unmarshal(ns.Tools, &nested); err != nil {
			slog.Warn("skipping_namespace_with_unparsable_tools", "namespace", ns.Name)
			continue
		}
		for _, child := range nested {
			renamed := prefixToolName(child, ns.Name)
			if renamed != nil {
				flat = append(flat, renamed)
			}
		}
		slog.Info("flattened_namespace", "name", ns.Name, "tool_count", len(nested))
	}
	return flat
}

func prefixToolName(item json.RawMessage, namespace string) json.RawMessage {
	var obj map[string]interface{}
	if err := json.Unmarshal(item, &obj); err != nil {
		return nil
	}
	name, _ := obj["name"].(string)
	if name == "" {
		return nil
	}
	obj["name"] = namespace + name
	b, _ := json.Marshal(obj)
	return b
}

func collectToolFields(item json.RawMessage) []string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(item, &fields); err != nil {
		return nil
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	return keys
}

func extractWebSearchFromTools(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(tools, &items); err != nil {
		return nil
	}
	for _, item := range items {
		var ws struct {
			Type               string          `json:"type"`
			SearchContextSize  string          `json:"search_context_size"`
			UserLocation       json.RawMessage `json:"user_location"`
		}
		if json.Unmarshal(item, &ws) != nil {
			continue
		}
		if ws.Type != "web_search" && ws.Type != "web_search_preview" {
			continue
		}
		opts := map[string]interface{}{}
		if ws.SearchContextSize != "" {
			opts["search_context_size"] = ws.SearchContextSize
		} else {
			opts["search_context_size"] = "medium"
		}
		if len(ws.UserLocation) > 0 && string(ws.UserLocation) != "null" {
			opts["user_location"] = ws.UserLocation
		}
		b, _ := json.Marshal(opts)
		return json.RawMessage(b)
	}
	return nil
}

// filterOpenAITools 过滤掉 function.name 为空的 OpenAI 格式工具
func filterOpenAITools(items []json.RawMessage) json.RawMessage {
	var filtered []json.RawMessage
	for _, item := range items {
		var t struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(item, &t) == nil && t.Function.Name == "" {
			slog.Warn("skipping_tool_with_empty_name", "index", len(filtered))
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return nil
	}
	out, _ := json.Marshal(filtered)
	return out
}

func normalizeRole(role string) string {
	switch role {
	case "developer":
		return "system"
	default:
		return role
	}
}

func toUnifiedResponse(oaiResp *chatCompletionResponse) *models.UnifiedResponse {
	resp := &models.UnifiedResponse{
		ID:    oaiResp.ID,
		Model: oaiResp.Model,
		Usage: models.UnifiedUsage{InputTokens: oaiResp.Usage.PromptTokens, OutputTokens: oaiResp.Usage.CompletionTokens},
	}
	if len(oaiResp.Choices) > 0 {
		c := oaiResp.Choices[0]
		resp.Role = c.Message.Role
		resp.Content = c.Message.Content
		resp.StopReason = c.FinishReason

		if len(c.Message.ToolCalls) > 0 {
			var tcs []toolCallObj
			if err := json.Unmarshal(c.Message.ToolCalls, &tcs); err == nil {
				for _, tc := range tcs {
					resp.ToolCalls = append(resp.ToolCalls, models.ToolCall{
						ID:   tc.ID,
						Type: tc.Type,
						Function: models.ToolCallFunction{
							Name:      tc.Function.Name,
							Arguments: tc.Function.Arguments,
						},
					})
				}
			}
		}
	}
	return resp
}
