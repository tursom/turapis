package email

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// 编译时接口检查。
var _ EmailProvider = (*Mailondeck)(nil)

// Mailondeck 实现 mailondeck.com 临时邮件服务的 EmailProvider。
// Mailondeck 使用返回 HTML 而非 JSON 的 AJAX 端点，并且需要
// session cookie 来管理状态。
//
// 端点：
//   - GET  /ajax/ce-new-email.php        → "email@domain.com|token123"
//   - POST /ajax/messages.php             → "0" 或 "N|...html..."
//   - GET  /email_iframe.php?msg_id=X     → 包含 #inbox_message 的 HTML 页面
type Mailondeck struct {
	baseURL string
	client  *http.Client
}

// NewMailondeck 使用给定的配置创建一个新的 Mailondeck 提供商。
// 自动配置 cookie jar 以在 AJAX 调用之间保持会话。
// 如果 config.ProxyURL 已设置，HTTP 客户端将通过代理路由流量。
func NewMailondeck(cfg EmailProviderConfig) *Mailondeck {
	jar, _ := cookiejar.New(nil)
	return &Mailondeck{
		baseURL: "https://www.emailondeck.com",
		client: &http.Client{
			Jar:       jar,
			Transport: buildTransport(cfg.ProxyURL),
			Timeout:   30 * time.Second,
		},
	}
}

// Name 返回提供商名称。
func (p *Mailondeck) Name() string { return "mailondeck" }

// SupportsReuse 返回 true — mailondeck 允许复用邮箱。
func (p *Mailondeck) SupportsReuse() bool { return true }

