package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
	"github.com/tursom/turapis/internal/translate"
)

// AnthropicProvider 实现 Anthropic 协议的 Provider
type AnthropicProvider struct {
	name   string
	url    string
	apiKey string
	client *http.Client
}

// New 创建 Anthropic 协议 Provider
func New(name, baseURL, apiKey string) *AnthropicProvider {
	return &AnthropicProvider{
		name:   name,
		url:    strings.TrimSuffix(baseURL, "/"),
		apiKey: apiKey,
		client: &http.Client{
			Transport: provider.SharedTransport(),
			Timeout:   60 * time.Second,
		},
	}
}

func (p *AnthropicProvider) Name() string                 { return p.name }
func (p *AnthropicProvider) Protocol() models.ProtocolType { return models.ProtocolAnthropic }

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
							ev, err := translate.AnthropicStreamEventToUnified(eventType, data)
							if err != nil {
								select {
								case ch <- models.UnifiedStreamEvent{Type: models.StreamEventError, Error: err}:
								case <-ctx.Done():
								}
								return
							}
							if ev != nil {
								select {
								case ch <- *ev:
								case <-ctx.Done():
									return
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

	req, err := http.NewRequestWithContext(ctx, "POST", p.url+path, bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	return p.client.Do(req)
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

func toAnthropicRequest(req *models.UnifiedRequest) *translate.AnthropicReq {
	aReq := &translate.AnthropicReq{
		Model:       req.Model,
		System:      req.System,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		StopSeq:     req.Stop,
		Stream:      req.Stream,
	}
	if aReq.MaxTokens == 0 {
		aReq.MaxTokens = 1024
	}

	for _, msg := range req.Messages {
		aMsg := translate.AnthropicMsg{
			Role: msg.Role,
			Content: []translate.AnthropicContent{
				{Type: "text", Text: msg.Content},
			},
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

	// 提取第一个 text block 的内容
	for _, block := range anthropicResp.Content {
		if block.Type == "text" {
			resp.Content = block.Text
			break
		}
	}
	return resp
}
