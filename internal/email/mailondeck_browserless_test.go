package email

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tursom/turapis/internal/browser"
)

func newMDbClient() *MailondeckBrowserless {
	wsURL := os.Getenv("BROWSERLESS_URL")
	if wsURL == "" {
		wsURL = "ws://localhost:3000/chromium"
	}
	bc := browser.NewBrowserlessClient(wsURL, 60*time.Second)
	return NewMailondeckBrowserless(bc)
}

func TestMailondeckBLCreateInbox(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	m := newMDbClient()
	info, err := m.CreateInbox(ctx)
	if err != nil {
		t.Fatalf("CreateInbox: %v", err)
	}

	if info.Address == "" {
		t.Fatal("地址为空")
	}
	if info.Token == "" {
		t.Log("token 为空（mailondeck.com 可能不会为访客用户设置 token）")
	}

	t.Logf("创建邮箱: %s (token: %s)", info.Address, info.Token)
}

func TestMailondeckBLGetMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	m := newMDbClient()
	info, err := m.CreateInbox(ctx)
	if err != nil {
		t.Fatalf("CreateInbox: %v", err)
	}

	msgs, err := m.GetMessages(ctx, info)
	if err != nil {
		t.Fatalf("GetMessages: %v", err)
	}

	if msgs == nil {
		t.Fatal("GetMessages 返回了 nil 切片")
	}

	t.Logf("邮件数量: %d", len(msgs))
}

func TestMailondeckBLSupportsReuse(t *testing.T) {
	m := newMDbClient()
	if !m.SupportsReuse() {
		t.Error("应支持复用")
	}
}

func TestMailondeckBLName(t *testing.T) {
	m := newMDbClient()
	if m.Name() != "mailondeck_browserless" {
		t.Errorf("Name() = %q, 期望 %q", m.Name(), "mailondeck_browserless")
	}
}

func TestMailondeckBLInterfaceSatisfaction(t *testing.T) {
	var _ EmailProvider = newMDbClient()
}
