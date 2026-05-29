// 访问日志中间件管道（access log middleware pipeline）
//
// 完整数据流：
//
//	HTTP 请求 → accessLogMiddleware（主入口, 每个请求创建新的 collector）
//	                          │
//	          ┌───────────────┼───────────────────┐
//	          ▼               ▼                   ▼
//	 responseRecorder  AccessLogCollector      路由/处理器
//	 （捕获状态码+响应体） （线程安全收集器）   （通过 context 注入 setter 调用）
//	                          │
//	          ┌───────────────┼───────────────────┐
//	          ▼               ▼                   ▼
//	 messages.go       responses.go          router.go
//	 （常规响应调用）  （流式响应调用）    （ctxWithAttemptRecorder 触发 failover 记录）
//	                          │
//	                          ▼
//	 accessLogMiddleware（请求结束时一次性收集所有字段）
//	                          │
//	                          ▼
//	 accessLogWriter.ch（buffered channel, 256 容量）
//	                          │
//	                          ▼
//	 accessLogWriter.run()（单 goroutine 串行消费）
//	                          │
//	                          ▼
//	 Pebble Batch Commit（50 条或 50ms 触发 flush, pebble.NoSync 写入）
//
// 关键设计决策：
//   - AccessLogCollector 使用 sync.Mutex 而非 channel：
//     多个 goroutine 可能并发写入（handler goroutine, streaming goroutine, failover goroutine），
//     但每次写入只是赋值简单字段（string/int），锁持有时间极短，用 channel 反而增加复杂性
//   - accessLogWriter 采用单 goroutine 串行化所有 Pebble 写入：
//     避免多 goroutine 竞争 Pebble 内部锁，同时保证写入顺序与请求完成顺序一致
//   - 批量提交（batch commit, 50 条或 50ms 触发 flush）：
//     摊销 Pebble 写入放大开销（每次 commit 产生 WAL 写入 + SST 文件整理）
//   - pebble.NoSync：不等待 fsync，数据安全由 Pebble 的 WAL（预写日志）保证；
//     极端崩溃场景可能丢失最后一批未 flush 的数据，但访问日志允许这种级别的丢失
//   - 非阻塞 channel 发送（1ms timeout）：
//     防止慢写入阻塞 HTTP 响应；日志丢弃时只打 warn 不损失主流程
package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/tursom/turapis/internal/config"
	"github.com/tursom/turapis/internal/models"
)

const accessLogSaveBodiesSetting = "access_log_save_bodies"

// AccessLogCollector 线程安全的请求元数据收集器
//
// 为什么需要线程安全（sync.Mutex）：
// 一个请求的处理过程中，可能有多个 goroutine 并发写入 Collector：
//   - handler goroutine：调用 SetModel, SetProvider, SetTokens, SetClientBody 等
//     （在 responses.go 和 messages.go 中，处理完成后立即调用）
//   - streaming goroutine：流式响应中异步调用 SetTokens, SetClientResponse, SetError
//     （在 responses.go 的 SSE 事件循环 goroutine 中）
//   - failover goroutine：router 在多 provider 重试时，通过 RecordAttempt 记录每次尝试
//     （在 router/failover.go 中，每次 provider 尝试完成时调用）
//
// 生命周期：每个 HTTP 请求创建一个新的 Collector 实例，请求结束后由中间件读取并丢弃。
// Collector 绝不跨请求复用，因此无需 Reset 方法。
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

// --- Setter 方法 ---
// 以下 setter 方法由 responses.go 和 messages.go 中的 handler 在请求处理过程中调用。
// 每个 setter 都加锁保护，因为多个 goroutine 可能并发写入。

// SetClientBody 设置客户端原始请求体（JSON 字符串）。
// 由 responses.go/HandleNonStreaming 和 messages.go/HandleNonStreaming 在处理开始时调用，
// 用于记录用户发送给网关的原始 prompt/messages。
func (c *AccessLogCollector) SetClientBody(b string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientBody = b
}

