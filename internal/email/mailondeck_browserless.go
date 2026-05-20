package email

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/browser"
)

// 编译时接口检查。
var _ EmailProvider = (*MailondeckBrowserless)(nil)

// MailondeckBrowserless 使用远程 browserless/chromium 实例
// 实现 mailondeck.com 的 EmailProvider。它通过 CDP 驱动
// mailondeck.com Web UI 来创建邮箱、读取消息以及更改邮件别名。
//
// 与基于 HTTP 调用 AJAX 端点的 Mailondeck 提供商不同，
// 此实现直接与 DOM 交互，在提取数据之前等待
// 站点的 JavaScript 初始化完成。
type MailondeckBrowserless struct {
	bc      *browser.BrowserlessClient
	timeout time.Duration
}

// NewMailondeckBrowserless 创建一个由给定 BrowserlessClient
// 支持的 MailondeckBrowserless。每个方法都会启动自己的临时
// 浏览器上下文（标签页），并在返回时清理。
func NewMailondeckBrowserless(bc *browser.BrowserlessClient) *MailondeckBrowserless {
	return &MailondeckBrowserless{bc: bc, timeout: 120 * time.Second}
}

// Name 返回提供商名称。
func (m *MailondeckBrowserless) Name() string { return "mailondeck_browserless" }

// SupportsReuse 返回 true — mailondeck 允许复用邮箱。
func (m *MailondeckBrowserless) SupportsReuse() bool { return true }

