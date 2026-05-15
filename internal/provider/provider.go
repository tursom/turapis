package provider

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/models"
)

// sharedTransport 全局共享 HTTP Transport，所有 Provider 复用
var sharedTransport = &http.Transport{
	MaxIdleConns:        100,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	ForceAttemptHTTP2:   false,
	TLSHandshakeTimeout:  10 * time.Second,
	DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, "tcp4", addr)
	},
}

// SharedTransport 返回全局共享的 HTTP Transport（供各 Provider 使用）
func SharedTransport() *http.Transport {
	return sharedTransport
}

// Provider 是所有上游 AI API 提供者的统一接口
type Provider interface {
	Name() string
	ChatCompletion(ctx context.Context, req *models.UnifiedRequest) (*models.UnifiedResponse, error)
	ChatCompletionStream(ctx context.Context, req *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error)
	ListModels(ctx context.Context) ([]models.ModelInfo, error)
	Protocol() models.ProtocolType
	SupportsTool(name string) bool
}

// Registry 管理所有已注册的 Provider 实例，并发安全
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry 创建新的 Registry
func NewRegistry() *Registry {
	return &Registry{
		providers: make(map[string]Provider),
	}
}

// Register 注册 Provider（并发安全）
func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name()] = p
}

// Get 按名称获取 Provider（并发安全）
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[name]
	return p, ok
}

// List 列出所有已注册 Provider（并发安全）
func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

// Delete 按名称删除 Provider（并发安全）
func (r *Registry) Delete(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, name)
}