// SetClientResponse 设置网关返回给客户端的响应体。
// 由 responses.go/HandleStreaming 在流式响应中通过 setClientResponseFromStream 设置。
// 注意：如果 handler 未调用此方法（如非流式场景），中间件会回退到 responseRecorder 捕获的 body。
func (c *AccessLogCollector) SetClientResponse(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.clientResponse = r
}

// SetUpstreamReq 设置发送给 upstream provider 的实际请求体。
// 当前代码中暂未使用，预留用于记录经统一格式转换后的请求内容。
func (c *AccessLogCollector) SetUpstreamReq(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamReq = r
}

// SetUpstreamResp 设置 upstream provider 返回的原始响应体。
// 由 responses.go 在非流式响应完成后、流式响应时在 HandleStreaming 中调用。
// 用于调试和审计 provider 返回的完整内容。
func (c *AccessLogCollector) SetUpstreamResp(r string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.upstreamResp = r
}

// SetModel 设置请求使用的模型名称（统一格式，如 "claude-sonnet-4-20250514"）。
// 由 responses.go 和 messages.go 在处理开始时从 router 返回的 unified.Model 设置。
func (c *AccessLogCollector) SetModel(m string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.model = m
}

// SetProvider 设置最终使用的 provider 名称。
// 由 responses.go 和 messages.go 在处理完成后从 router 返回的 result.UsedProvider 设置。
// 对于流式响应，在 HandleStreaming 中从 streamResult.ProviderName 设置。
func (c *AccessLogCollector) SetProvider(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.providerName = p
}

// SetTokens 设置输入和输出的 token 数量。
// 由 responses.go 和 messages.go 在处理完成后从 result.Response.Usage 设置。
// 对于流式响应，在 HandleStreaming 中从 streamResult 设置（最终 usage 事件触发）。
func (c *AccessLogCollector) SetTokens(in, out int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tokensIn = in
	c.tokensOut = out
}

// SetError 设置错误信息。
// 由 responses.go 和 messages.go 在处理失败时调用（router.Route 返回 err 时）。
func (c *AccessLogCollector) SetError(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.errorMsg = msg
}

// SetQuota 设置请求前后的 quota 信息（令牌使用量/限额）。
//
// Quota 回填逻辑（backfill）：
// 当 handler 通过此方法设置 quota 后，会遍历 attempts 列表从后往前查找
// 最后一个成功的尝试记录，并将其 quota 字段补全（如果为空）。
// 这样做的原因是：failover 尝试记录的 quota 信息可能在尝试完成时还未获取到，
// handler 层面获取到最终 quota 后再回填，保证 attempts 日志的完整性。
// 回填采用尾搜+break 策略，只补最后一次成功尝试，因为那是最终生效的 quota。
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

// RecordAttempt 记录一次 provider 尝试（包括成功和失败的 failover 尝试）。
//
// 调用路径：router/failover.go 的 recordAttempt() → models.AttemptRecorderFromContext(ctx)
// → ctxWithAttemptRecorder 注册的回调 → 此方法。
//
// Quota 传播逻辑：
// 如果尝试成功且携带了 quota 信息，则将其升级为 collector 级别的 quota。
// 这样做的原因是：failover 场景中 handler 可能并未显式调用 SetQuota，
// 但成功尝试自己携带了 quota 返回，直接提升到 collector 级别避免 quota 丢失。
func (c *AccessLogCollector) RecordAttempt(a config.AttemptRecord) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.attempts = append(c.attempts, a)
	if a.Success && (a.QuotaBefore != "" || a.QuotaAfter != "") {
		c.quotaBefore = a.QuotaBefore
		c.quotaAfter = a.QuotaAfter
	}
}

