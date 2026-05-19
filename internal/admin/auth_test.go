package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tursom/turapis/internal/config"
)

func setupTestAuth(t *testing.T) (*AdminAuth, *config.Store) {
	t.Helper()
	store, err := config.NewStore(":memory:")
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	auth := NewAdminAuth(store)
	t.Cleanup(func() { auth.Shutdown() })
	return auth, store
}

func TestLogin_Success(t *testing.T) {
	auth, _ := setupTestAuth(t)

	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"admin"}`
	r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.1:12345"

	auth.Login(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// 检查 Set-Cookie
	setCookie := resp.Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "session=") {
		t.Error("expected session cookie")
	}
	if !strings.Contains(setCookie, "HttpOnly") {
		t.Error("expected HttpOnly")
	}
	if !strings.Contains(setCookie, "Max-Age=") {
		t.Error("expected Max-Age")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	auth, _ := setupTestAuth(t)

	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"wrong"}`
	r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.2:12345"

	auth.Login(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestLogin_RateLimit(t *testing.T) {
	auth, _ := setupTestAuth(t)

	// 发送 5 次错误尝试
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		body := `{"username":"admin","password":"wrong"}`
		r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.RemoteAddr = "127.0.0.3:12345"
		auth.Login(w, r)
	}

	// 第 6 次应被限速
	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"wrong"}`
	r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.3:12345"
	auth.Login(w, r)

	resp := w.Result()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected 429, got %d", resp.StatusCode)
	}
}

func TestMiddleware_ValidSession(t *testing.T) {
	auth, _ := setupTestAuth(t)

	// 先登录获取 session
	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"admin"}`
	r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.4:12345"
	auth.Login(w, r)

	cookies := w.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("no cookies set")
	}

	// 用 session 请求受保护端点
	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"ok": "yes"})
	})

	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("GET", "/admin/providers", nil)
	r2.AddCookie(cookies[0])

	auth.Middleware(nextHandler).ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w2.Code)
	}
}

func TestMiddleware_NoCookie(t *testing.T) {
	auth, _ := setupTestAuth(t)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin/providers", nil)

	auth.Middleware(nextHandler).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestMiddleware_InvalidCookie(t *testing.T) {
	auth, _ := setupTestAuth(t)

	nextHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not be called")
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/admin/providers", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: "nonexistent"})

	auth.Middleware(nextHandler).ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestLogout(t *testing.T) {
	auth, _ := setupTestAuth(t)

	// 先登录
	w := httptest.NewRecorder()
	body := `{"username":"admin","password":"admin"}`
	r := httptest.NewRequest("POST", "/admin/login", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "127.0.0.5:12345"
	auth.Login(w, r)

	cookies := w.Result().Cookies()

	// 登出
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", "/admin/logout", nil)
	r2.AddCookie(cookies[0])

	auth.Logout(w2, r2)

	// 登出后 Cookie 应被清除
	setCookie := w2.Result().Header.Get("Set-Cookie")
	if !strings.Contains(setCookie, "Max-Age=0") {
		t.Errorf("expected cookie clear, got: %s", setCookie)
	}
}
