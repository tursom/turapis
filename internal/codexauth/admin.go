package codexauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/admin"
	"github.com/tursom/turapis/internal/config"
)

// taskStatus 表示异步任务的执行状态。
type taskStatus string

const (
	taskRunning taskStatus = "running"
	taskDone    taskStatus = "done"
	taskFailed  taskStatus = "failed"
)

// asyncTask 表示一个异步任务（注册或重登）的执行状态。
// 任务在 goroutine 中执行，通过 taskTracker 查询和更新。
type asyncTask struct {
	ID        string      `json:"id"`
	Type      string      `json:"type"`
	AccountID *int        `json:"account_id,omitempty"`
	Status    taskStatus  `json:"status"`
	Error     string      `json:"error,omitempty"`
	CreatedAt string      `json:"created_at"`
	Result    interface{} `json:"result,omitempty"`
}

// taskTracker 是内存中的异步任务注册表，使用 RWMutex 保证线程安全。
// 任务在进程重启后丢失，仅适用于单进程部署。
type taskTracker struct {
	mu    sync.RWMutex
	tasks map[string]*asyncTask
}

func newTaskTracker() *taskTracker {
	return &taskTracker{tasks: make(map[string]*asyncTask)}
}

// create 创建一个新任务并返回其引用。
// taskType 为 "register" 或 "relogin"；accountID 可选（重登时关联账号）。
func (tt *taskTracker) create(taskType string, accountID *int) *asyncTask {
	tt.mu.Lock()
	defer tt.mu.Unlock()

	id := randomHex(16)
	t := &asyncTask{
		ID:        id,
		Type:      taskType,
		AccountID: accountID,
		Status:    taskRunning,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	tt.tasks[id] = t
	return t
}

// get 按 ID 查询任务，返回任务及其存在性。
func (tt *taskTracker) get(id string) (*asyncTask, bool) {
	tt.mu.RLock()
	defer tt.mu.RUnlock()
	t, ok := tt.tasks[id]
	return t, ok
}

// complete 将任务标记为完成或失败。
// err 为 nil 时状态变为 taskDone，否则为 taskFailed 并记录错误信息。
func (tt *taskTracker) complete(id string, err error, result interface{}) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	t, ok := tt.tasks[id]
	if !ok {
		return
	}
	if err != nil {
		t.Status = taskFailed
		t.Error = err.Error()
	} else {
		t.Status = taskDone
		t.Result = result
	}
}

// cancel 将运行中任务标记为取消。持 mu 锁防止与 complete 的并发竞争。
// 返回 (task, true) 表示成功取消，(nil, false) 表示任务不存在，
// (task, false) 表示任务存在但非 running 状态。
func (tt *taskTracker) cancel(id string) (*asyncTask, bool) {
	tt.mu.Lock()
	defer tt.mu.Unlock()
	t, ok := tt.tasks[id]
	if !ok {
		return nil, false
	}
	if t.Status != taskRunning {
		return t, false
	}
	t.Status = taskFailed
	t.Error = "cancelled by user"
	return t, true
}

// CodexAdmin 是 Codex 账号管理的 HTTP API 处理器。
// 挂载在 /admin/codex/ 下，使用 admin.AdminAuth 进行 session 认证。
// 读操作（GET）要求登录用户，写操作要求管理员角色。
type CodexAdmin struct {
	reg           *AccountRegistry
	auth          *admin.AdminAuth
	lm            *LifecycleManager
	flow          LoginFlowRunner
	browserClient BrowserClient
	tasks         *taskTracker
}

// NewCodexAdmin 创建一个 CodexAdmin 实例。
// bc 为浏览器客户端，nil 表示 browserless 不可用（/browser/status 返回 disconnected）。
func NewCodexAdmin(
	reg *AccountRegistry,
	auth *admin.AdminAuth,
	lm *LifecycleManager,
	flow LoginFlowRunner,
	bc BrowserClient,
) *CodexAdmin {
	return &CodexAdmin{
		reg:           reg,
		auth:          auth,
		lm:            lm,
		flow:          flow,
		browserClient: bc,
		tasks:         newTaskTracker(),
	}
}

