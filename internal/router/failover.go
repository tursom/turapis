package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/tursom/turapis/internal/models"
	"github.com/tursom/turapis/internal/provider"
)

// FailoverError 故障转移失败错误，包含所有尝试的记录
type FailoverError struct {
	LastError error
	Attempts  []AttemptInfo
}

func (e *FailoverError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "all %d providers failed", len(e.Attempts))
	for _, a := range e.Attempts {
		fmt.Fprintf(&sb, "; [%s] %v (%.0fms)", a.Provider, a.Error, a.Duration.Seconds()*1000)
	}
	return sb.String()
}

func (e *FailoverError) Unwrap() error { return e.LastError }

// AttemptInfo 单次尝试记录
type AttemptInfo struct {
	Provider string
	Error    error
	Duration time.Duration
}

// RouteResult 路由结果
type RouteResult struct {
	Response     *models.UnifiedResponse
	UsedProvider string
	Attempts     []AttemptInfo
	QuotaBefore  string
	QuotaAfter   string
}

// buildPriorityChain 构建优先级链
func (r *Router) buildPriorityChain(ctx context.Context, modelName string) ([]provider.Provider, error) {
	seen := map[string]bool{}
	var providers []provider.Provider
	perms := models.KeyPermissionsFromContext(ctx)

	entries, err := r.store.GetPriorityChain(modelName)
	if err == nil {
		for _, e := range entries {
			p, ok := r.registry.Get(e.ProviderID)
			if ok && !seen[p.Name()] && perms.ProviderAllowed(p.Name()) {
				providers = append(providers, p)
				seen[p.Name()] = true
			}
		}
	}

	if len(providers) == 0 {
		defaultEntries, err := r.store.GetDefaultPriorityChain()
		if err == nil {
			for _, e := range defaultEntries {
				p, ok := r.registry.Get(e.ProviderID)
				if ok && !seen[p.Name()] && perms.ProviderAllowed(p.Name()) {
					providers = append(providers, p)
					seen[p.Name()] = true
				}
			}
		}
	}

	return providers, nil
}