// quotaFromAttempts 配额回退解析（fallback hierarchy）：
//
// 优先级（按顺序）：
//  1. 显式设置在 collector 上的 quota（before/after 参数）—— handler 直接调用 SetQuota 设置
//  2. 最后一个成功的 attempt 的 quota —— 最终生效的那次尝试
//  3. 任意 attempt 的 quota —— 兜底，只要有任意尝试记录了配额就用
//  4. 返回空字符串 —— 没有任何配额信息
//
// 这样设计的原因是：
//   - handler 可能未调用 SetQuota（如早期返回错误）
//   - 最后一个成功的尝试可能也未携带 quota（provider 未返回 quota header）
//   - 但某些中间尝试可能携带了 quota 信息，仍可用于日志分析
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

// findAttemptQuota 从 attempts 列表中查找配额信息。
//
// 查找策略：从后往前遍历（最新尝试优先），因为越靠后的尝试越接近最终结果。
//
// successOnly 参数：
//   - true：只查找成功的尝试（第 2 级 fallback，最后成功的尝试）
//   - false：查找任意尝试（第 3 级 fallback，最后的兜底）
//
// 返回第一个同时有 QuotaBefore 和 QuotaAfter 的尝试。
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

// --- Getter 方法 ---
// 以下 getter 方法由中间件在请求结束时调用，用于组装最终的 AccessLog。
// 每个 getter 也加锁保护，与 setter 共享同一把 mu。

// Model 返回请求使用的模型名称。
func (c *AccessLogCollector) Model() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.model
}

// ProviderName 返回最终使用的 provider 名称。
func (c *AccessLogCollector) ProviderName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.providerName
}

// Tokens 返回输入和输出的 token 数量。
func (c *AccessLogCollector) Tokens() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.tokensIn, c.tokensOut
}

// ErrorMsg 返回错误信息（空字符串表示无错误）。
func (c *AccessLogCollector) ErrorMsg() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.errorMsg
}

// --- Context 注入/提取 ---
// Collector 通过 context.Context 在中间件栈中传递。
// 流程：accessLogMiddleware 用 withCollector 注入 → handler 用 collectorFromContext 提取 →
// handler 调用 setter → 中间件结束时用 getter 收集所有字段。

// withCollector 将 AccessLogCollector 注入 context。
// ctxKeyCollector 定义在 auth.go 中，是一个不可导出的 ctxKey 类型，防止外部包冲突。
func withCollector(ctx context.Context, c *AccessLogCollector) context.Context {
	return context.WithValue(ctx, ctxKeyCollector, c)
}

// collectorFromContext 从 context 提取 AccessLogCollector。
// 如果 context 中不存在 collector（如 /v1/models 路径被 bypass），返回 nil。
// 所有 handler 调用此函数前都应检查 nil。
func collectorFromContext(ctx context.Context) *AccessLogCollector {
	if c, ok := ctx.Value(ctxKeyCollector).(*AccessLogCollector); ok {
		return c
	}
	return nil
}

