package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
)

// AccessLogCollector 线程安全的请求元数据收集器
type AccessLogCollector struct {
	mu             sync.Mutex
	model          string
	providerName   string
	tokensIn       int
	tokensOut      int
	errorMsg       string
	clientBody     string
	clientResponse string
	upstreamReq    string
	upstreamResp   string
	quotaBefore    string
	quotaAfter     string
	attempts       []config.AttemptRecord
}

func (c *AccessLogCollector) SetClientBody(b string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientBody = b
}

func (c *AccessLogCollector) SetClientResponse(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientResponse = r
}

func (c *AccessLogCollector) SetUpstreamReq(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamReq = r
}

func (c *AccessLogCollector) SetUpstreamResp(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamResp = r
}

func (c *AccessLogCollector) SetModel(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = m
}

func (c *AccessLogCollector) SetProvider(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providerName = p
}

func (c *AccessLogCollector) SetTokens(in, out int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokensIn = in
	c.tokensOut = out
}

func (c *AccessLogCollector) SetError(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errorMsg = msg
}

func (c *AccessLogCollector) SetQuota(before, after string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if before == "" && after == "" {
		return
	}
	c.quotaBefore = before
	c.quotaAfter = after
	for i := len(c.attempts) - 1; i >= 0; i-- {
		if !c.attempts[i].Success {
			continue
		}
		if c.attempts[i].QuotaBefore == "" {
			c.attempts[i].QuotaBefore = before
		}
		if c.attempts[i].QuotaAfter == "" {
			c.attempts[i].QuotaAfter = after
		}
		break
	}
}

func (c *AccessLogCollector) RecordAttempt(a config.AttemptRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, a)
	if a.Success && (a.QuotaBefore != "" || a.QuotaAfter != "") {
		c.quotaBefore = a.QuotaBefore
		c.quotaAfter = a.QuotaAfter
	}
}

func quotaFromAttempts(before, after string, attempts []config.AttemptRecord) (string, string) {
	if before != "" || after != "" {
		return before, after
	}
	if b, a, ok := findAttemptQuota(attempts, true); ok {
		return b, a
	}
	if b, a, ok := findAttemptQuota(attempts, false); ok {
		return b, a
	}
	return before, after
}

func findAttemptQuota(attempts []config.AttemptRecord, successOnly bool) (string, string, bool) {
	for i := len(attempts) - 1; i >= 0; i-- {
		if successOnly && !attempts[i].Success {
			continue
		}
		if attempts[i].QuotaBefore == "" && attempts[i].QuotaAfter == "" {
			continue
		}
		return attempts[i].QuotaBefore, attempts[i].QuotaAfter, true
	}
	return "", "", false
}

func (c *AccessLogCollector) Model() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model
}

func (c *AccessLogCollector) ProviderName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.providerName
}

func (c *AccessLogCollector) Tokens() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokensIn, c.tokensOut
}

func (c *AccessLogCollector) ErrorMsg() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errorMsg
}

// withCollector 注入 collector 到 context
func withCollector(ctx context.Context, c *AccessLogCollector) context.Context {
	return context.WithValue(ctx, ctxKeyCollector, c)
}

// collectorFromContext 从 context 提取 collector
func collectorFromContext(ctx context.Context) *AccessLogCollector {
	if c, ok := ctx.Value(ctxKeyCollector).(*AccessLogCollector); ok {
		return c
	}
	return nil
}

func ctxWithAttemptRecorder(ctx context.Context) context.Context {
	c := collectorFromContext(ctx)
	if c == nil {
		return ctx
	}
	return models.WithAttemptRecorder(ctx, func(provider string, statusCode int, errMsg string, durationMs int64, quotaBefore, quotaAfter string, success bool, attemptNum int) {
		c.RecordAttempt(config.AttemptRecord{
			Provider:    provider,
			StatusCode:  statusCode,
			Error:       errMsg,
			DurationMs:  durationMs,
			QuotaBefore: quotaBefore,
			QuotaAfter:  quotaAfter,
			Success:     success,
			AttemptNum:  attemptNum,
		})
	})
}

// accessLogWriter buffered channel + 后台单 goroutine 串行写入 Pebble（batch）
type accessLogWriter struct {
	logStore   *config.LogStore
	ch         chan config.AccessLog
	wg         sync.WaitGroup
	batchCount int64
}

func newAccessLogWriter(logStore *config.LogStore, bufSize int) *accessLogWriter {
	w := &accessLogWriter{
		logStore: logStore,
		ch:       make(chan config.AccessLog, bufSize),
	}
	w.wg.Add(1)
	go w.run()
	return w
}

