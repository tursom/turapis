package email

import (
	"context"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"
)

func mdTestConfig() EmailProviderConfig {
	proxyURL := os.Getenv("EMAIL_TEST_PROXY")
	if proxyURL == "" {
		proxyURL = "http://192.168.0.1:2080"
	}
	return EmailProviderConfig{
		ProxyURL: proxyURL,
	}
}

func TestMailondeckCreateInbox(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewMailondeck(mdTestConfig())
	info, err := p.CreateInbox(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "unexpected create response") {
			t.Skipf("mailondeck API 似乎已变更: %v", err)
		}
		t.Fatalf("CreateInbox 失败: %v", err)
	}

	if info.Address == "" {
		t.Fatal("地址为空")
	}

	if _, err := mail.ParseAddress(info.Address); err != nil {
		t.Fatalf("地址 %q 不是有效的邮件格式: %v", info.Address, err)
	}

	t.Logf("创建 mailondeck 邮箱: %s (token: %s)", info.Address, info.Token)
}

func TestMailondeckGetMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewMailondeck(mdTestConfig())
	info, err := p.CreateInbox(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "unexpected create response") {
			t.Skipf("mailondeck API 似乎已变更: %v", err)
		}
		if strings.Contains(err.Error(), "EOF") {
			t.Skipf("暂时性网络错误: %v", err)
		}
		t.Fatalf("CreateInbox 失败: %v", err)
	}

	msgs, err := p.GetMessages(ctx, info)
	if err != nil {
		if strings.Contains(err.Error(), "EOF") {
			t.Skipf("暂时性网络错误: %v", err)
		}
		t.Fatalf("GetMessages 失败: %v", err)
	}

	t.Logf("邮箱 %s 内有 %d 封邮件（新邮箱预期为 0）", info.Address, len(msgs))
}

func TestMailondeckSupportsReuse(t *testing.T) {
	p := NewMailondeck(mdTestConfig())
	if !p.SupportsReuse() {
		t.Error("Mailondeck.SupportsReuse() 应返回 true")
	}
}

func TestMailondeckName(t *testing.T) {
	p := NewMailondeck(mdTestConfig())
	if p.Name() != "mailondeck" {
		t.Errorf("Name() = %q, 期望 %q", p.Name(), "mailondeck")
	}
}

func TestMailondeckCreateInboxWithAlias(t *testing.T) {
	if testing.Short() {
		t.Skip("在 short 模式下跳过集成测试")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p := NewMailondeck(mdTestConfig())
	info, err := p.CreateInboxWithAlias(ctx, "codextest", "")
	if err != nil {
		if strings.Contains(err.Error(), "unexpected create response") ||
			strings.Contains(err.Error(), "unexpected") {
			t.Skipf("mailondeck API 似乎已变更: %v", err)
		}
		if strings.Contains(err.Error(), "empty response") {
			t.Skipf("mailondeck 更改邮件返回为空: %v", err)
		}
		t.Fatalf("CreateInboxWithAlias 失败: %v", err)
	}

	if info.Address == "" {
		t.Fatal("地址为空")
	}

	t.Logf("创建别名邮箱: %s", info.Address)
}

func TestMailondeckInterfaceSatisfaction(t *testing.T) {
	var _ EmailProvider = NewMailondeck(mdTestConfig())
}
