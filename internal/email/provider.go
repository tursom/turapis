// Package email 提供多提供商邮件接口，用于从临时邮件服务接收验证邮件，
// 供 Codex 自动登录脚手架使用。
package email

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"golang.org/x/net/proxy"
)

// DefaultPollInterval 是轮询邮箱的默认间隔时间。
const DefaultPollInterval = 5 * time.Second

// DefaultPollTimeout 是等待邮件的默认最大等待时间。
const DefaultPollTimeout = 120 * time.Second

// EmailMessage 表示临时邮箱中收到的一封邮件。
type EmailMessage struct {
	// ID 是提供商特定的消息标识符。
	ID string `json:"id"`
	// From 是邮件发件人地址。
	From string `json:"from"`
	// To 是邮件收件人地址。
	To string `json:"to"`
	// Subject 是邮件主题行。
	Subject string `json:"subject"`
	// Body 是邮件的纯文本正文。
	Body string `json:"body"`
	// HTML 是邮件的 HTML 正文，可能为空。
	HTML string `json:"html,omitempty"`
	// Date 是邮件的时间戳。
	Date string `json:"date"`
}

// InboxInfo 保存创建的临时邮箱的信息。
type InboxInfo struct {
	// Address 是邮箱的完整电子邮件地址。
	Address string `json:"address"`
	// Provider 是创建此邮箱的提供商名称。
	Provider string `json:"provider"`
	// Token 是此邮箱的提供商特定访问令牌。
	Token string `json:"token"`
	// Domain 是电子邮件地址的域名部分。
	Domain string `json:"domain,omitempty"`
	// Alias 是电子邮件地址的本地部分（用户名）。
	Alias string `json:"alias,omitempty"`
	// Extra 保存提供商特定的附加数据。
	Extra map[string]string `json:"extra,omitempty"`
}

// EmailProviderConfig 保存创建 EmailProvider 实例的配置。
type EmailProviderConfig struct {
	ProxyURL string
	APIKey   string
	Domain   string // custom domain for inbox creation
}

// buildTransport 创建一个带有可选代理支持的 http.Transport。
// proxyURL 可以为空（直连）、HTTP 代理（http://host:port）
// 或 SOCKS5 代理（socks5://host:port）。
func buildTransport(proxyURL string) *http.Transport {
	t := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			return d.DialContext(ctx, "tcp4", addr)
		},
	}

	if proxyURL == "" {
		return t
	}

	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return t
	}

	switch u.Scheme {
	case "socks5":
		dialer, err := proxy.SOCKS5("tcp", u.Host, nil, proxy.Direct)
		if err == nil {
			t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
	default:
		t.Proxy = http.ProxyURL(u)
	}

	return t
}

// EmailProvider 定义了临时邮件提供商的接口。
// 实现类负责创建邮箱、获取消息以及等待验证邮件。
type EmailProvider interface {
	// CreateInbox 创建一个新的临时邮箱并返回其信息。
	CreateInbox(ctx context.Context) (*InboxInfo, error)

	// CreateInboxWithAlias 使用指定的别名和域名创建新邮箱。
	// 并非所有提供商都支持此功能。
	CreateInboxWithAlias(ctx context.Context, alias, domain string) (*InboxInfo, error)

	// GetMessages 返回当前邮箱中的所有消息。
	GetMessages(ctx context.Context, inbox *InboxInfo) ([]EmailMessage, error)

	// GetMessage 根据消息 ID 返回单条消息。
	GetMessage(ctx context.Context, inbox *InboxInfo, messageID string) (*EmailMessage, error)

	// WaitForEmail 轮询邮箱直到收到匹配谓词的邮件，
	// 或超时到期。返回第一条匹配的消息。
	WaitForEmail(ctx context.Context, inbox *InboxInfo, timeout time.Duration, predicate func(*EmailMessage) bool) (*EmailMessage, error)

	// SupportsReuse 如果提供商支持复用已有邮箱则返回 true。
	SupportsReuse() bool

	// Name 返回提供商的人类可读名称。
	Name() string
}

// verificationCodeRE 匹配 OpenAI/Codex 通常发送的 6 位数字验证码。
var verificationCodeRE = regexp.MustCompile(`\b(\d{6})\b`)

// verificationCodeStyledRE 匹配具有特定 HTML 样式的验证码
//（OpenAI 邮件中验证码通常在灰底 #F3F3F3 的段落中）。
var verificationCodeStyledRE = regexp.MustCompile(`background-color:\s*#F3F3F3[^>]*>[\s\S]*?(\d{6})[\s\S]*?</p>`)

