package admin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/config"
	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookieName     = "session"
	defaultSessionTTL     = 30 * 24 * time.Hour
	rateWindow            = 1 * time.Minute
	rateMaxAttempts       = 5
)

// getSessionTTL 从环境变量读取 session 有效期，默认 30 天
func getSessionTTL() time.Duration {
	if v := os.Getenv("TURAPIS_SESSION_TTL"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil && hours > 0 {
			return time.Duration(hours) * time.Hour
		}
	}
	return defaultSessionTTL
}

// AdminAuth 管理后台鉴权（DB 持久化 session + 登录限速）
type AdminAuth struct {
	store     *config.Store
	mu        sync.Mutex
	attempts  map[string][]time.Time // IP → recent attempts
	cleanupCh chan struct{}
}

// NewAdminAuth 初始化鉴权系统
func NewAdminAuth(store *config.Store) *AdminAuth {
	a := &AdminAuth{
		store:     store,
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

	// 密码正确 → 生成 session token，持久化到 DB
	token := randomToken(32)
	ttl := getSessionTTL()
	expiresAt := now.Add(ttl)

	if err := a.store.CreateSession(token, expiresAt); err != nil {
		slog.Error("create_session", "err", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "session creation failed"})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		Expires:  expiresAt,
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// Logout 处理 POST /admin/logout
func (a *AdminAuth) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		_ = a.store.DeleteSession(cookie.Value)
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

		if !a.store.ValidateSession(cookie.Value) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}

		// Sliding expiration: refresh DB session expiry
		ttl := getSessionTTL()
		go a.store.RefreshSession(cookie.Value, ttl)

		// Refresh browser cookie so its MaxAge also slides
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    cookie.Value,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Path:     "/",
			MaxAge:   int(ttl.Seconds()),
		})

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
			n, err := a.store.DeleteExpiredSessions()
			if err != nil {
				slog.Error("cleanup_sessions_failed", "err", err)
			} else if n > 0 {
				slog.Info("cleaned_expired_sessions", "count", n)
			}
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
