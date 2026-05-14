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
}

// buildPriorityChain 构建优先级链
// 1. 先查找 model_mappings 中该模型的专属映射
// 2. 若找不到，使用全局默认链
func (r *Router) buildPriorityChain(modelName string) ([]provider.Provider, error) {
	// 尝试按模型查找
	entries, err := r.store.GetPriorityChain(modelName)
	if err == nil && len(entries) > 0 {
		providers := make([]provider.Provider, 0, len(entries))
		for _, e := range entries {
			p, ok := r.registry.Get(e.Provider.Name)
			if ok {
				providers = append(providers, p)
			}
		}
		if len(providers) > 0 {
			return providers, nil
		}
	}

	// 回退到全局默认链
	entries, err = r.store.GetDefaultPriorityChain()
	if err != nil {
		return nil, err
	}

	providers := make([]provider.Provider, 0, len(entries))
	for _, e := range entries {
		p, ok := r.registry.Get(e.Provider.Name)
		if ok {
			providers = append(providers, p)
		}
	}
	return providers, nil
}

// execWithTimeout 带超时执行 Provider 调用
func execWithTimeout(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(ctx)
}

// routeNonStream 非流式路由（全链重试）
func (r *Router) routeNonStream(ctx context.Context, req *models.UnifiedRequest) (*RouteResult, error) {
	chain, err := r.buildPriorityChain(req.Model)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("no available providers for model %q", req.Model)
	}

	var lastErr error
	var attempts []AttemptInfo

	for _, p := range chain {
		start := time.Now()
		resp, err := p.ChatCompletion(ctx, req)
		duration := time.Since(start)

		if err == nil {
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
			}, nil
		}

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
func (r *Router) routeStream(ctx context.Context, req *models.UnifiedRequest) (<-chan models.UnifiedStreamEvent, error) {
	chain, err := r.buildPriorityChain(req.Model)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("no available providers for model %q", req.Model)
	}

	var lastErr error
	// 只在连接建立前重试
	for i, p := range chain {
		events, err := p.ChatCompletionStream(ctx, req)
		if err == nil {
			if i > 0 {
				slog.Info("stream_failover",
					"model", req.Model,
					"used_provider", p.Name(),
					"attempt", i+1,
				)
			}
			return events, nil // 不包裹，不插入标记——保持透明性
		}

		cat := models.ClassifyError(err)
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

	return nil, &FailoverError{LastError: lastErr, Attempts: nil}
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