func (r *Router) routeNonStream(ctx context.Context, req *models.UnifiedRequest) (*RouteResult, error) {
	chain, err := r.buildPriorityChain(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("no available providers for model %q", req.Model)
	}

	var lastErr error
	var attempts []AttemptInfo
	attemptNum := 0
	skippedCooldown := 0

	callProvider := func(p provider.Provider) (*models.UnifiedResponse, error, time.Duration, string, string, int) {
		attemptNum++
		start := time.Now()
		quotaBefore := r.getProviderQuotaJSON(p.ID())
		resp, err := p.ChatCompletion(ctx, req)
		duration := time.Since(start)
		r.saveQuotaFromProvider(p)
		quotaAfter := r.getProviderQuotaJSON(p.ID())
		return resp, err, duration, quotaBefore, quotaAfter, attemptNum
	}

	for _, p := range chain {
		if r.providerCoolingDown(p.ID()) {
			skippedCooldown++
			slog.Info("provider_cooling_down", "provider", p.Name())
			continue
		}

		if _, err := r.refreshOAuthProviderIfNeeded(p, false); err != nil {
			slog.Warn("oauth_preemptive_refresh_failed", "provider", p.Name(), "error", err)
		}

		resp, err, duration, quotaBefore, quotaAfter, n := callProvider(p)

		if err == nil {
			recordAttempt(ctx, p.Name(), 200, nil, duration, quotaBefore, quotaAfter, true, n)
			r.resetProviderCooldown(p.ID())
			slog.Info("route_success",
				"model", req.Model,
				"used_provider", p.Name(),
				"attempts", len(attempts)+1,
				"duration_ms", duration.Milliseconds(),
			)
			return &RouteResult{
				Response:     resp,
				UsedProvider: p.Name(),
				Attempts:     attempts,
				QuotaBefore:  quotaBefore,
				QuotaAfter:   quotaAfter,
			}, nil
		}

		recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, quotaAfter, false, n)

		cat := models.ClassifyError(err)
		attempts = append(attempts, AttemptInfo{
			Provider: p.Name(),
			Error:    err,
			Duration: duration,
		})

		slog.Warn("provider_failed",
			"provider", p.Name(),
			"error_category", string(cat),
			"error", formatError(err),
		)

		if models.IsAuthError(err) {
			if refreshed, refreshErr := r.refreshOAuthProviderIfNeeded(p, true); refreshErr != nil {
				slog.Warn("oauth_refresh_after_auth_error_failed", "provider", p.Name(), "error", refreshErr)
			} else if refreshed {
				resp, retryErr, retryDuration, retryQuotaBefore, retryQuotaAfter, retryN := callProvider(p)
				if retryErr == nil {
					recordAttempt(ctx, p.Name(), 200, nil, retryDuration, retryQuotaBefore, retryQuotaAfter, true, retryN)
					r.resetProviderCooldown(p.ID())
					slog.Info("route_success_after_oauth_refresh",
						"model", req.Model,
						"used_provider", p.Name(),
						"attempts", len(attempts)+1,
						"duration_ms", retryDuration.Milliseconds(),
					)
					return &RouteResult{
						Response:     resp,
						UsedProvider: p.Name(),
						Attempts:     attempts,
						QuotaBefore:  retryQuotaBefore,
						QuotaAfter:   retryQuotaAfter,
					}, nil
				}
				recordAttempt(ctx, p.Name(), 0, retryErr, retryDuration, retryQuotaBefore, retryQuotaAfter, false, retryN)
				retryCat := models.ClassifyError(retryErr)
				attempts = append(attempts, AttemptInfo{
					Provider: p.Name(),
					Error:    retryErr,
					Duration: retryDuration,
				})
				slog.Warn("provider_failed_after_oauth_refresh",
					"provider", p.Name(),
					"error_category", string(retryCat),
					"error", formatError(retryErr),
				)
				cat = retryCat
				err = retryErr
			}
		}

		if !models.ShouldFailover(cat) {
			r.markProviderFailure(p.ID(), statusCodeFromErr(err))
			return nil, err // auth error — 不重试
		}

		lastErr = err
		r.markProviderFailure(p.ID(), statusCodeFromErr(err))
	}

	if lastErr == nil && skippedCooldown > 0 {
		return nil, fmt.Errorf("all providers are cooling down")
	}

	ferr := &FailoverError{LastError: lastErr, Attempts: attempts}
	slog.Error("all_providers_failed",
		"model", req.Model,
		"attempts", len(attempts),
		"error", formatError(lastErr),
	)
	return nil, ferr
}