// ctxWithAttemptRecorder 将 collector 的 RecordAttempt 方法注册为 models.AttemptRecorder。
//
// 调用位置：router.go 中在调用 r.Route 之前，将 collector 注入 router 的 context 链。
// 路由层（router/failover.go）在处理每次 provider 尝试时，从 context 提取 AttemptRecorder
// 并调用它来记录尝试信息。如果在 /v1/models bypass 路径上（collector 为 nil），跳过注册。
//
// 闭包捕获 c（collector），将路由层的参数（provider, statusCode 等）转换为
// config.AttemptRecord 后写入 collector。
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
//
// 架构设计理由：
//
// 【为什么用单 goroutine 而非每请求直接写 Pebble？】
// Pebble（以及大多数 LSM-tree 存储引擎）的 batch 写入操作内部有锁竞争。
// 如果每个请求完成后都直接调用 Pebble Batch.Commit，高并发下会产生严重的
// goroutine 调度和锁竞争开销。单 goroutine 串行化所有写入后：
//   - 消除了锁竞争（只有一个 writer）
//   - 写入顺序与请求完成顺序一致（channel FIFO）
//   - 可以将多条日志合并到一个 Pebble Batch 中（batch commit），减少写入放大
//
// 【为什么用 batch commit（50 条或 50ms）？】
// Pebble 每次 Commit 都会产生 WAL 写入和潜在的 SST 文件整理开销。
// 如果每条日志都单独 Commit，相当于每条日志都触发一次完整的写入流程。
// 批量提交将多条日志合并为一个 Batch，一次 Commit 写入所有日志，
// 显著摊销了写入放大系数（write amplification）。
// 50 条阈值和 50ms 时间窗的权衡：
//   - 50 条太少 → 批量收益不够；太多 → 延迟过高、内存占用大
//   - 50ms 太短 → 批量机会少；太长 → 日志延迟可感知
//
// 【pebble.NoSync 的性能含义】
// NoSync 表示 Commit 时不等待操作系统 fsync 完成。这意味着：
//   - 写入先进入 OS page cache，异步刷盘
//   - 性能极高（避免了磁盘 I/O 的同步等待）
//   - 安全边界：进程 crash 时可能丢失 page cache 中未刷盘的数据
//   - 可接受性：访问日志不是交易数据，允许极端情况下的日志丢失
//   - Pebble 的 WAL 仍然保证写入的原子性（要么全写，要么全不写）
//
// 【Buffer 大小】
// 256（在 gateway.go 的 New 中设置）：足够缓冲短暂的写入尖峰，
// 但不会消耗过多内存。如果 channel 满了，sender 端用 1ms timeout 放弃写入，
// 避免阻塞 HTTP 响应。
type accessLogWriter struct {
	logStore *config.LogStore
	ch       chan config.AccessLog
	wg       sync.WaitGroup
}

// newAccessLogWriter 创建并启动 accessLogWriter。
// 构造函数会立即启动后台 goroutine（go w.run()），因此调用方无需额外启动。
// bufSize 指定 channel 缓冲区大小，传入 256（在 gateway.go New 中）。
func newAccessLogWriter(logStore *config.LogStore, bufSize int) *accessLogWriter {
	w := &accessLogWriter{
		logStore: logStore,
		ch:       make(chan config.AccessLog, bufSize),
	}
	w.wg.Add(1)
	go w.run()
	return w
}

// run 后台主循环：从 channel 消费日志，批量写入 Pebble。
//
// 工作流程：
//  1. 从 ch 接收日志 → Append 到当前 batch
//  2. 如果 batch 达到 50 条 → 立即 flush（Commit）
//  3. 如果 50ms 内没有新日志 → ticker 触发 flush
//  4. channel 关闭（Shutdown 时）→ flush 剩余日志 → 退出
//
// 错误处理：单条日志 Append 失败时只记录错误并 continue，不中断整个 batch。
// 这样确保一条日志的序列化失败不会影响其他日志的写入。
// batch Commit 失败时记录错误并 Close batch（释放资源），下一条日志会创建新 batch。
func (w *accessLogWriter) run() {
	defer w.wg.Done()

	var batch *config.AccessLogBatch
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	flush := func() {
		if batch == nil || batch.Count() == 0 {
			return
		}
		if err := batch.Commit(pebble.NoSync); err != nil {
			slog.Error("pebble_batch_commit_failed", "err", err)
			batch.Close()
		}
		batch = nil
	}

	for {
		select {
		case logEntry, ok := <-w.ch:
			if !ok {
				flush()
				return
			}
			if batch == nil {
				batch = w.logStore.NewAccessLogBatch()
			}

			if err := batch.Append(&logEntry); err != nil {
				slog.Error("access_log_marshal_failed", "err", err)
				continue
			}
			if batch.Rows() >= 50 {
				flush()
			}

		case <-ticker.C:
			flush()
		}
	}
}

