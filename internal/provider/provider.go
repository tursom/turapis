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
	DialContext:          dialIPv4First,
}

func dialIPv4First(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	// 强制仅 IPv4，避免 IPv6 超时
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host); err == nil && len(ips) > 0 {
			return d.DialContext(ctx, "tcp4", net.JoinHostPort(ips[0].String(), port))
		}
	}
	// fallback：仍然用 tcp4，不碰 IPv6
	return d.DialContext(ctx, "tcp4", addr)
}

// SharedTransport 返回全局共享的 HTTP Transport（供各 Provider 使用）
func SharedTransport() *http.Transport {
	return sharedTransport
}

// Provider 是所有上游 AI API 提供者的统一接口
type Provider interface {
	// Name 返回提供者唯一标识名称
	Name() string

	// ChatCompletion 发送非流式聊天补全请求
	ChatCompletion(ctx context.Context, req *models.UnifiedRequest) (*models.UnifiedResponse, error)

	// ChatCompletionStream 发送流式聊天补全请求
	ChatCompletionStream(ctx context.Context, req *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error)

	// ListModels 从上游发现可用模型列表
	ListModels(ctx context.Context) ([]models.ModelInfo, error)

	// Protocol 返回该 Provider 使用的原生协议
	Protocol() models.ProtocolType
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
