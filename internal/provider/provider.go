package provider

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/models"
	"golang.org/x/net/proxy"
)

var sharedTransport = buildTransport(nil)

func SharedTransport() *http.Transport { return sharedTransport }

func NewTransportWithProxy(proxyURL string) *http.Transport {
	u, err := url.Parse(proxyURL)
	if err != nil || u.Host == "" {
		return SharedTransport()
	}
	return buildTransport(u)
}

func buildTransport(proxyURL *url.URL) *http.Transport {
	if proxyURL == nil {
		for _, env := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
			v := os.Getenv(env)
			if v == "" {
				continue
			}
			u, err := url.Parse(v)
			if err == nil && u.Host != "" {
				proxyURL = u
				break
			}
		}
	}

	t := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
		TLSHandshakeTimeout: 10 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
			return d.DialContext(ctx, "tcp4", addr)
		},
	}

	if proxyURL == nil {
		return t
	}

	switch proxyURL.Scheme {
	case "socks5":
		dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, nil, proxy.Direct)
		if err == nil {
			t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			}
		}
	default:
		t.Proxy = http.ProxyURL(proxyURL)
	}
	return t
}

type Provider interface {
	Name() string
	ID() int
	ChatCompletion(ctx context.Context, req *models.UnifiedRequest) (*models.UnifiedResponse, error)
	ChatCompletionStream(ctx context.Context, req *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error)
	ListModels(ctx context.Context) ([]models.ModelInfo, error)
	Protocol() models.ProtocolType
	SupportsTool(name string) bool
	// SetAPIKey 热更新 API Key（用于 OAuth token 刷新后同步更新内存中的 Provider）。
	SetAPIKey(key string)
}

// ProviderKeyUpdater 定义在 OAuth token 刷新后更新内存中 Provider API key 的接口。
// *Registry 实现此接口。
type ProviderKeyUpdater interface {
	SetProviderAPIKey(providerID int, accessToken string) error
}

type Registry struct {
	mu        sync.RWMutex
	providers map[int]Provider
}

func NewRegistry() *Registry {
	return &Registry{providers: make(map[int]Provider)}
}

func (r *Registry) Register(p Provider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID()] = p
}

func (r *Registry) Get(id int) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

func (r *Registry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Provider, 0, len(r.providers))
	for _, p := range r.providers {
		result = append(result, p)
	}
	return result
}

func (r *Registry) Delete(id int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.providers, id)
}

func (r *Registry) SetProviderAPIKey(providerID int, accessToken string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[providerID]
	if !ok {
		return fmt.Errorf("provider %d not found in registry", providerID)
	}
	p.SetAPIKey(accessToken)
	return nil
}
