package browser

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testWSURL 从 BROWSERLESS_URL 环境变量中返回 browserless WebSocket URL，
// 未设置时回退到 localhost:3000。
func testWSURL() string {
	url := os.Getenv("BROWSERLESS_URL")
	if url == "" {
		return "ws://localhost:3000/chromium"
	}
	return url
}

// newTestClient 为集成测试创建一个 BrowserlessClient。
func newTestClient() *BrowserlessClient {
	return NewBrowserlessClient(testWSURL(), 30*time.Second)
}

func TestBrowserlessNavigate(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	client := newTestClient()
	ctx, cancel := client.NewContext(context.Background())
	defer cancel()

	if err := client.Navigate(ctx, "https://httpbin.org/ip"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	url, err := client.CurrentURL(ctx)
	if err != nil {
		t.Fatalf("CurrentURL: %v", err)
	}
	t.Logf("当前 URL: %s", url)

	if !strings.Contains(url, "httpbin") {
		t.Errorf("期望 URL 包含 'httpbin'，实际为 %q", url)
	}
}

func TestBrowserlessScreenshot(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	client := newTestClient()
	ctx, cancel := client.NewContext(context.Background())
	defer cancel()

	if err := client.Navigate(ctx, "https://httpbin.org"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	path := filepath.Join(t.TempDir(), "screenshot.png")
	if err := client.Screenshot(ctx, path); err != nil {
		t.Fatalf("Screenshot: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat screenshot: %v", err)
	}
	if info.Size() == 0 {
		t.Error("截图文件为空")
	}
	t.Logf("截图已保存: %s (%d 字节)", path, info.Size())
}

func TestBrowserlessSendKeysAndClick(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	client := newTestClient()
	ctx, cancel := client.NewContext(context.Background())
	defer cancel()

	if err := client.Navigate(ctx, "https://httpbin.org/forms/post"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	// 填写表单字段
	if err := client.SendKeys(ctx, `input[name="custname"]`, "Test User"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// 验证输入的值已成功（使用 EvalJS 读取 DOM 属性，
	// 因为 SendKeys 更新的是属性而非 attribute）
	val, err := client.EvalJS(ctx, `document.querySelector('input[name="custname"]').value`)
	if err != nil {
		t.Fatalf("EvalJS: %v", err)
	}
	t.Logf("custname 值: %s", val)
	if val != "Test User" {
		t.Errorf("期望 'Test User'，实际为 %q", val)
	}
}

func TestBrowserlessJavaScript(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过集成测试")
	}

	client := newTestClient()
	ctx, cancel := client.NewContext(context.Background())
	defer cancel()

	if err := client.Navigate(ctx, "https://httpbin.org"); err != nil {
		t.Fatalf("Navigate: %v", err)
	}

	result, err := client.EvalJS(ctx, "navigator.userAgent")
	if err != nil {
		t.Fatalf("EvalJS: %v", err)
	}
	t.Logf("用户代理: %s", result)
	if result == "" {
		t.Error("期望非空的用户代理")
	}
}

func TestBrowserlessConnectionError(t *testing.T) {
	// 此测试故意不跳过 -short，因为它应该很快
	// （预计不会有真正的连接）。
	client := NewBrowserlessClient("ws://invalid:9999/chromium", 5*time.Second)
	ctx, cancel := client.NewContext(context.Background())
	defer cancel()

	err := client.Navigate(ctx, "https://example.com")
	if err == nil {
		t.Error("期望无效 URL 返回错误")
	}
	t.Logf("收到预期的错误: %v", err)
}
