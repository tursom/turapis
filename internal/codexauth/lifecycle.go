// Package codexauth 提供 Codex 自动登录流程编排器，
// 基于 OAuth PKCE 流程和浏览器自动化实现零人工介入的账号注册与登录。
package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/provider"
)

// CodexAuthConfig 配置 LifecycleManager 各后台 goroutine 的行为。
type CodexAuthConfig struct {
	AutoLoginEnabled    bool
	AutoRefreshEnabled  bool
	AutoHealthEnabled   bool
	AutoLoginInterval   time.Duration
	RefreshInterval     time.Duration
	HealthCheckInterval time.Duration
	MaxConcurrentLogins int
	DefaultPassword     string
	ProxyURL            string
}

// DefaultCodexAuthConfig 返回合理的生产默认配置：
// 注册间隔 1h、刷新间隔 7d、健康检查间隔 24h、最大并发注册数 1。
func DefaultCodexAuthConfig() CodexAuthConfig {
	return CodexAuthConfig{
		AutoLoginEnabled:    true,
		AutoRefreshEnabled:  true,
		AutoHealthEnabled:   true,
		AutoLoginInterval:   1 * time.Hour,
		RefreshInterval:     7 * 24 * time.Hour,
		HealthCheckInterval: 24 * time.Hour,
		MaxConcurrentLogins: 1,
	}
}

// TokenRefresher 定义 OAuth Token 刷新操作接口。
// 生产环境使用 provider.RefreshCodexToken，测试环境可使用 mock。
type TokenRefresher interface {
	Refresh(store RegistryStore, providerID int, proxyURL string) error
}

// defaultTokenRefresher 是 TokenRefresher 的生产实现，
// 直接委托给 provider.RefreshCodexToken。
type defaultTokenRefresher struct{}

func (defaultTokenRefresher) Refresh(store RegistryStore, providerID int, proxyURL string) error {
	return provider.RefreshCodexToken(store, providerID, proxyURL)
}

// CodexHealthProber 定义 Codex API 健康检查的探针接口。
// 用于判断账号的 access_token 是否仍然有效。
type CodexHealthProber interface {
	Probe(ctx context.Context, accessToken string) (*HealthProbeResult, error)
}

// HealthProbeResult 包含一次健康检查探针的完整结果。
type HealthProbeResult struct {
	StatusCode int    // HTTP 状态码（200=正常，401=token 无效）
	Body       string // 响应体（截断至 4KB）
}

// httpCodexHealthProber 使用 HTTP GET 到 Codex /v1/responses 端点作为健康检查。
type httpCodexHealthProber struct {
	client *http.Client
}

// NewHTTPCodexHealthProber 创建基于 HTTP 的健康检查探针。
// 若 client 为 nil，使用 10s 超时的默认客户端。
func NewHTTPCodexHealthProber(client *http.Client) CodexHealthProber {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &httpCodexHealthProber{client: client}
}

func (p *httpCodexHealthProber) Probe(ctx context.Context, accessToken string) (*HealthProbeResult, error) {
	req, err := http.NewRequestWithContext(ctx, "GET",
		"https://api.openai.com/v1/responses?limit=0", nil)
	if err != nil {
		return nil, fmt.Errorf("health probe: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("health probe: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &HealthProbeResult{
		StatusCode: resp.StatusCode,
		Body:       string(body),
	}, nil
}

// LifecycleManager 管理 Codex 账号的后台生命周期 goroutine，
// 包括自动注册、Token 刷新和健康检查三个独立定时任务。
// 通过 context.Context 控制所有 goroutine 的启动与停止。
type LifecycleManager struct {
	cfg       CodexAuthConfig
	reg       *AccountRegistry
	store     RegistryStore
	refresher TokenRefresher
	prober    CodexHealthProber

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	sem    chan struct{}
}

// NewLifecycleManager 创建一个新的 LifecycleManager。
// 若 cfg 中的 Duration 字段为零值，将应用 DefaultCodexAuthConfig 的默认值。
func NewLifecycleManager(
	cfg CodexAuthConfig,
	reg *AccountRegistry,
	store RegistryStore,
	refresher TokenRefresher,
	prober CodexHealthProber,
) *LifecycleManager {
	if cfg.AutoLoginInterval <= 0 {
		cfg.AutoLoginInterval = 1 * time.Hour
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 7 * 24 * time.Hour
	}
	if cfg.HealthCheckInterval <= 0 {
		cfg.HealthCheckInterval = 24 * time.Hour
	}
	if cfg.MaxConcurrentLogins <= 0 {
		cfg.MaxConcurrentLogins = 1
	}
	if refresher == nil {
		refresher = defaultTokenRefresher{}
	}

	return &LifecycleManager{
		cfg:       cfg,
		reg:       reg,
		store:     store,
		refresher: refresher,
		prober:    prober,
		sem:       make(chan struct{}, cfg.MaxConcurrentLogins),
	}
}

// Start 启动所有已启用的后台 goroutine。
// 各 goroutine 使用从 ctx 派生的 context 以支持统一取消。
func (lm *LifecycleManager) Start(ctx context.Context) {
	lm.ctx, lm.cancel = context.WithCancel(ctx)

	if lm.cfg.AutoLoginEnabled {
		lm.wg.Add(1)
		go lm.autoRegisterRoutine()
	}
	if lm.cfg.AutoRefreshEnabled {
		lm.wg.Add(1)
		go lm.refreshRoutine()
	}
	if lm.cfg.AutoHealthEnabled {
		lm.wg.Add(1)
		go lm.healthCheckRoutine()
	}
}

// Shutdown 取消所有后台 goroutine 并等待其退出。
func (lm *LifecycleManager) Shutdown() {
	if lm.cancel != nil {
		lm.cancel()
	}
	lm.wg.Wait()
}

func (lm *LifecycleManager) autoRegisterRoutine() {
	defer lm.wg.Done()

	ticker := time.NewTicker(lm.cfg.AutoLoginInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lm.ctx.Done():
			return
		case <-ticker.C:
			lm.sem <- struct{}{}
			go func() {
				defer func() { <-lm.sem }()
				lm.runAutoRegister()
			}()
		}
	}
}

func (lm *LifecycleManager) runAutoRegister() {
	slog.Info("auto_register_start")
	if err := lm.reg.Register(lm.ctx); err != nil {
		slog.Error("auto_register_failed", "error", err)
	}
}

func (lm *LifecycleManager) refreshRoutine() {
	defer lm.wg.Done()

	lm.runRefreshCycle()

	ticker := time.NewTicker(lm.cfg.RefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lm.ctx.Done():
			return
		case <-ticker.C:
			lm.runRefreshCycle()
		}
	}
}