// routeStream 流式路由（连接建立前重试，数据发送后不再重试）
func (r *Router) routeStream(ctx context.Context, req *models.UnifiedRequest) (*StreamRouteResult, error) {
	chain, err := r.buildPriorityChain(ctx, req.Model)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("no available providers for model %q", req.Model)
	}

	var lastErr error
	var attempts []AttemptInfo
	attemptNum := 0
	skippedCooldown := 0

	callProvider := func(p provider.Provider) (<-chan models.UnifiedStreamEvent, error, time.Duration, string, string, int) {
		attemptNum++
		start := time.Now()
		quotaBefore := r.getProviderQuotaJSON(p.ID())
		events, err := p.ChatCompletionStream(ctx, req)
		duration := time.Since(start)
		r.saveQuotaFromProvider(p)
		quotaAfter := r.getProviderQuotaJSON(p.ID())
		return events, err, duration, quotaBefore, quotaAfter, attemptNum
	}

	for _, p := range chain {
		if r.providerCoolingDown(p.ID()) {
			skippedCooldown++
			slog.Info("stream_provider_cooling_down", "provider", p.Name())
			continue
		}

		if _, err := r.refreshOAuthProviderIfNeeded(p, false); err != nil {
			slog.Warn("oauth_preemptive_refresh_failed", "provider", p.Name(), "error", err)
		}

		events, err, duration, quotaBefore, quotaAfter, n := callProvider(p)
		if err == nil {
			recordAttempt(ctx, p.Name(), 200, nil, duration, quotaBefore, quotaAfter, true, n)
			r.resetProviderCooldown(p.ID())
			if n > 1 {
				slog.Info("stream_failover",
					"model", req.Model,
					"used_provider", p.Name(),
					"attempt", n,
				)
			}
			result := &StreamRouteResult{
				Events:       events,
				ProviderName: p.Name(),
				QuotaBefore:  quotaBefore,
				QuotaAfter:   quotaAfter,
			}
			// 若 provider 实现了 TakeRawBody（codex Responses API 原始 SSE 透传），
			// 取出 raw body 填入结果，Gateway 会直接 pipe 给客户端。
			type rawBodyProvider interface {
				TakeRawBody() io.ReadCloser
			}
			if rbp, ok := p.(rawBodyProvider); ok {
				result.RawBody = rbp.TakeRawBody()
			}
			return result, nil
		}

		recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, quotaAfter, false, n)

		cat := models.ClassifyError(err)
		attempts = append(attempts, AttemptInfo{
			Provider: p.Name(),
			Error:    err,
			Duration: duration,
		})
		slog.Warn("stream_connect_failed",
			"provider", p.Name(),
			"error_category", string(cat),
			"error", formatError(err),
		)

		if models.IsAuthError(err) {
			if refreshed, refreshErr := r.refreshOAuthProviderIfNeeded(p, true); refreshErr != nil {
				slog.Warn("oauth_refresh_after_stream_auth_error_failed", "provider", p.Name(), "error", refreshErr)
			} else if refreshed {
				events, retryErr, retryDuration, retryQuotaBefore, retryQuotaAfter, retryN := callProvider(p)
				if retryErr == nil {
					recordAttempt(ctx, p.Name(), 200, nil, retryDuration, retryQuotaBefore, retryQuotaAfter, true, retryN)
					r.resetProviderCooldown(p.ID())
					result := &StreamRouteResult{
						Events:       events,
						ProviderName: p.Name(),
						QuotaBefore:  retryQuotaBefore,
						QuotaAfter:   retryQuotaAfter,
					}
					type rawBodyProvider interface {
						TakeRawBody() io.ReadCloser
					}
					if rbp, ok := p.(rawBodyProvider); ok {
						result.RawBody = rbp.TakeRawBody()
					}
					return result, nil
				}
				recordAttempt(ctx, p.Name(), 0, retryErr, retryDuration, retryQuotaBefore, retryQuotaAfter, false, retryN)
				retryCat := models.ClassifyError(retryErr)
				attempts = append(attempts, AttemptInfo{
					Provider: p.Name(),
					Error:    retryErr,
					Duration: retryDuration,
				})
				slog.Warn("stream_connect_failed_after_oauth_refresh",
					"provider", p.Name(),
					"error_category", string(retryCat),
					"error", formatError(retryErr),
				)
				cat = retryCat
				err = retryErr
			}
		}

		lastErr = err
		if !models.ShouldFailover(cat) {
			r.markProviderFailure(p.ID(), statusCodeFromErr(err))
			return nil, err
		}
		r.markProviderFailure(p.ID(), statusCodeFromErr(err))
	}

	if lastErr == nil && skippedCooldown > 0 {
		return nil, fmt.Errorf("all providers are cooling down")
	}

	return nil, &FailoverError{LastError: lastErr, Attempts: attempts}
}

// formatError 提取完整错误信息，包含上游响应体
func formatError(err error) string {
	if err == nil {
		return ""
	}
	var parts []string
	parts = append(parts, err.Error())

	var ue *models.UpstreamError
	if errors.As(err, &ue) {
		parts = append(parts, fmt.Sprintf("status=%d", ue.StatusCode))
		if len(ue.Body) > 0 {
			body := string(ue.Body)
			if len(body) > 1000 {
				body = body[:1000] + "..."
			}
			parts = append(parts, "body="+body)
		}
	}
	return strings.Join(parts, " | ")
}

func statusCodeFromErr(err error) int {
	var ue *models.UpstreamError
	if errors.As(err, &ue) {
		return ue.StatusCode
	}
	return 0
}

func recordAttempt(ctx context.Context, providerName string, statusCode int, err error, duration time.Duration, quotaBefore, quotaAfter string, success bool, attemptNum int) {
	rec := models.AttemptRecorderFromContext(ctx)
	if rec == nil {
		return
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
		if statusCode == 0 {
			statusCode = statusCodeFromErr(err)
		}
	}
	rec(providerName, statusCode, errMsg, duration.Milliseconds(), quotaBefore, quotaAfter, success, attemptNum)
}
