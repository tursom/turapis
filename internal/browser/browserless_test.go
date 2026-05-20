package browser

import (
	"testing"
	"time"
)

func TestNewBrowserlessClient(t *testing.T) {
	client := NewBrowserlessClient("ws://example.com:3000/chromium", 30*time.Second)
	if client == nil {
		t.Fatal("期望非 nil 客户端")
	}
	if client.wsURL != "ws://example.com:3000/chromium" {
		t.Errorf("非预期的 wsURL: %s", client.wsURL)
	}
	if client.timeout != 30*time.Second {
		t.Errorf("非预期的 timeout: %v", client.timeout)
	}
}