func (w *accessLogWriter) run() {
	defer w.wg.Done()

	var batch *pebble.Batch
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	flush := func() {
		if batch == nil || batch.Count() == 0 {
			return
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			slog.Error("pebble_batch_commit_failed", "err", err)
		}
		batch.Close()
		batch = nil
		w.logStore.AddTotal(w.batchCount)
		w.batchCount = 0
	}

	for {
		select {
		case logEntry, ok := <-w.ch:
			if !ok {
				flush()
				return
			}
			if batch == nil {
				batch = w.logStore.DB().NewBatch()
			}

			id := w.logStore.NextID()
			logEntry.ID = int(id)

			if logEntry.Timestamp <= 0 {
				logEntry.Timestamp = time.Now().UnixMilli()
			}
			tsNano := uint64(time.UnixMilli(logEntry.Timestamp).UnixNano())

			jsonData, err := json.Marshal(&logEntry)
			if err != nil {
				slog.Error("access_log_marshal_failed", "err", err)
				continue
			}

			_ = batch.Set(config.EncodePrimaryKey(tsNano, id), jsonData, nil)
			_ = batch.Set(config.EncodeIndexKey(id), config.EncodeTimestampValue(tsNano), nil)
			if logEntry.Model != "" {
				_ = batch.Set(config.EncodeModelIndexKey(logEntry.Model, tsNano, id), nil, nil)
			}
			w.batchCount++

			if batch.Count() >= 50 {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

func (w *accessLogWriter) Shutdown(timeout time.Duration) {
	close(w.ch)
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		slog.Warn("access_log_writer_shutdown_timeout")
	}
}

// responseRecorder 包装 http.ResponseWriter，捕获 status code 和响应体
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	body        []byte
}

func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.statusCode = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.statusCode = http.StatusOK
		r.wroteHeader = true
	}
	r.body = append(r.body, b...)
	return r.ResponseWriter.Write(b)
}

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *responseRecorder) StatusCode() int {
	if !r.wroteHeader {
		return http.StatusOK
	}
	return r.statusCode
}

func (r *responseRecorder) Body() string {
	return string(r.body)
}

// accessLogMiddleware 记录所有 AI API 请求的访问日志（/v1/models 除外）
func (g *Gateway) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()

		collector := &AccessLogCollector{}
		r = r.WithContext(withCollector(r.Context(), collector))

		rec := &responseRecorder{ResponseWriter: w}

		next.ServeHTTP(rec, r)

		apiKey := apiKeyFromContext(r.Context())
		var keyID *int
		var keyName string
		if apiKey != nil {
			kid := apiKey.ID
			keyID = &kid
			keyName = apiKey.Name
		}

		inTokens, outTokens := collector.Tokens()
		var clientBody, clientResp, upstreamReq, upstreamResp string
		var quotaBefore, quotaAfter string
		var attemptsCopy []config.AttemptRecord
		func() {
			collector.mu.Lock()
			defer collector.mu.Unlock()
			clientBody = collector.clientBody
			clientResp = collector.clientResponse
			upstreamReq = collector.upstreamReq
			upstreamResp = collector.upstreamResp
			quotaBefore = collector.quotaBefore
			quotaAfter = collector.quotaAfter
			attemptsCopy = collector.attempts
		}()
		quotaBefore, quotaAfter = quotaFromAttempts(quotaBefore, quotaAfter, attemptsCopy)

		var attemptsJSON string
		if len(attemptsCopy) > 0 {
			b, _ := json.Marshal(attemptsCopy)
			attemptsJSON = string(b)
		}

		// 如果 handler 未显式设置 clientResponse，使用 recorder 捕获的响应体
		if clientResp == "" && rec.StatusCode() < 300 {
			clientResp = rec.Body()
		}

		log := config.AccessLog{
			Timestamp:    start.UnixMilli(),
			ApiKeyID:     keyID,
			ApiKeyName:   keyName,
			Method:       r.Method,
			Path:         r.URL.Path,
			Model:        collector.Model(),
			StatusCode:   rec.StatusCode(),
			TokensIn:     inTokens,
			TokensOut:    outTokens,
			DurationMs:   int(time.Since(start).Milliseconds()),
			RemoteIP:     r.RemoteAddr,
			RequestID:    chimw.GetReqID(r.Context()),
			ProviderName: collector.ProviderName(),
			ErrorMsg:     collector.ErrorMsg(),
			ClientReq:    clientBody,
			ClientResp:   clientResp,
			UpstreamReq:  upstreamReq,
			UpstreamResp: upstreamResp,
			QuotaBefore:  quotaBefore,
			QuotaAfter:   quotaAfter,
			AttemptsJSON: attemptsJSON,
		}

		select {
		case g.accessLogWriter.ch <- log:
		case <-time.After(1 * time.Millisecond):
			slog.Warn("access_log_channel_full", "path", r.URL.Path)
		}
	})
}
