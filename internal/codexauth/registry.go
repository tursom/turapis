package codexauth

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/config"
)

// AccountRegistry 是 Codex 账号的线程安全注册表，位于 AutoLoginFlow 流程编排器
// 与 config.Store 持久层之间。读操作使用 RLock，写操作使用 Lock；
// Register 与 EmailCodeLogin 在调用外部流程（耗时，锁外）后获取互斥锁进行 DB 写入。
type AccountRegistry struct {
	mu    sync.RWMutex
	store RegistryStore
	flow  RegFlowRunner
}

// NewRegistry 创建新的 AccountRegistry。
func NewRegistry(store RegistryStore, flow RegFlowRunner) *AccountRegistry {
	return &AccountRegistry{store: store, flow: flow}
}

// List 返回所有 Codex 账号记录。
func (r *AccountRegistry) List(ctx context.Context) ([]config.CodexAccount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.ListCodexAccounts()
}

// GetByID 按主键获取 Codex 账号。
func (r *AccountRegistry) GetByID(ctx context.Context, id int) (*config.CodexAccount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.GetCodexAccount(id)
}

// GetByAccountID 按 account_id 唯一键获取 Codex 账号。
func (r *AccountRegistry) GetByAccountID(ctx context.Context, accountID string) (*config.CodexAccount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.GetCodexAccountByAccountID(accountID)
}

// GetByProviderID 按关联的 provider_id 查找 Codex 账号。
func (r *AccountRegistry) GetByProviderID(ctx context.Context, providerID int) (*config.CodexAccount, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.store.FindCodexAccountByProviderID(providerID)
}

// GetEmailCredential 从账号的 metadata JSON 中提取存储的邮箱凭证。
// 若 metadata 中不存在 email_credential 键或反序列化失败，返回 (nil, nil)。
func (r *AccountRegistry) GetEmailCredential(ctx context.Context, id int) (*EmailCredential, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	account, err := r.store.GetCodexAccount(id)
	if err != nil {
		return nil, fmt.Errorf("get email credential: %w", err)
	}

	m := parseMetadata(account.Metadata)
	raw, ok := m["email_credential"]
	if !ok {
		return nil, nil
	}
	ec, err := EmailCredentialFromJSON(raw)
	if err != nil {
		return nil, nil
	}
	return ec, nil
}

// SetEmailCredential 将邮箱凭证序列化后写入账号的 metadata JSON，
// 不影响 metadata 中的其他字段（如 login_history）。
func (r *AccountRegistry) SetEmailCredential(ctx context.Context, id int, cred *EmailCredential) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	account, err := r.store.GetCodexAccount(id)
	if err != nil {
		return fmt.Errorf("set email credential: %w", err)
	}

	ecJSON, err := EmailCredentialToJSON(cred)
	if err != nil {
		return fmt.Errorf("set email credential: %w", err)
	}

	account.Metadata, err = buildMetadata(account.Metadata, "email_credential", ecJSON)
	if err != nil {
		return fmt.Errorf("set email credential: %w", err)
	}

	return r.store.UpdateCodexAccount(account)
}

// RemoveEmailCredential 从账号的 metadata JSON 中移除 email_credential 键。
func (r *AccountRegistry) RemoveEmailCredential(ctx context.Context, id int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	account, err := r.store.GetCodexAccount(id)
	if err != nil {
		return fmt.Errorf("remove email credential: %w", err)
	}

	account.Metadata, err = removeMetadataKey(account.Metadata, "email_credential")
	if err != nil {
		return fmt.Errorf("remove email credential: %w", err)
	}

	return r.store.UpdateCodexAccount(account)
}

// Remove 删除 Codex 账号及其关联的 Provider。
// 若账号关联了 Provider，先删 Provider（best-effort，忽略错误），再删账号。
// 不存在 ID 时返回错误（由底层 store 产生）。
func (r *AccountRegistry) Remove(ctx context.Context, id int) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	account, err := r.store.GetCodexAccount(id)
	if err != nil {
		return fmt.Errorf("remove: %w", err)
	}

	if account.ProviderID != nil {
		_ = r.store.DeleteProvider(*account.ProviderID)
	}
	return r.store.DeleteCodexAccount(id)
}

