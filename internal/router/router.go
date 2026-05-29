package router

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
)

const (
	failoverCooldownGrowthSetting = "failover_error_cooldown_growth_seconds"
	defaultFailoverCooldownGrowth = 60 * time.Second
)

// Router 故障转移路由器
type Router struct {
	store      *config.Store
	registry   *provider.Registry
	cooldownMu sync.Mutex
	cooldowns  map[int]providerCooldown
	now        func() time.Time
}

// New 创建新的 Router
func New(store *config.Store, registry *provider.Registry) *Router {
	return &Router{
		store:     store,
		registry:  registry,
		cooldowns: make(map[int]providerCooldown),
		now:       time.Now,
	}
}

type providerCooldown struct {
	failures int
	until    time.Time
}

func (r *Router) currentTime() time.Time {
	if r != nil && r.now != nil {
		return r.now()
	}
	return time.Now()
}

func (r *Router) cooldownGrowth() time.Duration {
	if r == nil || r.store == nil {
		return defaultFailoverCooldownGrowth
	}
	val, err := r.store.GetSetting(failoverCooldownGrowthSetting)
	if err != nil || val == "" {
		return defaultFailoverCooldownGrowth
	}
	seconds, err := strconv.Atoi(val)
	if err != nil || seconds < 0 {
		slog.Warn("invalid_failover_cooldown_growth", "value", val, "error", err)
		return defaultFailoverCooldownGrowth
	}
	return time.Duration(seconds) * time.Second
}

func (r *Router) providerCoolingDown(providerID int) bool {
	if r == nil {
		return false
	}
	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()

	state, ok := r.cooldowns[providerID]
	if !ok || state.until.IsZero() {
		return false
	}
	if !r.currentTime().Before(state.until) {
		state.until = time.Time{}
		r.cooldowns[providerID] = state
		return false
	}
	return true
}

func (r *Router) resetProviderCooldown(providerID int) {
	if r == nil {
		return
	}
	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()
	delete(r.cooldowns, providerID)
}

func (r *Router) markProviderFailure(providerID int, statusCode int) {
	if r == nil || statusCode == http.StatusTooManyRequests {
		return
	}
	growth := r.cooldownGrowth()
	if growth <= 0 {
		return
	}

	r.cooldownMu.Lock()
	defer r.cooldownMu.Unlock()

	state := r.cooldowns[providerID]
	state.failures++
	state.until = r.currentTime().Add(time.Duration(state.failures) * growth)
	r.cooldowns[providerID] = state
}

// Route 执行故障转移路由（非流式）
func (r *Router) Route(ctx context.Context, req *models.UnifiedRequest) (*RouteResult, error) {
	if req.Stream {
		return nil, r.routeStreamToResult(ctx, req)
	}
	return r.routeNonStream(ctx, req)
}

// StreamRouteResult 流式路由结果。
// 当上游是 codex Responses API 时，RawBody 包含原始 SSE 响应体，
// Gateway 应直接透传给客户端，不走 UnifiedStreamEvent 解析-重建。
type StreamRouteResult struct {
	Events       <-chan models.UnifiedStreamEvent
	RawBody      io.ReadCloser // codex Responses API 原始 SSE 响应体
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
	attemptNum := 0
	skippedCooldown := 0
	var lastErr error
	for _, p := range chain {
		type rawStreamer interface {
			RawResponsesStream(ctx context.Context, rawBody []byte) (*http.Response, error)
		}
		if rs, ok := p.(rawStreamer); ok {
			if r.providerCoolingDown(p.ID()) {
				skippedCooldown++
				slog.Info("raw_stream_provider_cooling_down", "provider", p.Name())
				continue
			}

			if _, err := r.refreshOAuthProviderIfNeeded(p, false); err != nil {
				slog.Warn("oauth_preemptive_refresh_failed", "provider", p.Name(), "error", err)
			}
			attemptNum++
			start := time.Now()
			quotaBefore := r.getProviderQuotaJSON(p.ID())
			resp, err := rs.RawResponsesStream(ctx, rawBody)
			duration := time.Since(start)
			if err != nil {
				recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, "", false, attemptNum)
				r.markProviderFailure(p.ID(), statusCodeFromErr(err))
				lastErr = err
				continue
			}
			r.saveQuotaFromHeaders(p.ID(), resp.Header)
			quotaAfter := r.getProviderQuotaJSON(p.ID())
			if resp.StatusCode != 200 {
				body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
				resp.Body.Close()
				upstreamErr := &models.UpstreamError{
					StatusCode: resp.StatusCode,
					Body:       body,
					Err:        fmt.Errorf("upstream returned %d", resp.StatusCode),
				}
				recordAttempt(ctx, p.Name(), resp.StatusCode, upstreamErr, duration, quotaBefore, quotaAfter, false, attemptNum)
				lastErr = upstreamErr
				if isAuthStatus(resp.StatusCode) {
					if refreshed, refreshErr := r.refreshOAuthProviderIfNeeded(p, true); refreshErr == nil && refreshed {
						attemptNum++
						start = time.Now()
						quotaBefore = r.getProviderQuotaJSON(p.ID())
						resp, err = rs.RawResponsesStream(ctx, rawBody)
						duration = time.Since(start)
						if err != nil {
							recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, "", false, attemptNum)
							lastErr = err
							continue
						}
						r.saveQuotaFromHeaders(p.ID(), resp.Header)
						quotaAfter = r.getProviderQuotaJSON(p.ID())
						if resp.StatusCode == 200 {
							recordAttempt(ctx, p.Name(), resp.StatusCode, nil, duration, quotaBefore, quotaAfter, true, attemptNum)
							return &RawStreamResult{
								Body:         resp.Body,
								ProviderName: p.Name(),
								QuotaBefore:  quotaBefore,
								QuotaAfter:   quotaAfter,
							}, nil
						}
						body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
						resp.Body.Close()
						upstreamErr := &models.UpstreamError{
							StatusCode: resp.StatusCode,
							Body:       body,
							Err:        fmt.Errorf("upstream returned %d", resp.StatusCode),
						}
						recordAttempt(ctx, p.Name(), resp.StatusCode, upstreamErr, duration, quotaBefore, quotaAfter, false, attemptNum)
						lastErr = upstreamErr
					}
				}
				continue
			}
			recordAttempt(ctx, p.Name(), resp.StatusCode, nil, duration, quotaBefore, quotaAfter, true, attemptNum)
			return &RawStreamResult{
				Body:         resp.Body,
				ProviderName: p.Name(),
				QuotaBefore:  quotaBefore,
				QuotaAfter:   quotaAfter,
			}, nil
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	if skippedCooldown > 0 {
		return nil, fmt.Errorf("all providers are cooling down")
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