// Routes 返回 chi 路由器，包含 15 个 API 端点。
// 路由分为两个认证组：
//   - middleware 组：GET 端点，仅需登录
//   - middleware + RequireAdmin 组：POST/PUT/DELETE 端点，需管理员
func (c *CodexAdmin) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(chimw.RequestSize(1 << 20))

	r.Group(func(r chi.Router) {
		r.Use(c.auth.Middleware)

		r.Get("/accounts", c.listAccounts)
		r.Get("/accounts/{id}", c.getAccount)

		r.Group(func(r chi.Router) {
			r.Use(c.auth.RequireAdmin)

			r.Post("/register", c.triggerRegister)
			r.Post("/login", c.generateAuthURL)
			r.Post("/accounts/{id}/relogin", c.triggerRelogin)

			r.Get("/tasks/{taskId}", c.getTaskStatus)
			r.Post("/tasks/{taskId}/cancel", c.cancelTask)

			r.Post("/accounts/{id}/refresh", c.manualRefresh)
			r.Post("/accounts/{id}/health-check", c.manualHealthCheck)

			r.Put("/accounts/{id}/email-credential", c.setEmailCredential)
			r.Delete("/accounts/{id}/email-credential", c.deleteEmailCredential)

			r.Delete("/accounts/{id}", c.deleteAccount)

			r.Get("/config", c.getConfig)
			r.Put("/config", c.putConfig)

			r.Get("/browser/status", c.browserStatus)
		})
	})

	return r
}

// listAccounts 返回所有 Codex 账号列表。
func (c *CodexAdmin) listAccounts(w http.ResponseWriter, r *http.Request) {
	accounts, err := c.reg.List(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if accounts == nil {
		accounts = []config.CodexAccount{}
	}
	writeJSON(w, http.StatusOK, accounts)
}

// getAccount 按主键 ID 返回单个账号详情。
func (c *CodexAdmin) getAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	account, err := c.reg.GetByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, account)
}

// triggerRegister 触发自动注册（场景 A），异步执行。
// 若 browserClient 未配置，返回 503。
func (c *CodexAdmin) triggerRegister(w http.ResponseWriter, r *http.Request) {
	if c.browserClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "browser automation not configured"})
		return
	}

	task := c.tasks.create("register", nil)

	go func() {
		err := c.reg.Register(context.Background())
		c.tasks.complete(task.ID, err, nil)
		if err != nil {
			slog.Error("admin_register_failed", "task_id", task.ID, "error", err)
		} else {
			slog.Info("admin_register_done", "task_id", task.ID)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"task_id": task.ID,
		"status":  "running",
	})
}

// generateAuthURL 生成 OAuth 授权 URL（场景 B），立即返回。
// 调用者拿到 URL 后在浏览器中打开；后台 goroutine 等待 OAuth 回调并交换令牌。
func (c *CodexAdmin) generateAuthURL(w http.ResponseWriter, r *http.Request) {
	authURL, wait, err := c.flow.StartLogin(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// 场景 B 的令牌交换在后台等待回调；由 /login 调用方负责在浏览器中操作
	go func() {
		result, wErr := wait(context.Background())
		if wErr != nil {
			slog.Error("admin_login_callback_failed", "error", wErr)
			return
		}
		slog.Info("admin_login_callback_success", "account_id", result.Identity.AccountID)
	}()

	writeJSON(w, http.StatusOK, map[string]string{"auth_url": authURL})
}

// triggerRelogin 触发使用存储的邮箱凭证重新登录（场景 C），异步执行。
// 若 browserClient 未配置，返回 503。
func (c *CodexAdmin) triggerRelogin(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	ec, err := c.reg.GetEmailCredential(r.Context(), id)
	if err != nil || ec == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no email credential for this account"})
		return
	}

	if c.browserClient == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "browser automation not configured"})
		return
	}

	task := c.tasks.create("relogin", &id)

	go func() {
		err := c.reg.EmailCodeLogin(context.Background(), ec)
		c.tasks.complete(task.ID, err, nil)
		if err != nil {
			slog.Error("admin_relogin_failed", "task_id", task.ID, "account_id", id, "error", err)
		} else {
			slog.Info("admin_relogin_done", "task_id", task.ID, "account_id", id)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"task_id": task.ID,
		"status":  "running",
	})
}

