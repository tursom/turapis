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
	"github.com/tursom/turapis/internal/models"
)

const (
	sessionCookieName = "session"
	defaultSessionTTL = 30 * 24 * time.Hour
	rateWindow        = 1 * time.Minute
	rateMaxAttempts   = 5
)

func getSessionTTL() time.Duration {
	if v := os.Getenv("TURAPIS_SESSION_TTL"); v != "" {
		if hours, err := strconv.Atoi(v); err == nil && hours > 0 {
			return time.Duration(hours) * time.Hour
		}
	}
	return defaultSessionTTL
}

type AdminAuth struct {
	store     *config.Store
	mu        sync.Mutex
	attempts  map[string][]time.Time
	cleanupCh chan struct{}
}

func NewAdminAuth(store *config.Store) *AdminAuth {
	a := &AdminAuth{
		store:     store,
		attempts:  make(map[string][]time.Time),
		cleanupCh: make(chan struct{}),
	}
	a.initAdminUser()
	go a.cleanupLoop()
	return a
}

func (a *AdminAuth) initAdminUser() {
	count, err := a.store.CountUsers()
	if err != nil {
		slog.Error("check_user_count", "err", err)
		return
	}
	if count > 0 {
		return
	}

	password := os.Getenv("TURAPIS_ADMIN_PASSWORD")
	if password == "" {
		password = "admin"
		slog.Warn("default admin password in use — set TURAPIS_ADMIN_PASSWORD to override")
	}

	_, err = a.store.CreateUser("admin", password, "admin")
	if err != nil {
		slog.Error("create_default_admin", "err", err)
		return
	}
	slog.Info("default admin user created", "username", "admin")
}

func (a *AdminAuth) Login(w http.ResponseWriter, r *http.Request) {
	ip := r.RemoteAddr

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

	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Username == "" || body.Password == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "username and password are required"})
		return
	}

	user, err := a.store.ValidateUserPassword(body.Username, body.Password)
	if err != nil {
		a.mu.Lock()
		a.attempts[ip] = append(a.attempts[ip], now)
		a.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid username or password"})
		return
	}

	token := randomToken(32)
	ttl := getSessionTTL()
	expiresAt := now.Add(ttl)

	if err := a.store.CreateSession(token, user.ID, expiresAt); err != nil {
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
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"username": user.Username,
		"role":     user.Role,
	})
}

func (a *AdminAuth) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		_ = a.store.DeleteSession(cookie.Value)
	}

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

		info, err := a.store.GetSessionUser(cookie.Value)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
			return
		}

		ctx := models.WithSessionUser(r.Context(), &models.SessionUser{
			UserID: info.UserID,
			Role:   info.Role,
		})
		r = r.WithContext(ctx)

		ttl := getSessionTTL()
		go a.store.RefreshSession(cookie.Value, ttl)

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

func (a *AdminAuth) RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := models.SessionUserFromContext(r.Context())
		if u == nil || u.Role != "admin" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"error": "admin access required"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *AdminAuth) Shutdown() {
	close(a.cleanupCh)
}

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
