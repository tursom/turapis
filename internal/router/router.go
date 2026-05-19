package router

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

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
	QuotaBefore  string
	QuotaAfter   string
}

// RouteStream 执行流式故障转移路由
func (r *Router) RouteStream(ctx context.Context, req *models.UnifiedRequest) (*StreamRouteResult, error) {
	return r.routeStream(ctx, req)
}

// RawStreamResult 原始流式路由结果
type RawStreamResult struct {
	Body         io.ReadCloser
	ProviderName string
	QuotaBefore  string
	QuotaAfter   string
}

// RouteRawStream 原始流式透传——选一个 JWT provider，返回原始 io.ReadCloser
func (r *Router) RouteRawStream(ctx context.Context, modelName string, rawBody []byte) (*RawStreamResult, error) {
	chain, err := r.buildPriorityChain(ctx, modelName)
	if err != nil {
		return nil, err
	}
	for i, p := range chain {
		type rawStreamer interface {
			RawResponsesStream(ctx context.Context, rawBody []byte) (*http.Response, error)
		}
		if rs, ok := p.(rawStreamer); ok {
			start := time.Now()
			quotaBefore := r.getProviderQuotaJSON(p.Name())
			resp, err := rs.RawResponsesStream(ctx, rawBody)
			duration := time.Since(start)
			if err != nil {
				recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, "", false, i+1)
				continue
			}
			r.saveQuotaFromHeaders(p.Name(), resp.Header)
			quotaAfter := r.getProviderQuotaJSON(p.Name())
			if resp.StatusCode != 200 {
				recordAttempt(ctx, p.Name(), resp.StatusCode, fmt.Errorf("upstream returned %d", resp.StatusCode), duration, quotaBefore, quotaAfter, false, i+1)
				_, _ = io.ReadAll(io.LimitReader(resp.Body, 65536))
				resp.Body.Close()
				continue
			}
			recordAttempt(ctx, p.Name(), resp.StatusCode, nil, duration, quotaBefore, quotaAfter, true, i+1)
			return &RawStreamResult{
				Body:         resp.Body,
				ProviderName: p.Name(),
				QuotaBefore:  quotaBefore,
				QuotaAfter:   quotaAfter,
			}, nil
		}
	}
	return nil, fmt.Errorf("no raw stream provider available")
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
