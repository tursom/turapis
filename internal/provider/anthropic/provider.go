package anthropic

import (
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
	"github.com/tursom/turapis/internal/translate"
)

// AnthropicProvider 实现 Anthropic 协议的 Provider
type AnthropicProvider struct {
	id             int
	name           string
	url            string
	apiKey         string
	client         *http.Client
	supportedTools map[string]bool
}

func New(id int, name, baseURL, apiKey string, supportedTools []string, proxyURL string) *AnthropicProvider {
	st := make(map[string]bool, len(supportedTools))
	for _, t := range supportedTools {
		st[t] = true
	}
	transport := provider.SharedTransport()
	if proxyURL != "" {
		transport = provider.NewTransportWithProxy(proxyURL)
	}
	return &AnthropicProvider{
		id:             id,
		name:           name,
		url:            strings.TrimSuffix(baseURL, "/"),
		apiKey:         apiKey,
		client: &http.Client{
			Transport: transport,
			Timeout:   300 * time.Second,
		},
		supportedTools: st,
	}
}

func (p *AnthropicProvider) SupportsTool(name string) bool {
	if p.supportedTools == nil {
		return true
	}
	return p.supportedTools[name]
}

func (p *AnthropicProvider) Name() string                 { return p.name }
func (p *AnthropicProvider) ID() int                    { return p.id }
func (p *AnthropicProvider) Protocol() models.ProtocolType { return models.ProtocolAnthropic }
func (p *AnthropicProvider) SetAPIKey(key string)       { p.apiKey = key }

// ChatCompletion 发送非流式请求
func (p *AnthropicProvider) ChatCompletion(ctx context.Context, req *models.UnifiedRequest) (*models.UnifiedResponse, error) {
	body := toAnthropicRequest(req)

	resp, err := p.doRequest(ctx, "/messages", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		return nil, &models.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	var anthropicResp translate.AnthropicResp
	if err := json.NewDecoder(resp.Body).Decode(&anthropicResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return toUnifiedResponse(&anthropicResp), nil
}

// ChatCompletionStream 发送流式请求
func (p *AnthropicProvider) ChatCompletionStream(ctx context.Context, req *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error) {
	body := toAnthropicRequest(req)
	body.Stream = true

	resp, err := p.doRequest(ctx, "/messages", body)
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

		buf := make([]byte, 1<<20)
		var eventType string
		var dataLines [][]byte

		reader := resp.Body
		remainder := make([]byte, 0)

		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			n, readErr := reader.Read(buf)
			if n > 0 {
				lines := bytes.Split(append(remainder, buf[:n]...), []byte("\n"))

				// 最后一行可能不完整，保留到下次
				if readErr != io.EOF && len(lines) > 0 && len(lines[len(lines)-1]) > 0 {
					remainder = lines[len(lines)-1]
					lines = lines[:len(lines)-1]
				} else {
					remainder = nil
				}

				for _, line := range lines {
					text := string(line)

					if text == "" {
						if len(dataLines) > 0 {
							data := bytes.Join(dataLines, []byte("\n"))
							evs, err := translate.AnthropicStreamEventToUnified(eventType, data)
							if err != nil {
								select {
								case ch <- models.UnifiedStreamEvent{Type: models.StreamEventError, Error: err}:
								case <-ctx.Done():
								}
								return
							}
							for _, ev := range evs {
								if ev != nil {
									select {
									case ch <- *ev:
									case <-ctx.Done():
										return
									}
								}
							}
						}
						eventType = ""
						dataLines = nil
						continue
					}

					if strings.HasPrefix(text, "event: ") {
						eventType = strings.TrimPrefix(text, "event: ")
					} else if strings.HasPrefix(text, "data: ") {
						dataLines = append(dataLines, []byte(strings.TrimPrefix(text, "data: ")))
					}
				}
			}

			if readErr != nil {
				break
			}
		}

		// 发送结束事件
		select {
		case ch <- models.UnifiedStreamEvent{Type: models.StreamEventStop}:
		case <-ctx.Done():
		}
	}()

	return ch, nil
}

// ListModels 列出可用模型
func (p *AnthropicProvider) ListModels(ctx context.Context) ([]models.ModelInfo, error) {
	resp, err := p.doGet(ctx, "/models")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
		return nil, &models.UpstreamError{StatusCode: resp.StatusCode, Body: respBody}
	}

	var result struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}

	infos := make([]models.ModelInfo, len(result.Data))
	for i, m := range result.Data {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		infos[i] = models.ModelInfo{ID: m.ID, Name: name, Provider: p.name}
	}
	return infos, nil
}

func (p *AnthropicProvider) doRequest(ctx context.Context, path string, body interface{}) (*http.Response, error) {
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
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post %s: ctx_err=%v err=%w", fullURL, ctx.Err(), err)
	}
	return resp, nil
}

func (p *AnthropicProvider) doGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.url+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	return p.client.Do(req)
}

