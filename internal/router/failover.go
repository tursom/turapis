package router

import (
	"context"
	"errors"
	"fmt"
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

	for i, p := range chain {
		start := time.Now()
		quotaBefore := r.getProviderQuotaJSON(p.ID())
		resp, err := p.ChatCompletion(ctx, req)
		duration := time.Since(start)
		r.saveQuotaFromProvider(p)
		quotaAfter := r.getProviderQuotaJSON(p.ID())

		if err == nil {
			recordAttempt(ctx, p.Name(), 200, nil, duration, quotaBefore, quotaAfter, true, i+1)
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

		recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, quotaAfter, false, i+1)

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

		if !models.ShouldFailover(cat) {
			return nil, err // auth error — 不重试
		}

		lastErr = err
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
	for i, p := range chain {
		start := time.Now()
		quotaBefore := r.getProviderQuotaJSON(p.ID())
		events, err := p.ChatCompletionStream(ctx, req)
		duration := time.Since(start)
		r.saveQuotaFromProvider(p)
		quotaAfter := r.getProviderQuotaJSON(p.ID())
		if err == nil {
			recordAttempt(ctx, p.Name(), 200, nil, duration, quotaBefore, quotaAfter, true, i+1)
			if i > 0 {
				slog.Info("stream_failover",
					"model", req.Model,
					"used_provider", p.Name(),
					"attempt", i+1,
				)
			}
			return &StreamRouteResult{
				Events:       events,
				ProviderName: p.Name(),
				QuotaBefore:  quotaBefore,
				QuotaAfter:   quotaAfter,
			}, nil
		}

		recordAttempt(ctx, p.Name(), 0, err, duration, quotaBefore, quotaAfter, false, i+1)

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

		lastErr = err
		if !models.ShouldFailover(cat) {
			return nil, err
		}
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