// verificationCodeContextRE 匹配上下文中的验证码，如 "Verification code: 123456"。
var verificationCodeContextRE = regexp.MustCompile(`(?i)(?:verification\s*code|code\s*is|代码为|验证码)[:\s]*(\d{6})`)

// verificationCodeTagRE 匹配 HTML 标签内的 6 位数字。
var verificationCodeTagRE = regexp.MustCompile(`>\s*(\d{6})\s*<`)

// verificationLinkRE 匹配 auth.openai.com 验证 URL。
var verificationLinkRE = regexp.MustCompile(`https://auth\.openai\.com/verify[^\s<>"']*`)

// knownFalseOTP is a known non-OTP 6-digit number that appears in OpenAI emails.
const knownFalseOTP = "177010"

// ExtractVerificationCode 从邮件中提取 6 位数字验证码。
// 采用多阶段提取策略（与 chatgpt2api 一致）：
//  1. 先匹配灰底 #F3F3F3 样式区域内的验证码（最可靠）
//  2. 再匹配 "Verification code" / "code is" / "验证码" 上下文
//  3. 回退匹配 HTML 标签中的 6 位数字
//  4. 排除已知误报码（177010）
func ExtractVerificationCode(msg *EmailMessage) string {
	if msg == nil {
		return ""
	}

	// Build combined content for contextual matching
	content := msg.Subject + "\n" + msg.Body + "\n" + msg.HTML

	// Stage 1: Match styled OTP in gray background
	if matches := verificationCodeStyledRE.FindStringSubmatch(msg.HTML); len(matches) >= 2 {
		if code := matches[1]; code != "" && code != knownFalseOTP {
			return code
		}
	}

	// Stage 2: Match contextual patterns
	if matches := verificationCodeContextRE.FindStringSubmatch(content); len(matches) >= 2 {
		if code := matches[1]; code != "" && code != knownFalseOTP {
			return code
		}
	}

	// Stage 3: Find 6-digit numbers in HTML tags (fallback)
	for _, matches := range verificationCodeTagRE.FindAllStringSubmatch(content, -1) {
		if len(matches) >= 2 {
			code := matches[1]
			if code != "" && code != knownFalseOTP {
				return code
			}
		}
	}

	// Stage 4: Generic 6-digit match (last resort)
	return extractGenericCode(msg)
}

// extractGenericCode is the legacy fallback for environments that don't
// match the standard OpenAI email format.
func extractGenericCode(msg *EmailMessage) string {
	for _, source := range []string{msg.Body, msg.HTML, msg.Subject} {
		if source == "" {
			continue
		}
		matches := verificationCodeRE.FindStringSubmatch(source)
		if len(matches) >= 2 && matches[1] != knownFalseOTP {
			return matches[1]
		}
	}
	return ""
}

// ExtractVerificationLink 从邮件中提取 auth.openai.com 验证链接。
// 它在 HTML 和纯文本正文中搜索。
func ExtractVerificationLink(msg *EmailMessage) string {
	if msg == nil {
		return ""
	}

	for _, source := range []string{msg.HTML, msg.Body} {
		if source == "" {
			continue
		}
		match := verificationLinkRE.FindString(source)
		if match != "" {
			return match
		}
	}
	return ""
}

// WaitForEmailPolling 是一个通用的轮询循环，等待匹配谓词的邮件到达。
// 它使用指定的轮询间隔，超时由给定的 timeout 控制。
//
// 返回第一条匹配的消息；如果上下文被取消或超时，则返回错误。
func WaitForEmailPolling(ctx context.Context, provider EmailProvider, inbox *InboxInfo, timeout time.Duration, interval time.Duration, predicate func(*EmailMessage) bool) (*EmailMessage, error) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = DefaultPollTimeout
	}

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 在启动定时器之前先做一次即时检查（限流等临时错误非致命，继续轮询）
	msgs, err := provider.GetMessages(ctx, inbox)
	if err == nil {
		for i := range msgs {
			if predicate(&msgs[i]) {
				return &msgs[i], nil
			}
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("context cancelled while waiting for email to %s at %s: %w", inbox.Address, inbox.Provider, ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("timed out waiting for email to %s at %s after %v", inbox.Address, inbox.Provider, timeout)
			}

			msgs, err := provider.GetMessages(ctx, inbox)
			if err != nil {
				// 发生暂时性错误时记录日志但继续轮询。
				continue
			}
			for i := range msgs {
				if predicate(&msgs[i]) {
					return &msgs[i], nil
				}
			}
		}
	}
}