// normalizeToolsToAnthropic 将 tools 转换为 Anthropic 格式
// 支持三种输入格式:
//   - Anthropic:              {"name":"...","description":"...","input_schema":{...}}
//   - OpenAI Chat Completions: {"type":"function","function":{"name":"...","parameters":{...}}}
//   - OpenAI Responses API:   {"type":"function","name":"...","description":"...","parameters":{...}}
func normalizeToolsToAnthropic(tools json.RawMessage) json.RawMessage {
	if len(tools) == 0 {
		return tools
	}
	var items []json.RawMessage
	if err := json.Unmarshal(tools, &items); err != nil || len(items) == 0 {
		return tools
	}
	// 检查是否是 OpenAI Chat Completions 格式：有 function 字段
	var check struct {
		Function json.RawMessage `json:"function"`
	}
	if json.Unmarshal(items[0], &check) == nil && check.Function != nil {
		// OpenAI Chat Completions → Anthropic 转换
		var result []json.RawMessage
		for _, item := range items {
			var oai struct {
				Function struct {
					Name        string          `json:"name"`
					Description string          `json:"description"`
					Parameters  json.RawMessage `json:"parameters"`
				} `json:"function"`
			}
			if err := json.Unmarshal(item, &oai); err != nil {
				return tools
			}
			if oai.Function.Name == "" {
				slog.Warn("skipping_tool_with_empty_name", "index", len(result))
				continue
			}
			anth := map[string]interface{}{
				"name":         oai.Function.Name,
				"description":  oai.Function.Description,
				"input_schema": oai.Function.Parameters,
			}
			b, _ := json.Marshal(anth)
			result = append(result, b)
		}
		if len(result) == 0 {
			return nil
		}
		out, _ := json.Marshal(result)
		return out
	}
	// 检查是否是 Responses API 格式: type="function", 有 name 和 parameters，无 function 包裹
	var responsesCheck struct {
		Type       string          `json:"type"`
		Name       string          `json:"name"`
		Parameters json.RawMessage `json:"parameters"`
	}
	if json.Unmarshal(items[0], &responsesCheck) == nil && (responsesCheck.Type == "function" || responsesCheck.Type == "namespace") {
		// Responses API → Anthropic 转换（parameters → input_schema）
		return convertResponsesToAnthropic(items)
	}
	// 已是 Anthropic 格式，过滤空 name 的工具
	return filterAnthropicTools(items)
}

// convertResponsesToAnthropic 将 Responses API 格式转换为 Anthropic 格式
func convertResponsesToAnthropic(items []json.RawMessage) json.RawMessage {
	flat := flattenNamespaceContainers(items)
	var result []json.RawMessage
	for _, item := range flat {
		var rt struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if err := json.Unmarshal(item, &rt); err != nil {
			return nil
		}
		if rt.Type != "function" || rt.Name == "" {
			allFields := collectToolFields(item)
			if rt.Type == "web_search" || rt.Type == "web_search_preview" {
				slog.Info("skipping_web_search_tool_not_supported_by_anthropic",
					"index", len(result), "fields", allFields,
				)
			} else {
				slog.Warn("skipping_non_function_tool_in_responses_format",
					"type", rt.Type, "name", rt.Name, "index", len(result),
					"fields", allFields, "raw", string(item),
				)
			}
			continue
		}
		anth := map[string]interface{}{
			"name":         rt.Name,
			"description":  rt.Description,
			"input_schema": rt.Parameters,
		}
		b, _ := json.Marshal(anth)
		result = append(result, b)
	}
	if len(result) == 0 {
		return nil
	}
	out, _ := json.Marshal(result)
	return out
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

// filterAnthropicTools 过滤掉 name 为空的 Anthropic 格式工具
func filterAnthropicTools(items []json.RawMessage) json.RawMessage {
	var filtered []json.RawMessage
	for _, item := range items {
		var t struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(item, &t) == nil && t.Name == "" {
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

func toAnthropicRequest(req *models.UnifiedRequest) *translate.AnthropicReq {
	systemRaw, _ := json.Marshal(req.System)
	aReq := &translate.AnthropicReq{
		Model:       req.Model,
		System:      systemRaw,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeq:     req.Stop,
		Stream:      req.Stream,
		Tools:       normalizeToolsToAnthropic(req.Tools),
		ToolChoice:  req.ToolChoice,
	}
	if aReq.MaxTokens == 0 {
		aReq.MaxTokens = 1024
	}

	for _, msg := range req.Messages {
		aMsg := translate.AnthropicMsg{Role: msg.Role}

		if len(msg.ContentBlocks) > 0 {
			for _, block := range msg.ContentBlocks {
				ac := translate.AnthropicContent{
					Type: block.Type,
					Text: block.Text,
				}
				switch block.Type {
				case "text":
					ac.Text = block.Text
				case "tool_use":
					ac.ID = block.ID
					ac.Name = block.Name
					ac.Input = block.Input
				case "tool_result":
					ac.ToolUseID = block.ToolUseID
					ac.Content = block.Content
					ac.IsError = block.IsError
				}
				aMsg.Content = append(aMsg.Content, ac)
			}
		} else {
			aMsg.Content = []translate.AnthropicContent{
				{Type: "text", Text: msg.Content},
			}
		}
		aReq.Messages = append(aReq.Messages, aMsg)
	}
	return aReq
}

func toUnifiedResponse(anthropicResp *translate.AnthropicResp) *models.UnifiedResponse {
	resp := &models.UnifiedResponse{
		ID:         anthropicResp.ID,
		Model:      anthropicResp.Model,
		Role:       anthropicResp.Role,
		StopReason: anthropicResp.StopReason,
		Usage: models.UnifiedUsage{
			InputTokens:  anthropicResp.Usage.InputTokens,
			OutputTokens: anthropicResp.Usage.OutputTokens,
		},
	}

	for _, block := range anthropicResp.Content {
		switch block.Type {
		case "text":
			resp.Content = block.Text
		case "tool_use":
			args := ""
			if len(block.Input) > 0 {
				args = string(block.Input)
			}
			resp.ToolCalls = append(resp.ToolCalls, models.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: models.ToolCallFunction{
					Name:      block.Name,
					Arguments: args,
				},
			})
		}
	}
	return resp
}