// Shutdown 优雅关闭 writer：关闭 channel → 等待 goroutine 处理完剩余日志 → 超时保护。
//
// 关闭流程：
//  1. close(ch)：通知 run() goroutine 停止接收新日志，channel 关闭后 range 退出
//  2. wg.Wait()：等待 run() 完成最后一批 flush 并退出
//  3. timeout 超时保护：如果 run() 在 timeout 时间内未完成（如 Pebble 写入卡死），
//     记录 warn 日志并放弃等待，避免网关关闭被无限阻塞
//
// 注意：close(ch) 之后，中间件中向 ch 发送日志的操作会 panic。
// 调用方需要在 Shutdown 前停止接收新请求（通常在 server.Shutdown 的流程中保证）。
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
//
// 使用场景：accessLogMiddleware 用 responseRecorder 替代原始的 http.ResponseWriter，
// 传给后续的 handler 链，从而透明地捕获响应状态码和响应体内容。
//
// 内存注意事项：
// Write 方法通过 append 持续累积响应体到 body []byte。对于大型流式响应（如 SSE streaming），
// body 会持续增长。但由于 Go 的 append 在容量不足时会分配新底层数组并复制，极端长连接场景
// 可能会有内存压力。当前场景下，流式响应通过 SetClientResponse 单独设置 clientResponse，
// 中间件在 clientResp 非空或状态码 >= 300 时不会回退读取 recorder.Body()，因此实际影响可控。
type responseRecorder struct {
	http.ResponseWriter
	statusCode  int
	wroteHeader bool
	body        []byte
	skipBody    bool
}

// WriteHeader 记录状态码（幂等保护）。
//
// Go 标准库的 http.ResponseWriter 要求 WriteHeader 只能调用一次。
// 虽然标准库有一定保护，这里通过 wroteHeader 标志做防御性编程，
// 确保即使被多次调用也只在第一次记录状态码。
// 注意：实际 ResponseWriter.WriteHeader(code) 每次都会被调用，
// 底层 http 包会忽略后续调用。
func (r *responseRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.statusCode = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write 写入响应体并捕获内容。
//
// 自动设置 200 状态码：
// 如果 handler 在 Write 之前从未调用 WriteHeader，Go 的 http 包默认会发送 200。
// 这里模拟该行为：第一次 Write 时如果 wroteHeader 为 false，自动设置 statusCode = 200。
// 这确保了 StatusCode() 方法在任意调用顺序下都能返回正确值。
//
// 响应体捕获：
// body 通过 append(r.body, b...) 累积所有写入的内容。不限制大小，
// 依赖中间件层的 clientResp 回退逻辑（状态码 < 300 且 clientResp 为空时读取 body）
// 来控制实际使用场景。
func (r *responseRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.statusCode = http.StatusOK
		r.wroteHeader = true
	}
	if !r.skipBody {
		r.body = append(r.body, b...)
	}
	return r.ResponseWriter.Write(b)
}

// Flush 透传 Flush 调用到底层 ResponseWriter（如果支持）。
// SSE 流式响应需要 Flush 支持来及时推送数据块。
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// StatusCode 返回记录的 HTTP 状态码。
// 如果 WriteHeader 和 Write 都未被调用（理论上不应出现），返回 200（默认成功状态）。
func (r *responseRecorder) StatusCode() int {
	if !r.wroteHeader {
		return http.StatusOK
	}
	return r.statusCode
}

// Body 返回捕获的响应体内容。
func (r *responseRecorder) Body() string {
	return string(r.body)
}

func (g *Gateway) shouldSaveAccessLogBodies() bool {
	if g == nil || g.store == nil {
		return true
	}
	value, err := g.store.GetSetting(accessLogSaveBodiesSetting)
	if err != nil {
		slog.Warn("access_log_save_bodies_get_setting_failed", "error", err)
		return true
	}
	if value == "" {
		return true
	}
	save, err := strconv.ParseBool(value)
	if err != nil {
		slog.Warn("access_log_save_bodies_invalid", "value", value, "error", err)
		return true
	}
	return save
}