// seedSession 通过加载主页建立会话，
// 以便后续的 AJAX 调用拥有所需的 cookie。
func (p *Mailondeck) seedSession(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/", nil)
	if err != nil {
		return fmt.Errorf("mailondeck seed session request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; CodexBot/1.0)")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("mailondeck seed session: %w", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return nil
}

// CreateInbox 通过调用 AJAX new-email 端点创建一个新的临时邮箱。
func (p *Mailondeck) CreateInbox(ctx context.Context) (*InboxInfo, error) {
	if err := p.seedSession(ctx); err != nil {
		return nil, fmt.Errorf("mailondeck create inbox: %w", err)
	}

	body, err := p.doGet(ctx, "/ajax/ce-new-email.php")
	if err != nil {
		return nil, fmt.Errorf("mailondeck create inbox: %w", err)
	}

	parts := strings.Split(strings.TrimSpace(string(body)), "|")
	if len(parts) < 2 {
		return nil, fmt.Errorf("mailondeck unexpected create response: %s", string(body))
	}

	info := &InboxInfo{
		Address:  parts[0],
		Token:    parts[1],
		Provider: p.Name(),
	}

	if at := strings.Index(parts[0], "@"); at > 0 {
		info.Alias = parts[0][:at]
		info.Domain = parts[0][at+1:]
	}

	return info, nil
}

// CreateInboxWithAlias 通过先创建一个标准邮箱，
// 然后向 change-email 表单发送 POST 请求来设置别名。
func (p *Mailondeck) CreateInboxWithAlias(ctx context.Context, alias, domain string) (*InboxInfo, error) {
	info, err := p.CreateInbox(ctx)
	if err != nil {
		return nil, err
	}

	formData := url.Values{}
	formData.Set("action", "change")
	if domain != "" {
		formData.Set("domain", domain)
	}
	if alias != "" {
		formData.Set("alias", alias)
	}

	reqBody := strings.NewReader(formData.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/ajax/ce-change-email.php", reqBody)
	if err != nil {
		return nil, fmt.Errorf("mailondeck change email request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mailondeck change email: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return nil, fmt.Errorf("mailondeck read change email response: %w", err)
	}

	trimmed := strings.TrimSpace(string(respBody))
	if trimmed == "" {
		return nil, fmt.Errorf("mailondeck change email returned empty response")
	}

	parts := strings.Split(trimmed, "|")
	if len(parts) >= 2 {
		info.Address = parts[0]
		info.Token = parts[1]
	}

	if at := strings.Index(info.Address, "@"); at > 0 {
		info.Alias = info.Address[:at]
		info.Domain = info.Address[at+1:]
	}

	return info, nil
}

// GetMessages 通过解析 AJAX HTML 响应获取邮箱中的消息。
func (p *Mailondeck) GetMessages(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
	body, err := p.doPost(ctx, "/ajax/messages.php", nil)
	if err != nil {
		return nil, fmt.Errorf("mailondeck get messages: %w", err)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "0" || trimmed == "" {
		return nil, nil
	}

	return parseMailondeckMessages(trimmed)
}

// GetMessage 通过调用 iframe 端点获取单条消息。
func (p *Mailondeck) GetMessage(ctx context.Context, inbox *InboxInfo, messageID string) (*EmailMessage, error) {
	path := fmt.Sprintf("/email_iframe.php?msg_id=%s", messageID)
	body, err := p.doGet(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("mailondeck get message %s: %w", messageID, err)
	}

	msg := parseMailondeckMessage(string(body))
	if msg == nil {
		return nil, fmt.Errorf("mailondeck: could not parse message %s", messageID)
	}
	msg.ID = messageID
	return msg, nil
}

// WaitForEmail 使用通用轮询辅助函数。
func (p *Mailondeck) WaitForEmail(ctx context.Context, inbox *InboxInfo, timeout time.Duration, predicate func(*EmailMessage) bool) (*EmailMessage, error) {
	return WaitForEmailPolling(ctx, p, inbox, timeout, DefaultPollInterval, predicate)
}

// doGet 执行 GET 请求并返回响应体。
func (p *Mailondeck) doGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("mailondeck GET %s: %w", path, err)
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mailondeck GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("mailondeck read GET %s: %w", path, err)
	}
	return body, nil
}

// doPost 执行 POST 请求并返回响应体。
func (p *Mailondeck) doPost(ctx context.Context, path string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("mailondeck POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mailondeck POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("mailondeck read POST %s: %w", path, err)
	}
	return respBody, nil
}

// msgRowRE 匹配 div.inbox_rows 元素并提取 id、from、subject、time。
// 格式：<div class="inbox_rows msglink" data-msgid="123"...>
var msgRowRE = regexp.MustCompile(`<div\s+[^>]*class="[^"]*inbox_rows[^"]*"[^>]*data-msgid="([^"]*)"[^>]*>`)

// parseMailondeckMessages 解析 mailondeck AJAX 消息响应。
// 格式为："N|...html..."，其中 HTML 包含 div.inbox_rows 元素。
// 空邮箱返回 "0"。
func parseMailondeckMessages(raw string) ([]EmailMessage, error) {
	_, htmlContent, found := strings.Cut(raw, "|")
	if !found {
		if raw == "0" {
			return nil, nil
		}
		return nil, fmt.Errorf("mailondeck: unexpected messages format: %s", raw)
	}
	matches := msgRowRE.FindAllStringSubmatch(htmlContent, -1)
	if len(matches) == 0 {
		return nil, nil
	}

	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("mailondeck parse messages HTML: %w", err)
	}

	var msgs []EmailMessage
	rows := findInboxRows(doc)
	for _, row := range rows {
		msg := extractRowData(row)
		if msg != nil && msg.ID != "" {
			msgs = append(msgs, *msg)
		}
	}

	_ = matches
	return msgs, nil
}

// findInboxRows 遍历 HTML 树，查找 class 为 "inbox_rows" 的 div 元素。
func findInboxRows(n *html.Node) []*html.Node {
	var rows []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "div" {
			for _, attr := range node.Attr {
				if attr.Key == "class" && strings.Contains(attr.Val, "inbox_rows") {
					rows = append(rows, node)
					break
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return rows
}

// extractRowData 从 inbox_rows div 节点中提取 EmailMessage 字段。
func extractRowData(row *html.Node) *EmailMessage {
	msg := &EmailMessage{}

	for _, attr := range row.Attr {
		switch attr.Key {
		case "data-msgid":
			msg.ID = attr.Val
		case "data-from":
			msg.From = attr.Val
		case "data-subject":
			msg.Subject = attr.Val
		case "data-date":
			msg.Date = attr.Val
		}
	}

	return msg
}

// parseMailondeckMessage 从 iframe HTML 中提取邮件内容。
// 查找 <div id="inbox_message"> 并提取其内部内容。
func parseMailondeckMessage(raw string) *EmailMessage {
	doc, err := html.Parse(strings.NewReader(raw))
	if err != nil {
		return nil
	}

	msgDiv := findInboxMessage(doc)
	if msgDiv == nil {
		return nil
	}

	body := renderInnerHTML(msgDiv)
	msg := &EmailMessage{
		Body: body,
		HTML: body,
	}

	subj := findElementByClass(doc, "inbox_subject")
	if subj != nil {
		msg.Subject = renderInnerHTML(subj)
	}

	from := findElementByClass(doc, "inbox_from")
	if from != nil {
		msg.From = strings.TrimSpace(renderInnerHTML(from))
	}

	return msg
}

// findInboxMessage 在 HTML 树中查找 #inbox_message div。
func findInboxMessage(n *html.Node) *html.Node {
	var walk func(*html.Node) *html.Node
	walk = func(node *html.Node) *html.Node {
		if node.Type == html.ElementNode && node.Data == "div" {
			for _, attr := range node.Attr {
				if attr.Key == "id" && attr.Val == "inbox_message" {
					return node
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if found := walk(c); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(n)
}

// findElementByClass 查找具有指定 class 的单个元素。
func findElementByClass(n *html.Node, class string) *html.Node {
	var walk func(*html.Node) *html.Node
	walk = func(node *html.Node) *html.Node {
		if node.Type == html.ElementNode {
			for _, attr := range node.Attr {
				if attr.Key == "class" && strings.Contains(attr.Val, class) {
					return node
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			if found := walk(c); found != nil {
				return found
			}
		}
		return nil
	}
	return walk(n)
}

// renderInnerHTML 将所有子节点序列化为 HTML 文本。
func renderInnerHTML(n *html.Node) string {
	var buf strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		html.Render(&buf, c)
	}
	return buf.String()
}
