// Package codexauth 提供 Codex 自动登录流程编排器，
// 基于 OAuth PKCE 流程和浏览器自动化实现零人工介入的账号注册与登录。
package codexauth

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tursom/turapis/internal/config"
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

// RegistryStore 定义 AccountRegistry 所需的 config.Store 方法子集。
type RegistryStore interface {
	CreateProviderFromSite(siteID int, nameOverride, apiKey string, oauthJSON json.RawMessage) (*config.Provider, int, error)
	GetProviderAPIKey(id int) (string, error)
	UpdateProviderAPIKey(id int, apiKey string) error
	DeleteProvider(id int) error
	CreateCodexAccount(a *config.CodexAccount) error
	GetCodexAccount(id int) (*config.CodexAccount, error)
	GetCodexAccountByAccountID(accountID string) (*config.CodexAccount, error)
	ListCodexAccounts() ([]config.CodexAccount, error)
	UpdateCodexAccount(a *config.CodexAccount) error
	DeleteCodexAccount(id int) error
	FindCodexAccountByProviderID(providerID int) (*config.CodexAccount, error)
	UpdateCodexAccountStatus(id int, status, errorMsg string) error
	UpdateCodexAccountRefresh(id int) error
	UpdateCodexAccountHealth(id int) error
}

// RegFlowRunner 定义 AccountRegistry 所需的 AutoLoginFlow 方法子集。
type RegFlowRunner interface {
	RunRegister(ctx context.Context) (*FlowResult, *EmailCredential, error)
	RunRelogin(ctx context.Context, ec *EmailCredential) (*FlowResult, error)
}

// LoginHistoryEntry 记录一次登录/注册事件。
type LoginHistoryEntry struct {
	Time    string `json:"time"`
	Method  string `json:"method"` // "auto_register" | "email_code_login"
	Success bool   `json:"success"`
}
