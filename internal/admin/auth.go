package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName = "session"
	sessionTTL        = 24 * time.Hour
	rateWindow        = 1 * time.Minute
	rateMaxAttempts   = 5
)

// AdminAuth 管理后台鉴权（内存 session + 登录限速）
type AdminAuth struct {
	store      *config.Store
	mu         sync.RWMutex
	sessions   map[string]time.Time // token → expires_at
	attempts   map[string][]time.Time // IP → recent attempts
	cleanupCh  chan struct{}
}

// NewAdminAuth 初始化鉴权系统
func NewAdminAuth(store *config.Store) *AdminAuth {
	a := &AdminAuth{
		store:     store,
		sessions:  make(map[string]time.Time),
		attempts:  make(map[string][]time.Time),
		cleanupCh: make(chan struct{}),
	}
	a.initPassword()
	go a.cleanupLoop()
	return a
}

// 密码初始化
func (a *AdminAuth) initPassword() {
	// 1. 检查是否已有 hash
	if hash, err := a.store.GetSetting("admin_password_hash"); err == nil && hash != "" {
		return
	}

	// 2. 从环境变量初始化
	var password string
	// password will be set from env or default
	// 3. 默认密码
	password = "admin"
	slog.Warn("admin password not configured — using default 'admin'. Set TURAPIS_ADMIN_PASSWORD environment variable.")
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	a.store.SetSetting("admin_password_hash", string(hash))
}

// Login 处理 POST /admin/login
func (a *AdminAuth) Login(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr

	// 限速检查
	a.mu.Lock()
	now := time.Now()
	windowStart := now.Add(-rateWindow)
	var recent []time.Time
	for _, t := range a.attempts[ip] {
		if t.After(windowStart) {
			recent = append(recent, t)
		}
	}
	a.attempts[ip] = recent
	a.mu.Unlock()

	if len(recent) >= rateMaxAttempts {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "too many login attempts, try again later"})
		return
	}

	// 解析请求体
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	// 验证密码
	storedHash, err := a.store.GetSetting("admin_password_hash")
	if err != nil || storedHash == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "password not configured"})
		return
	}

	if bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(body.Password)) != nil {
		// 记录失败尝试
		a.mu.Lock()
		a.attempts[ip] = append(a.attempts[ip], now)
		a.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid password"})
		return
	}

	// 密码正确 → 生成 session token
	token := randomToken(32)
	expiresAt := now.Add(sessionTTL)

	a.mu.Lock()
	a.sessions[token] = expiresAt
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   int(sessionTTL.Seconds()),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Logout 处理 POST /admin/logout
func (a *AdminAuth) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		a.mu.Lock()
		delete(a.sessions, cookie.Value)
		a.mu.Unlock()
	}

	// 清除 Cookie
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   -1,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Middleware chi 中间件 —— 检查 session
func (a *AdminAuth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}

		a.mu.RLock()
		expiresAt, ok := a.sessions[cookie.Value]
		a.mu.RUnlock()

		if !ok || time.Now().After(expiresAt) {
			if ok {
				a.mu.Lock()
				delete(a.sessions, cookie.Value)
				a.mu.Unlock()
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}

		// Sliding expiration: 刷新过期时间
		a.mu.Lock()
		a.sessions[cookie.Value] = time.Now().Add(sessionTTL)
		a.mu.Unlock()

		next.ServeHTTP(w, r)
	})
}

// Shutdown 停止后台清理
func (a *AdminAuth) Shutdown() {
	close(a.cleanupCh)
}

// 后台清理过期 session（每 5 分钟）
func (a *AdminAuth) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			a.mu.Lock()
			now := time.Now()
			for token, expires := range a.sessions {
				if now.After(expires) {
					delete(a.sessions, token)
				}
			}
			a.mu.Unlock()
		case <-a.cleanupCh:
			return
		}
	}
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}
