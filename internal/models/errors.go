package models

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

// ErrorCategory 错误分类
type ErrorCategory string

const (
	CategoryTimeout          ErrorCategory = "timeout"
	CategoryRateLimit        ErrorCategory = "rate_limit"
	CategoryQuotaExhausted   ErrorCategory = "quota_exhausted"
	CategoryServerError      ErrorCategory = "server_error"
	CategoryEmptyResponse    ErrorCategory = "empty_response"
	CategoryFormatError      ErrorCategory = "format_error"
	CategoryModelUnavailable ErrorCategory = "model_unavailable"
	CategoryAuthError        ErrorCategory = "auth_error"
	CategoryUnknown          ErrorCategory = "unknown"
)

// UpstreamError 上游错误，包含 HTTP 状态码和响应体
type UpstreamError struct {
	StatusCode int
	Body       []byte
	Err        error
}

func (e *UpstreamError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "upstream error"
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// ErrUnsupportedFeature 不支持的高级特性
var ErrUnsupportedFeature = errors.New("unsupported feature: tool_use/multimodal/thinking not supported in v1")

// ClassifyError 将上游错误分类为 ErrorCategory
func ClassifyError(err error) ErrorCategory {
	if err == nil {
		return CategoryUnknown
	}

	var ue *UpstreamError
	if errors.As(err, &ue) {
		return classifyByStatus(ue.StatusCode, ue.Body)
	}

	if isTimeout(err) {
		return CategoryTimeout
	}

	msg := err.Error()
	if msg == "" {
		return CategoryEmptyResponse
	}

	return CategoryUnknown
}

func classifyByStatus(statusCode int, body []byte) ErrorCategory {
	switch {
	case statusCode == 429:
		if bytes.Contains(bytes.ToLower(body), []byte("quota")) ||
			bytes.Contains(bytes.ToLower(body), []byte("exhausted")) ||
			bytes.Contains(bytes.ToLower(body), []byte("insufficient")) {
			return CategoryQuotaExhausted
		}
		return CategoryRateLimit
	case statusCode == 401 || statusCode == 403:
		return CategoryAuthError
	case statusCode >= 500:
		return CategoryServerError
	case statusCode == 404:
		lower := bytes.ToLower(body)
		if bytes.Contains(lower, []byte("model")) &&
			(bytes.Contains(lower, []byte("not found")) || bytes.Contains(lower, []byte("not supported"))) {
			return CategoryModelUnavailable
		}
		return CategoryUnknown
	default:
		return CategoryUnknown
	}
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return true
		}
		if errors.Is(urlErr.Err, context.Canceled) {
			return true
		}
	}

	msg := err.Error()
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "context deadline exceeded")
}

// ShouldFailover 判断该错误是否应触发故障转移
func ShouldFailover(cat ErrorCategory) bool {
	switch cat {
	case CategoryTimeout,
		CategoryRateLimit,
		CategoryQuotaExhausted,
		CategoryServerError,
		CategoryEmptyResponse,
		CategoryFormatError,
		CategoryModelUnavailable,
		CategoryAuthError,
		CategoryUnknown:
		return true
	default:
		return true
	}
}
