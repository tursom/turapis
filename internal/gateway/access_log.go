package gateway

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/config"
)

// AccessLogCollector 线程安全的请求元数据收集器
type AccessLogCollector struct {
	mu              sync.Mutex
	model           string
	providerName    string
	tokensIn        int
	tokensOut       int
	errorMsg        string
	clientBody      string
	clientResponse  string
	upstreamReq     string
	upstreamResp    string
}

func (c *AccessLogCollector) SetClientBody(b string) {
	c.mu.Lock()
	c.clientBody = b
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetClientResponse(r string) {
	c.mu.Lock()
	c.clientResponse = r
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetUpstreamReq(r string) {
	c.mu.Lock()
	c.upstreamReq = r
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetUpstreamResp(r string) {
	c.mu.Lock()
	c.upstreamResp = r
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetModel(m string) {
	c.mu.Lock()
	c.model = m
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetProvider(p string) {
	c.mu.Lock()
	c.providerName = p
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetTokens(in, out int) {
	c.mu.Lock()
	c.tokensIn = in
	c.tokensOut = out
	c.mu.Unlock()
}

func (c *AccessLogCollector) SetError(msg string) {
	c.mu.Lock()
	c.errorMsg = msg
	c.mu.Unlock()
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

// accessLogWriter buffered channel + 后台单 goroutine 串行写入 SQLite
type accessLogWriter struct {
	store *config.Store
	ch    chan config.AccessLog
	wg    sync.WaitGroup
}

func newAccessLogWriter(store *config.Store, bufSize int) *accessLogWriter {
	w := &accessLogWriter{
		store: store,
		ch:    make(chan config.AccessLog, bufSize),
	}
	w.wg.Add(1)
	go w.run()
	return w
}

func (w *accessLogWriter) run() {
	defer w.wg.Done()
	for log := range w.ch {
		if err := w.store.InsertAccessLog(&log); err != nil {
			slog.Error("access_log_insert_failed", "err", err, "key", log.ApiKeyName, "model", log.Model)
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
		collector.mu.Lock()
		clientBody := collector.clientBody
		clientResp := collector.clientResponse
		upstreamReq := collector.upstreamReq
		upstreamResp := collector.upstreamResp
		collector.mu.Unlock()

		// 如果 handler 未显式设置 clientResponse，使用 recorder 捕获的响应体
		if clientResp == "" && rec.StatusCode() < 300 {
			clientResp = rec.Body()
		}

		log := config.AccessLog{
			Timestamp:    start.UTC().Format(time.RFC3339),
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
		}

		select {
		case g.accessLogWriter.ch <- log:
		case <-time.After(1 * time.Millisecond):
			slog.Warn("access_log_channel_full", "path", r.URL.Path)
		}
	})
}