// accessLogMiddleware 记录所有 AI API 请求的访问日志（/v1/models 除外）
//
// 中间件处理流程：
//
//  1. /v1/models 绕过（bypass）
//     /v1/models 是列出可用模型的查询接口，不是 AI 推理请求，日志噪音大且无分析价值。
//     直接调用 next.ServeHTTP(w, r) 不经过任何 collector 逻辑。
//
//  2. 创建 collector 并注入 context
//     每个请求创建独立的 AccessLogCollector 实例（绝不跨请求复用），
//     通过 withCollector 注入到请求的 context 链中。
//     handler（responses.go, messages.go）通过 collectorFromContext 提取并设置字段。
//
//  3. 包装 responseRecorder
//     用 responseRecorder 替代原始 ResponseWriter，捕获下游 handler 产生的
//     状态码和响应体内容。
//
//  4. 执行下一个 handler
//     next.ServeHTTP(rec, r) → 路由 → handler → provider 调用。
//     所有 collector.setter 调用在此期间发生。
//
//  5. 请求完成后收集字段
//     为最小化锁持有时间，将锁内操作限制在一个匿名闭包中（func(){ collector.mu.Lock()... }()）。
//     先拷贝所有需要字段的副本，释放锁后再执行后续逻辑（quota 回退解析、JSON 序列化等）。
//
//  6. Quota 回退解析
//     调用 quotaFromAttempts 按三级 fallback 解析最终的配额信息。
//
//  7. Attempts JSON 序列化
//     如果有 failover 尝试记录（len(attemptsCopy) > 0），序列化为 JSON 字符串。
//     空 attempts 时不序列化（attemptsJSON 保持空字符串）。
//
//  8. 响应体回退（fallback）
//     如果 handler 未通过 SetClientResponse 显式设置 clientResponse（clientResp == ""），
//     且状态码正常（< 300），则使用 responseRecorder 捕获的 body 作为响应体。
//     排除了错误状态码（>= 300）的原因是：错误响应体通常是网关层生成的简短错误信息，
//     不是 AI 模型的推理输出，记录它没有日志分析价值。
//
//  9. 组装 AccessLog
//     将所有字段组装为 config.AccessLog 结构体，包括：
//     - 请求元数据：Timestamp, ApiKeyID, Method, Path, RemoteIP, RequestID
//     - AI 推理特征：Model, ProviderName, TokensIn, TokensOut, DurationMs
//     - 调试审计：ClientReq, ClientResp, UpstreamReq, UpstreamResp, ErrorMsg
//     - Failover：AttemptsJSON
//     - 配额：QuotaBefore, QuotaAfter
//     RequestID 通过 chimw.GetReqID(r.Context()) 从 chi 中间件获取，
//     实现了全链路日志关联。
//
//  10. 非阻塞 channel 发送（1ms timeout）
//     尝试将 AccessLog 发送到 accessLogWriter.ch。
//     如果 1ms 内 channel 没有空闲缓冲区（可能因为 writer goroutine 正在处理
//     大量日志或 Pebble 写入卡顿），直接放弃并记录 warn 日志。
//     这样确保 HTTP 响应的延迟不受日志写入速度影响，是一种背压保护策略。
//     设计考量：访问日志是辅助数据，不应阻塞或延迟主业务流程。
func (g *Gateway) accessLogMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		saveBodies := g.shouldSaveAccessLogBodies()

		collector := &AccessLogCollector{}
		r = r.WithContext(withCollector(r.Context(), collector))

		rec := &responseRecorder{ResponseWriter: w, skipBody: !saveBodies}

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
		if !saveBodies {
			clientBody = ""
			clientResp = ""
			upstreamReq = ""
			upstreamResp = ""
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