// CreateInbox 在一个新的浏览器标签页中打开 mailondeck.com，
// 等待站点的 JavaScript 填充邮件地址输入框。然后提取
// 地址和隐藏的 session token。
func (m *MailondeckBrowserless) CreateInbox(ctx context.Context) (*InboxInfo, error) {
	bctx, cancel := m.bc.NewContext(ctx)
	defer cancel()

	if err := m.bc.Navigate(bctx, "https://www.mailondeck.com"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless navigate: %w", err)
	}

	if err := m.bc.WaitForSelector(bctx, "#mainEmail"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless wait for #mainEmail: %w", err)
	}

	// 邮件输入框初始设置为 "Landing"，由站点 JS 更新。
	// 轮询直到出现真实的电子邮件地址。
	var address string
	for range 30 {
		var err error
		address, err = m.bc.EvalJS(bctx, `document.querySelector('#mainEmail').value`)
		if err != nil {
			return nil, fmt.Errorf("mailondeck_browserless read email: %w", err)
		}
		address = strings.TrimSpace(address)
		if address != "" && !strings.HasPrefix(address, "Landing") && strings.Contains(address, "@") {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	if address == "" || strings.HasPrefix(address, "Landing") || !strings.Contains(address, "@") {
		return nil, fmt.Errorf("mailondeck_browserless: timed out waiting for email address (got %q)", address)
	}

	// 提取隐藏的 session token。
	token, err := m.bc.EvalJS(bctx, `document.querySelector('#email_token').value`)
	if err != nil {
		return nil, fmt.Errorf("mailondeck_browserless read token: %w", err)
	}
	token = strings.TrimSpace(token)

	info := &InboxInfo{
		Address:  address,
		Token:    token,
		Provider: m.Name(),
	}

	if at := strings.Index(address, "@"); at > 0 {
		info.Alias = address[:at]
		info.Domain = address[at+1:]
	}

	return info, nil
}

// GetMessages 打开 mailondeck.com，等待收件箱行渲染，
// 并通过 JavaScript 从 DOM 中提取消息列表。
func (m *MailondeckBrowserless) GetMessages(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error) {
	bctx, cancel := m.bc.NewContext(ctx)
	defer cancel()

	if err := m.bc.Navigate(bctx, "https://www.mailondeck.com"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless navigate: %w", err)
	}

	// 等待收件箱容器或空状态。
	if err := m.bc.WaitForSelector(bctx, "#mailbox"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless wait for #mailbox: %w", err)
	}

	// 动态内容需要短暂的稳定时间。
	time.Sleep(2 * time.Second)

	// 通过 JavaScript 提取消息行。每行是一个 div.inbox_rows，
	// 包含 data-msgid，以及带有 from/subject/time 类名的子元素。
	script := `JSON.stringify(Array.from(document.querySelectorAll('.inbox_rows')).map(row => ({
		id: row.getAttribute('data-msgid') || '',
		from: (row.querySelector('.inbox_td_from') || {}).textContent || '',
		subject: (row.querySelector('.inbox_td_subject') || {}).textContent || '',
		time: (row.querySelector('.inbox_td_received') || {}).textContent || ''
	})))`

	raw, err := m.bc.EvalJS(bctx, script)
	if err != nil {
		return nil, fmt.Errorf("mailondeck_browserless eval messages: %w", err)
	}

	var rows []struct {
		ID      string `json:"id"`
		From    string `json:"from"`
		Subject string `json:"subject"`
		Time    string `json:"time"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless parse messages JSON: %w", err)
	}

	msgs := make([]EmailMessage, 0, len(rows))
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		msgs = append(msgs, EmailMessage{
			ID:      r.ID,
			From:    strings.TrimSpace(r.From),
			Subject: strings.TrimSpace(r.Subject),
			Date:    strings.TrimSpace(r.Time),
			To:      inbox.Address,
		})
	}

	return msgs, nil
}

// GetMessage 导航到 mailondeck.com，点击目标消息行，
// 等待嵌入式 iframe 加载，然后提取完整的消息正文。
func (m *MailondeckBrowserless) GetMessage(ctx context.Context, inbox *InboxInfo, messageID string) (*EmailMessage, error) {
	bctx, cancel := m.bc.NewContext(ctx)
	defer cancel()

	if err := m.bc.Navigate(bctx, "https://www.mailondeck.com"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless navigate: %w", err)
	}

	if err := m.bc.WaitForSelector(bctx, "#mailbox"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless wait for #mailbox: %w", err)
	}

	// 通过 data-msgid 点击消息行。
	clickScript := fmt.Sprintf(`(function() {
		var row = document.querySelector('.inbox_rows[data-msgid="%s"]');
		if (row) { row.click(); return 'clicked'; }
		return 'not found';
	})()`, messageID)

	result, err := m.bc.EvalJS(bctx, clickScript)
	if err != nil {
		return nil, fmt.Errorf("mailondeck_browserless click message %s: %w", messageID, err)
	}
	result = strings.TrimSpace(result)
	if result != "clicked" {
		return nil, fmt.Errorf("mailondeck_browserless: message %s %s", messageID, result)
	}

	// 等待消息 iframe 加载。
	time.Sleep(3 * time.Second)

	// 从 iframe 中提取消息正文。
	bodyScript := `(function() {
		var frame = document.querySelector('#myContent');
		if (!frame || !frame.contentDocument) return '';
		return frame.contentDocument.body ? frame.contentDocument.body.innerText : '';
	})()`

	body, err := m.bc.EvalJS(bctx, bodyScript)
	if err != nil {
		return nil, fmt.Errorf("mailondeck_browserless read message body: %w", err)
	}

	// 从页面标题区域提取主题和发件人。
	subject, _ := m.bc.EvalJS(bctx, `(function() {
		var el = document.querySelector('.inbox_subject');
		return el ? el.textContent.trim() : '';
	})()`)

	from, _ := m.bc.EvalJS(bctx, `(function() {
		var el = document.querySelector('.inbox_from');
		return el ? el.textContent.trim() : '';
	})()`)

	msg := &EmailMessage{
		ID:      messageID,
		From:    strings.TrimSpace(from),
		Subject: strings.TrimSpace(subject),
		Body:    strings.TrimSpace(body),
		To:      inbox.Address,
	}

	return msg, nil
}

// CreateInboxWithAlias 打开 mailondeck.com，点击"Change Email"按钮，
// 从历史记录列表中选择匹配的邮箱（如果有），
// 然后返回新的邮箱信息。
func (m *MailondeckBrowserless) CreateInboxWithAlias(ctx context.Context, alias, domain string) (*InboxInfo, error) {
	bctx, cancel := m.bc.NewContext(ctx)
	defer cancel()

	if err := m.bc.Navigate(bctx, "https://www.mailondeck.com"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless navigate: %w", err)
	}

	if err := m.bc.WaitForSelector(bctx, "#mainEmail"); err != nil {
		return nil, fmt.Errorf("mailondeck_browserless wait for #mainEmail: %w", err)
	}

	// 等待邮件完全初始化。
	var currentAddress string
	for range 30 {
		currentAddress, _ = m.bc.EvalJS(bctx, `document.querySelector('#mainEmail').value`)
		currentAddress = strings.TrimSpace(currentAddress)
		if currentAddress != "" && currentAddress != "Landing" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	// 点击修改邮件按钮。
	if err := m.bc.Click(bctx, `[aria-label="Change"]`); err != nil {
		// 回退：尝试基于 ID 的选择器。
		if err2 := m.bc.Click(bctx, "#change_email_btn"); err2 != nil {
			return nil, fmt.Errorf("mailondeck_browserless click change button: %w (fallback: %v)", err, err2)
		}
	}

	// 等待历史记录模态框/面板出现。
	time.Sleep(2 * time.Second)

	// 尝试选择与别名匹配的历史项目。
	selectScript := fmt.Sprintf(`(function() {
		var items = document.querySelectorAll('.history_choose_email');
		for (var i = 0; i < items.length; i++) {
			if (items[i].textContent.indexOf('%s') >= 0) {
				items[i].click();
				return 'selected ' + items[i].textContent.trim();
			}
		}
		return 'not found';
	})()`, alias)

	selResult, _ := m.bc.EvalJS(bctx, selectScript)
	selResult = strings.TrimSpace(selResult)

	if strings.Contains(selResult, "not found") {
		// 如果没有历史匹配项，尝试通过更改表单设置别名。
		// 点击别名输入框并输入所需的别名。
		if alias != "" {
			if err := m.bc.Click(bctx, "#alias"); err != nil {
				return nil, fmt.Errorf("mailondeck_browserless click alias input: %w", err)
			}
			time.Sleep(500 * time.Millisecond)

			// 清除现有值并输入新别名。
			if _, err := m.bc.EvalJS(bctx, `document.querySelector('#alias').value = ''`); err != nil {
				return nil, fmt.Errorf("mailondeck_browserless clear alias: %w", err)
			}
			if err := m.bc.SendKeys(bctx, "#alias", alias); err != nil {
				return nil, fmt.Errorf("mailondeck_browserless type alias: %w", err)
			}
		}

		// 提交更改。
		if err := m.bc.Click(bctx, `[type="submit"]`); err != nil {
			// 回退：尝试点击表单内的按钮。
			_, _ = m.bc.EvalJS(bctx, `document.querySelector('form input[type="submit"]')?.click()`)
		}
	}

	// 等待页面更新为新邮箱。
	time.Sleep(3 * time.Second)

	// 读取更新后的电子邮件地址。
	var newAddress string
	for range 15 {
		newAddress, _ = m.bc.EvalJS(bctx, `document.querySelector('#mainEmail').value`)
		newAddress = strings.TrimSpace(newAddress)
		if newAddress != "" && newAddress != "Landing" {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	if newAddress == "" || newAddress == "Landing" {
		return nil, fmt.Errorf("mailondeck_browserless: timed out waiting for new email address after alias change")
	}

	// 提取更新后的 token。
	token, _ := m.bc.EvalJS(bctx, `document.querySelector('#email_token').value`)
	token = strings.TrimSpace(token)

	info := &InboxInfo{
		Address:  newAddress,
		Token:    token,
		Provider: m.Name(),
	}

	if at := strings.Index(newAddress, "@"); at > 0 {
		info.Alias = newAddress[:at]
		info.Domain = newAddress[at+1:]
	}

	return info, nil
}

// WaitForEmail 使用通用轮询辅助函数等待匹配谓词的邮件。
func (m *MailondeckBrowserless) WaitForEmail(ctx context.Context, inbox *InboxInfo, timeout time.Duration, predicate func(*EmailMessage) bool) (*EmailMessage, error) {
	return WaitForEmailPolling(ctx, m, inbox, timeout, DefaultPollInterval, predicate)
}
