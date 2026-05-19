package router

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
)

// Router 故障转移路由器
type Router struct {
	store    *config.Store
	registry *provider.Registry
}

// New 创建新的 Router
func New(store *config.Store, registry *provider.Registry) *Router {
	return &Router{store: store, registry: registry}
}

// Route 执行故障转移路由（非流式）
func (r *Router) Route(ctx context.Context, req *models.UnifiedRequest) (*RouteResult, error) {
	if req.Stream {
		return nil, r.routeStreamToResult(ctx, req)
	}
	return r.routeNonStream(ctx, req)
}

// StreamRouteResult 流式路由结果
type StreamRouteResult struct {
	Events       <-chan models.UnifiedStreamEvent
	ProviderName string
}

// RouteStream 执行流式故障转移路由
func (r *Router) RouteStream(ctx context.Context, req *models.UnifiedRequest) (*StreamRouteResult, error) {
	return r.routeStream(ctx, req)
}

// RouteRawStream 原始流式透传——选一个 JWT provider，返回原始 io.ReadCloser
func (r *Router) RouteRawStream(ctx context.Context, modelName string, rawBody []byte) (io.ReadCloser, string, error) {
	chain, err := r.buildPriorityChain(modelName)
	if err != nil {
		return nil, "", err
	}
	for _, p := range chain {
		type rawStreamer interface {
			RawResponsesStream(ctx context.Context, rawBody []byte) (*http.Response, error)
		}
		if rs, ok := p.(rawStreamer); ok {
			resp, err := rs.RawResponsesStream(ctx, rawBody)
			if err != nil {
				continue
			}
			r.saveQuotaFromHeaders(p.Name(), resp.Header)
			if resp.StatusCode != 200 {
				_, _ = io.ReadAll(io.LimitReader(resp.Body, 65536))
				resp.Body.Close()
				continue
			}
			return resp.Body, p.Name(), nil
		}
	}
	return nil, "", fmt.Errorf("no raw stream provider available")
}

// routeStreamToResult 流式路由的兼容包装（当 caller 以非流式方式调用流式请求时）
func (r *Router) routeStreamToResult(ctx context.Context, req *models.UnifiedRequest) error {
	result, err := r.routeStream(ctx, req)
	if err != nil {
		return err
	}
	// 消费直到结束
	for range result.Events {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
	return nil
}
