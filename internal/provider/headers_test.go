package provider

import (
	"context"
	"net/http"
	"testing"

	"github.com/tursom/turapis/internal/models"
)

func TestForwardClientHeadersSkipsAcceptEncoding(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	headers := http.Header{
		"Accept-Encoding": []string{"gzip, br"},
		"X-Test":          []string{"ok"},
	}

	ForwardClientHeaders(req, models.WithClientHeaders(context.Background(), headers))

	if got := req.Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("accept-encoding = %q, want empty", got)
	}
	if got := req.Header.Get("X-Test"); got != "ok" {
		t.Fatalf("x-test = %q, want ok", got)
	}
}
