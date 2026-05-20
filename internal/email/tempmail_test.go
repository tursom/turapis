package email

import (
	"context"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"
)

func testConfig() EmailProviderConfig {
	proxyURL := os.Getenv("EMAIL_TEST_PROXY")
	if proxyURL == "" {
		proxyURL = "http://192.168.0.1:2080" // 默认代理
	}
	return EmailProviderConfig{
		ProxyURL: proxyURL,
	}
}

func skipIfTransient(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "EOF") {
		t.Skipf("暂时性网络错误: %v", err)
	}
}

func TestTempmailCreateInbox(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewTempmailLOL(testConfig())
	info, err := p.CreateInbox(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "EOF") {
			t.Skipf("暂时性网络错误: %v", err)
		}
		t.Fatalf("CreateInbox 失败: %v", err)
	}

	if info.Address == "" {
		t.Fatal("地址为空")
	}
	if info.Token == "" {
		t.Fatal("token 为空")
	}

	if _, err := mail.ParseAddress(info.Address); err != nil {
		t.Fatalf("地址 %q 不是有效的邮件格式: %v", info.Address, err)
	}

	t.Logf("创建邮箱: %s (token: %s)", info.Address, info.Token)
}

func TestTempmailGetMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewTempmailLOL(testConfig())
	info, err := p.CreateInbox(ctx)
	if err != nil {
		skipIfTransient(t, err)
		t.Fatalf("CreateInbox 失败: %v", err)
	}

	msgs, err := p.GetMessages(ctx, info)
	if err != nil {
		skipIfTransient(t, err)
		t.Fatalf("GetMessages 失败: %v", err)
	}

	if msgs == nil {
		t.Fatal("GetMessages 返回了 nil 切片")
	}

	t.Logf("邮箱 %s 内有 %d 封邮件", info.Address, len(msgs))
}

func TestTempmailSupportsReuse(t *testing.T) {
	p := NewTempmailLOL(testConfig())
	if p.SupportsReuse() {
		t.Error("TempmailLOL.SupportsReuse() 应返回 false")
	}
}

func TestTempmailName(t *testing.T) {
	p := NewTempmailLOL(testConfig())
	if p.Name() != "tempmail_lol" {
		t.Errorf("Name() = %q, 期望 %q", p.Name(), "tempmail_lol")
	}
}

func TestTempmailCreateInboxWithAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewTempmailLOL(testConfig())
	_, err := p.CreateInboxWithAlias(ctx, "testalias", "tempmail.lol")
	if err == nil {
		t.Error("CreateInboxWithAlias 在免费版上应返回错误")
	}
}

func TestTempmailCreateInboxWithPrefix(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewTempmailLOL(testConfig())
	info, err := p.CreateInboxWithPrefix(ctx, "codextest")
	if err != nil {
		skipIfTransient(t, err)
		t.Fatalf("CreateInboxWithPrefix 失败: %v", err)
	}

	if info.Address == "" {
		t.Fatal("地址为空")
	}

	t.Logf("创建带前缀的邮箱: %s", info.Address)
}

func TestTempmailRealtimeEmail(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	p := NewTempmailLOL(testConfig())
	info, err := p.CreateInbox(ctx)
	if err != nil {
		skipIfTransient(t, err)
		t.Fatalf("CreateInbox 失败: %v", err)
	}

	t.Logf("正在等待邮件到达: %s （此测试在 60 秒内没有真实邮件将超时）", info.Address)

	predicate := func(msg *EmailMessage) bool {
		return msg.From != ""
	}

	timeout := 60 * time.Second
	msg, err := p.WaitForEmail(ctx, info, timeout, predicate)
	if err != nil {
		t.Skipf("在 %v 内未收到邮件: %v （没有真实发件人时这是预期行为）", timeout, err)
		return
	}

	t.Logf("收到来自 %s 的邮件，主题为 %q", msg.From, msg.Subject)
}

func TestTempmailInterfaceSatisfaction(t *testing.T) {
	var _ EmailProvider = NewTempmailLOL(testConfig())
}
