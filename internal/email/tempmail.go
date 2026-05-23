package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// 编译时接口检查。
var _ EmailProvider = (*TempmailLOL)(nil)

// TempmailLOL 实现 tempmail.lol API 的 EmailProvider。
//
// API 文档：POST /v2/inbox/create 创建邮箱，
// GET /v2/inbox?token=X 获取消息。
type TempmailLOL struct {
	baseURL string
	client  *http.Client
	apiKey  string
	domain  string
}

func NewTempmailLOL(cfg EmailProviderConfig) *TempmailLOL {
	return &TempmailLOL{
		baseURL: "https://api.tempmail.lol",
		client: &http.Client{
			Transport: buildTransport(cfg.ProxyURL),
			Timeout:   30 * time.Second,
		},
		apiKey: cfg.APIKey,
		domain: cfg.Domain,
	}
}

// Name 返回提供商名称。
func (p *TempmailLOL) Name() string { return "tempmail_lol" }

// SupportsReuse 返回 false — tempmail.lol 不支持复用邮箱。
func (p *TempmailLOL) SupportsReuse() bool { return false }

func (p *TempmailLOL) setAuth(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

// CreateInbox 通过 POST /v2/inbox/create 创建一个新的临时邮箱。
func (p *TempmailLOL) CreateInbox(ctx context.Context) (*InboxInfo, error) {
	var body io.Reader
	if p.domain != "" {
		payload, _ := json.Marshal(map[string]string{"domain": p.domain})
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v2/inbox/create", body)
	if err != nil {
		return nil, fmt.Errorf("tempmail.lol create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempmail.lol create inbox: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tempmail.lol create inbox returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Address string `json:"address"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("tempmail.lol decode create response: %w", err)
	}

	if result.Address == "" || result.Token == "" {
		return nil, fmt.Errorf("tempmail.lol returned empty address or token")
	}

	return &InboxInfo{
		Address:  result.Address,
		Token:    result.Token,
		Provider: p.Name(),
	}, nil
}

// CreateInboxWithAlias 在 tempmail.lol 免费版上不支持。
func (p *TempmailLOL) CreateInboxWithAlias(ctx context.Context, alias, domain string) (*InboxInfo, error) {
	return nil, fmt.Errorf("tempmail.lol: CreateInboxWithAlias not supported on free tier")
}

// GetMessages 通过 GET /v2/inbox?token=X 获取邮箱中的所有消息。
func (p *TempmailLOL) GetMessages(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
	url := fmt.Sprintf("%s/v2/inbox?token=%s", p.baseURL, inbox.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("tempmail.lol get messages request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempmail.lol get messages: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tempmail.lol get messages returned status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Emails  []tempmailEmail `json:"emails"`
		Expired bool            `json:"expired"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("tempmail.lol decode messages: %w", err)
	}

	msgs := make([]EmailMessage, len(result.Emails))
	for i, e := range result.Emails {
		msgs[i] = EmailMessage{
			ID:      fmt.Sprintf("%d", e.ID),
			From:    e.From,
			To:      e.To,
			Subject: e.Subject,
			Body:    e.Body,
			HTML:    e.HTML,
			Date:    fmt.Sprintf("%d", e.Date),
		}
	}
	return msgs, nil
}

// GetMessage 返回单条消息。对于 tempmail.lol，消息已经通过
// GetMessages 完整获取，因此这里只是遍历邮箱。
func (p *TempmailLOL) GetMessage(ctx context.Context, inbox *InboxInfo, messageID string) (*EmailMessage, error) {
	msgs, err := p.GetMessages(ctx, inbox)
	if err != nil {
		return nil, err
	}
	for i := range msgs {
		if msgs[i].ID == messageID {
			return &msgs[i], nil
		}
	}
	return nil, fmt.Errorf("tempmail.lol: message %s not found", messageID)
}

// WaitForEmail 使用通用轮询辅助函数等待邮件。
func (p *TempmailLOL) WaitForEmail(ctx context.Context, inbox *InboxInfo, timeout time.Duration, predicate func(*EmailMessage) bool) (*EmailMessage, error) {
	return WaitForEmailPolling(ctx, p, inbox, timeout, DefaultPollInterval, predicate)
}

// tempmailEmail 是 tempmail.lol API 返回的原始消息结构体。
type tempmailEmail struct {
	ID      int64  `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	HTML    string `json:"html"`
	Date    int64  `json:"date"`
}

// CreateInboxWithPrefix 创建一个带有指定前缀的本地部分的新邮箱。
// 它使用了 tempmail.lol 支持的可选 JSON body 字段。
func (p *TempmailLOL) CreateInboxWithPrefix(ctx context.Context, prefix string) (*InboxInfo, error) {
	var body io.Reader
	if prefix != "" {
		payload, err := json.Marshal(map[string]string{"prefix": prefix})
		if err != nil {
			return nil, fmt.Errorf("tempmail.lol marshal prefix: %w", err)
		}
		body = bytes.NewReader(payload)
	}

	url := p.baseURL + "/v2/inbox/create"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return nil, fmt.Errorf("tempmail.lol create with prefix request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	p.setAuth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tempmail.lol create inbox with prefix: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tempmail.lol create inbox with prefix returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Address string `json:"address"`
		Token   string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("tempmail.lol decode prefix response: %w", err)
	}

	if result.Address == "" || result.Token == "" {
		return nil, fmt.Errorf("tempmail.lol returned empty address or token")
	}

	return &InboxInfo{
		Address:  result.Address,
		Token:    result.Token,
		Provider: p.Name(),
	}, nil
}
