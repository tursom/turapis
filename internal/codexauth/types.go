// Package codexauth 提供 Codex 自动登录流程编排器，
// 基于 OAuth PKCE 流程和浏览器自动化实现零人工介入的账号注册与登录。
package codexauth

import (
	"context"
	"time"

	"github.com/tursom/turapis/internal/email"
)

// TokenSet 包含一次 OAuth PKCE 流程获取的完整令牌集。
type TokenSet struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	AccountID    string `json:"account_id"`
	ExpiresAt    int64  `json:"expires_at"`
}

// AccountIdentity 从 id_token JWT 解析出的账号身份信息。
type AccountIdentity struct {
	Email     string
	AccountID string
	UserID    string
	PlanType  string
}

// EmailCredential 存储邮箱凭证，掌握邮箱即掌握账号，无需密码。
// 用于后续复用已注册的邮箱进行登录。
type EmailCredential struct {
	Email     string `json:"email"`
	Provider  string `json:"provider"`
	Token     string `json:"token"`
	UpdatedAt string `json:"updated_at"`
}

// BrowserClient 定义自动登录流程所需的浏览器操作接口。
// *browser.BrowserlessClient 自动满足此接口。
type BrowserClient interface {
	NewContext(ctx context.Context) (context.Context, context.CancelFunc)
	Navigate(ctx context.Context, url string) error
	WaitForSelector(ctx context.Context, selector string) error
	SendKeys(ctx context.Context, selector, text string) error
	Click(ctx context.Context, selector string) error
}

// FlowConfig 配置自动登录流程的各项参数。
type FlowConfig struct {
	EmailProvider  email.EmailProvider
	BrowserClient  BrowserClient
	CallbackPort   int
	PollInterval   time.Duration
	PollTimeout    time.Duration
	BrowserTimeout time.Duration
	// TokenURL 覆盖默认的 OAuth token 端点 (https://auth.openai.com/oauth/token)。
	// 为空时使用默认地址。主要用于测试。
	TokenURL string
}

// FlowResult 包含一次自动登录流程的完整结果，
// 包括令牌集和解析后的账号身份信息。
type FlowResult struct {
	Tokens   *TokenSet
	Identity *AccountIdentity
}

// DefaultFlowConfig 返回一组合理的默认流程配置。
func DefaultFlowConfig() FlowConfig {
	return FlowConfig{
		CallbackPort:   1455,
		PollInterval:   5 * time.Second,
		PollTimeout:    120 * time.Second,
		BrowserTimeout: 120 * time.Second,
	}
}
