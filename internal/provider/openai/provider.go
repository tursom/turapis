package openai

import (
	"bufio"
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
)

// OpenAIProvider 实现 OpenAI 协议的 Provider
type OpenAIProvider struct {
	name   string
	url    string
	apiKey string
	client *http.Client
}

// New 创建 OpenAI 协议 Provider
func New(name, baseURL, apiKey string) *OpenAIProvider {
	return &OpenAIProvider{
		name:   name,
		url:    strings.TrimSuffix(baseURL, "/"),
		apiKey: apiKey,
		client: &http.Client{
			Transport: provider.SharedTransport(),
			Timeout:   60 * time.Second,
		},
	}
}

func (p *OpenAIProvider) Name() string                  { return p.name }
func (p *OpenAIProvider) Protocol() models.ProtocolType  { return models.ProtocolOpenAI }

// ChatCompletion 发送非流式请求
func (p *OpenAIProvider) ChatCompletion(ctx context.Context, req *models.UnifiedRequest) (*models.UnifiedResponse, error) {
	body := chatCompletionRequest{
		Model:       req.Model,
		Messages:    toOpenAIMessages(req),
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      false,
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
		scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB buffer

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
				continue // skip bad chunks
			}

			if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
				event := models.UnifiedStreamEvent{
					Type:    models.StreamEventDelta,
					Content: chunk.Choices[0].Delta.Content,
				}
				if chunk.Choices[0].FinishReason != "" {
					event.StopReason = chunk.Choices[0].FinishReason
				}
				select {
				case ch <- event:
				case <-ctx.Done():
					return
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

		// 发送结束事件（如果正常读完流）
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
	Model         string         `json:"model"`
	Messages      []openaiMsg    `json:"messages"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	TopP          *float64       `json:"top_p,omitempty"`
	Stop          []string       `json:"stop,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openaiMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
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
			Content string `json:"content"`
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

func toOpenAIMessages(req *models.UnifiedRequest) []openaiMsg {
	msgs := make([]openaiMsg, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openaiMsg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, openaiMsg{Role: m.Role, Content: m.Content})
	}
	return msgs
}

func toUnifiedResponse(oaiResp *chatCompletionResponse) *models.UnifiedResponse {
	resp := &models.UnifiedResponse{
		ID:      oaiResp.ID,
		Model:   oaiResp.Model,
		Usage:   models.UnifiedUsage{InputTokens: oaiResp.Usage.PromptTokens, OutputTokens: oaiResp.Usage.CompletionTokens},
	}
	if len(oaiResp.Choices) > 0 {
		resp.Role = oaiResp.Choices[0].Message.Role
		resp.Content = oaiResp.Choices[0].Message.Content
		resp.StopReason = oaiResp.Choices[0].FinishReason
	}
	return resp
}