func (lm *LifecycleManager) runRefreshCycle() {
	accounts, err := lm.reg.List(lm.ctx)
	if err != nil {
		slog.Error("refresh_cycle_list_failed", "error", err)
		return
	}

	for _, account := range accounts {
		lm.refreshOneAccount(account)
	}
}

func (lm *LifecycleManager) refreshOneAccount(account config.CodexAccount) {
	if account.ProviderID == nil {
		return
	}

	providerID := *account.ProviderID
	err := lm.refresher.Refresh(lm.store, providerID, lm.cfg.ProxyURL)
	if err != nil {
		slog.Error("refresh_token_failed",
			"account_id", account.ID,
			"provider_id", providerID,
			"error", err)
		_ = lm.reg.UpdateStatus(lm.ctx, account.ID, "needs_login", err.Error())
		lm.refreshWithFallback(account)
		return
	}

	_ = lm.reg.UpdateLastRefresh(lm.ctx, account.ID)
	_ = lm.reg.UpdateStatus(lm.ctx, account.ID, "active", "")
	slog.Info("refresh_token_success",
		"account_id", account.ID,
		"provider_id", providerID)
}

// refreshWithFallback 在 Token 刷新失败后尝试回退到自动重登（场景 C）。
// 先检查账号是否有存储的邮箱凭证；若有则调用 EmailCodeLogin 恢复 Token，
// 若无则账号保持 needs_login 状态以等待人工干预。
func (lm *LifecycleManager) refreshWithFallback(account config.CodexAccount) {
	ec, err := lm.reg.GetEmailCredential(lm.ctx, account.ID)
	if err != nil || ec == nil {
		slog.Error("refresh_fallback_no_credential",
			"account_id", account.ID,
			"has_credential", ec != nil,
			"error", err)
		return
	}

	slog.Info("refresh_fallback_relogin",
		"account_id", account.ID,
		"email", ec.Email)

	if err := lm.reg.EmailCodeLogin(lm.ctx, ec); err != nil {
		slog.Error("refresh_fallback_relogin_failed",
			"account_id", account.ID,
			"email", ec.Email,
			"error", err)
		_ = lm.reg.UpdateStatus(lm.ctx, account.ID, "error", err.Error())
		return
	}

	_ = lm.reg.UpdateLastRefresh(lm.ctx, account.ID)
	slog.Info("refresh_fallback_relogin_success",
		"account_id", account.ID,
		"email", ec.Email)
}

func (lm *LifecycleManager) healthCheckRoutine() {
	defer lm.wg.Done()

	ticker := time.NewTicker(lm.cfg.HealthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lm.ctx.Done():
			return
		case <-ticker.C:
			lm.runHealthCheckCycle()
		}
	}
}

func (lm *LifecycleManager) runHealthCheckCycle() {
	accounts, err := lm.reg.List(lm.ctx)
	if err != nil {
		slog.Error("health_check_list_failed", "error", err)
		return
	}

	for _, account := range accounts {
		lm.healthCheckOneAccount(account)
	}
}

func (lm *LifecycleManager) healthCheckOneAccount(account config.CodexAccount) {
	if account.ProviderID == nil {
		return
	}

	apiKey, err := lm.store.GetProviderAPIKey(*account.ProviderID)
	if err != nil {
		slog.Error("health_check_get_key_failed",
			"account_id", account.ID,
			"error", err)
		return
	}

	accessToken := extractAccessTokenFromCredentialJSON(apiKey)
	if accessToken == "" {
		slog.Error("health_check_no_access_token",
			"account_id", account.ID)
		_ = lm.reg.UpdateStatus(lm.ctx, account.ID, "needs_login",
			"no access token in credential")
		return
	}

	result, err := lm.prober.Probe(lm.ctx, accessToken)
	if err != nil {
		slog.Error("health_check_probe_failed",
			"account_id", account.ID,
			"error", err)
		return
	}

	_ = lm.reg.UpdateLastHealth(lm.ctx, account.ID)

	if result.StatusCode == 401 {
		slog.Warn("health_check_401",
			"account_id", account.ID)
		lm.refreshWithFallback(account)
	}
}

// extractAccessTokenFromCredentialJSON 从 OAuth 凭证 JSON 中提取 access_token。
// 凭证格式: {"tokens":{"access_token":"...","refresh_token":"...",...}}
// 若解析失败或字段缺失，返回空字符串。
func extractAccessTokenFromCredentialJSON(credJSON string) string {
	var cred struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal([]byte(credJSON), &cred); err != nil {
		return ""
	}
	return cred.Tokens.AccessToken
}