// UpdateStatus 更新账号状态（active/expired/needs_login/error）和错误信息。
func (r *AccountRegistry) UpdateStatus(ctx context.Context, id int, status, msg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.UpdateCodexAccountStatus(id, status, msg)
}

// UpdateLastRefresh 更新账号的最后刷新时间为当前 UTC 时间。
func (r *AccountRegistry) UpdateLastRefresh(ctx context.Context, id int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.UpdateCodexAccountRefresh(id)
}

// UpdateLastHealth 更新账号的最后健康检查时间为当前 UTC 时间。
func (r *AccountRegistry) UpdateLastHealth(ctx context.Context, id int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.UpdateCodexAccountHealth(id)
}

// Register 执行完整的自动注册流程（场景 A）并持久化结果。
//
// 流程分为两个阶段：
//  1. 锁外调用 flow.RunRegister() —— 此阶段涉及浏览器自动化和邮件轮询，可能耗时数分钟。
//  2. 获取互斥锁后执行：
//     a. 唯一性检查（account_id 重复则拒绝）
//     b. 从 FlowResult 构建 OAuth 凭证 JSON 并创建 Provider（Codex site ID=4）
//     c. 创建 CodexAccount 记录（status="active"）
//     d. 构建 metadata JSON（含 login_history + email_credential）
//
// 若 Provider 创建成功但 CodexAccount 创建失败，Provider 将变为孤儿记录，
// 由于 FK 为 ON DELETE SET NULL，不会产生数据一致性问题。
func (r *AccountRegistry) Register(ctx context.Context) error {
	flowResult, emailCred, err := r.flow.RunRegister(ctx)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 唯一性守卫：GetCodexAccountByAccountID 在账号不存在时返回错误，
	// 因此 err==nil 表示冲突（账号已存在）。
	if _, err := r.store.GetCodexAccountByAccountID(flowResult.Identity.AccountID); err == nil {
		return fmt.Errorf("register: codex account %q already exists", flowResult.Identity.AccountID)
	}

	credJSON, err := TokenSetToCredentialJSON(flowResult.Tokens)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	provider, _, err := r.store.CreateProviderFromSite(4, flowResult.Identity.Email, "", credJSON)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	providerID := provider.ID
	account := &config.CodexAccount{
		ProviderID: &providerID,
		Email:      flowResult.Identity.Email,
		AccountID:  flowResult.Identity.AccountID,
		UserID:     flowResult.Identity.UserID,
		PlanType:   flowResult.Identity.PlanType,
		Status:     "active",
	}

	if err := r.store.CreateCodexAccount(account); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	metaWithHistory, err := appendLoginHistory("{}", LoginHistoryEntry{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Method:  "auto_register",
		Success: true,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	ecJSON, err := EmailCredentialToJSON(emailCred)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	account.Metadata, err = buildMetadata(metaWithHistory, "email_credential", ecJSON)
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	if err := r.store.UpdateCodexAccount(account); err != nil {
		return fmt.Errorf("register: %w", err)
	}

	return nil
}

// EmailCodeLogin 使用已有邮箱凭证完成重新登录（场景 B2/C）并更新持久化数据。
//
// 流程分为两个阶段：
//  1. 锁外调用 flow.RunRelogin() —— 此阶段涉及浏览器模拟登录和 OAuth PKCE，
//     可能耗时数十秒。
//  2. 获取互斥锁后执行：
//     a. 按 FlowResult.Identity.AccountID 查找账号
//     b. 读取现有 Provider 凭证 JSON，部分更新 tokens 子对象（token 合并，保留 client_id 和 quota 等字段）
//     c. 重置账号 Status 为 "active"，清空 ErrorMsg，更新 LastLogin
//     d. 追加 login_history 条目（method="email_code_login"）
//
// Token 合并策略：仅替换 access_token、refresh_token、id_token、expires_at，
// 保留已存储的 client_id 及 provider 凭证中的其他字段（如 quota）。
func (r *AccountRegistry) EmailCodeLogin(ctx context.Context, emailCred *EmailCredential) error {
	flowResult, err := r.flow.RunRelogin(ctx, emailCred)
	if err != nil {
		return fmt.Errorf("email code login: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	account, err := r.store.GetCodexAccountByAccountID(flowResult.Identity.AccountID)
	if err != nil {
		return fmt.Errorf("email code login: %w", err)
	}

	apiKey, err := r.store.GetProviderAPIKey(*account.ProviderID)
	if err != nil {
		return fmt.Errorf("email code login: %w", err)
	}

	var credMap map[string]any
	if err := json.Unmarshal([]byte(apiKey), &credMap); err != nil {
		return fmt.Errorf("email code login: parse credential: %w", err)
	}

	tokens, _ := credMap["tokens"].(map[string]any)
	if tokens == nil {
		tokens = make(map[string]any)
		credMap["tokens"] = tokens
	}
	tokens["access_token"] = flowResult.Tokens.AccessToken
	tokens["refresh_token"] = flowResult.Tokens.RefreshToken
	tokens["id_token"] = flowResult.Tokens.IDToken
	tokens["expires_at"] = flowResult.Tokens.ExpiresAt

	updatedCred, err := json.Marshal(credMap)
	if err != nil {
		return fmt.Errorf("email code login: marshal credential: %w", err)
	}

	if err := r.store.UpdateProviderAPIKey(*account.ProviderID, string(updatedCred)); err != nil {
		return fmt.Errorf("email code login: %w", err)
	}

	account.LastLogin = time.Now().UTC().Format(time.RFC3339)
	account.Status = "active"
	account.ErrorMsg = ""

	account.Metadata, err = appendLoginHistory(account.Metadata, LoginHistoryEntry{
		Time:    time.Now().UTC().Format(time.RFC3339),
		Method:  "email_code_login",
		Success: true,
	})
	if err != nil {
		return fmt.Errorf("email code login: %w", err)
	}

	if err := r.store.UpdateCodexAccount(account); err != nil {
		return fmt.Errorf("email code login: %w", err)
	}

	return nil
}

// parseMetadata 将 metadata JSON 字符串解析为 map[string]json.RawMessage，
// 在输入为空或解析失败时返回空 map。
func parseMetadata(raw string) map[string]json.RawMessage {
	m := make(map[string]json.RawMessage)
	if raw != "" {
		json.Unmarshal([]byte(raw), &m)
	}
	return m
}

// buildMetadata 向现有 metadata JSON 中设置指定键的值，返回序列化后的字符串。
// 该操作不会影响 raw 中已有的其他键。
func buildMetadata(raw string, key string, value json.RawMessage) (string, error) {
	m := parseMetadata(raw)
	m[key] = value
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("build metadata: %w", err)
	}
	return string(data), nil
}

func removeMetadataKey(raw string, key string) (string, error) {
	m := parseMetadata(raw)
	delete(m, key)
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("remove metadata key: %w", err)
	}
	return string(data), nil
}

// appendLoginHistory 向 metadata 的 login_history 数组追加一条记录，
// 返回更新后的完整 metadata JSON 字符串。
func appendLoginHistory(raw string, entry LoginHistoryEntry) (string, error) {
	m := parseMetadata(raw)
	var history []LoginHistoryEntry
	if rawHistory, ok := m["login_history"]; ok {
		if err := json.Unmarshal(rawHistory, &history); err != nil {
			return "", fmt.Errorf("parse login history: %w", err)
		}
	}
	history = append(history, entry)
	historyJSON, err := json.Marshal(history)
	if err != nil {
		return "", fmt.Errorf("marshal login history: %w", err)
	}
	m["login_history"] = json.RawMessage(historyJSON)
	data, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	return string(data), nil
}