// getTaskStatus 查询异步任务的当前状态。
func (c *CodexAdmin) getTaskStatus(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, ok := c.tasks.get(taskID)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// cancelTask 取消一个运行中的任务。
// 注意：Go 无法中断正在执行的 goroutine，因此仅标记任务为 failed。
func (c *CodexAdmin) cancelTask(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, cancelled := c.tasks.cancel(taskID)
	if !cancelled {
		if task == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		} else {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "task not running"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// manualRefresh 手动触发单个账号的 Token 刷新。
// 同步执行：刷新是轻量 HTTP 调用（非浏览器自动化），通常秒级完成。
// 刷新失败后会自动尝试回退重登（如有存储的邮箱凭证）。
func (c *CodexAdmin) manualRefresh(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := c.lm.RefreshAccount(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// manualHealthCheck 手动触发单个账号的健康检查。
// 向 Codex API 发送轻量 GET /v1/responses 探测请求，
// 若返回 401 则自动触发回退重登。
func (c *CodexAdmin) manualHealthCheck(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := c.lm.HealthCheckAccount(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// setEmailCredential 为账号设置或更新邮箱凭证。
// 凭证存储在 codex_accounts.metadata JSON 的 email_credential 字段中。
func (c *CodexAdmin) setEmailCredential(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	var cred EmailCredential
	if err := json.NewDecoder(r.Body).Decode(&cred); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json body"})
		return
	}

	if err := c.reg.SetEmailCredential(r.Context(), id, &cred); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// deleteEmailCredential 删除账号的邮箱凭证，使其无法用于自动重登。
func (c *CodexAdmin) deleteEmailCredential(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := c.reg.RemoveEmailCredential(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// deleteAccount 删除 Codex 账号及其关联的 Provider。
func (c *CodexAdmin) deleteAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}

	if err := c.reg.Remove(r.Context(), id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// getConfig 返回当前 LifecycleManager 的配置（只读）。
func (c *CodexAdmin) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg := c.lm.Config()
	writeJSON(w, http.StatusOK, configResp{
		AutoLoginEnabled:    cfg.AutoLoginEnabled,
		AutoRefreshEnabled:  cfg.AutoRefreshEnabled,
		AutoHealthEnabled:   cfg.AutoHealthEnabled,
		AutoLoginInterval:   cfg.AutoLoginInterval.String(),
		RefreshInterval:     cfg.RefreshInterval.String(),
		HealthCheckInterval: cfg.HealthCheckInterval.String(),
		MaxConcurrentLogins: cfg.MaxConcurrentLogins,
		ProxyURL:            cfg.ProxyURL,
	})
}

// configResp 是 GET /config 的 JSON 响应结构。
// Duration 字段序列化为字符串（如 "1h0m0s"），而非纳秒数值。
type configResp struct {
	AutoLoginEnabled    bool   `json:"auto_login_enabled"`
	AutoRefreshEnabled  bool   `json:"auto_refresh_enabled"`
	AutoHealthEnabled   bool   `json:"auto_health_enabled"`
	AutoLoginInterval   string `json:"auto_login_interval"`
	RefreshInterval     string `json:"refresh_interval"`
	HealthCheckInterval string `json:"health_check_interval"`
	MaxConcurrentLogins int    `json:"max_concurrent_logins"`
	ProxyURL            string `json:"proxy_url"`
}

// putConfig 更新 LifecycleManager 配置（暂不支持）。
func (c *CodexAdmin) putConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "config mutation not yet supported"})
}

// browserStatus 返回 browserless 连接状态。
// connected=true 时 browserClient 已配置且可用。
func (c *CodexAdmin) browserStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"connected": c.browserClient != nil})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
