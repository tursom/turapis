/*
logstore_v2.go — 访问日志存储 V2 优化层
=============================================
V2 相对于 V1（legacy logstore.go）的核心优化策略：

1. 紧凑摘要行（AccessLogSummary）：
   V1 每次查询都需要反序列化完整的请求/响应 payload（JSON 体积大），查询效率低。
   V2 新增独立的摘要行，仅包含列表和统计所需的字段（不含 RequestBody/ResponseBody），
   查询时无需读取 payload，大幅减少 I/O 和反序列化开销。

2. 复合索引（composite indexes）：
   V1 只有按时间排序的主键和少量的模型索引，多条件筛选需要全表扫描。
   V2 建立了多级复合索引层次结构，支持按 (apiKey, model, status) 的任意组合快速定位：
   - 单字段索引：APIKey、Model、Status
   - 双字段索引：APIKey+Model、APIKey+Status、Model+Status
   - 三字段索引：APIKey+Model+Status
   accessLogIndexPrefixForQuery 根据查询条件自动选择最优索引前缀。

3. 分钟级预聚合统计（pre-aggregated minute stats）：
   V1 的 Stats 查询需要遍历时间范围内所有记录逐条累加，O(N) 复杂度。
   V2 在写入时实时累积每分钟的统计增量（计数、token 出入），
   统计查询时直接读取预计算值，实现 O(1) 查询复杂度。

4. Delta 追踪系统（accessLogStatDelta）：
   采用增量而非全量存储统计值。每次写入/删除只计算该记录对统计的贡献（delta），
   与已有值合并后写入。通过 clamp() 防止并发或异常导致负数，isZero 判断清理零值键。

5. 部分分钟回退（partial-minute fallback）：
   当时间桶边界与分钟对齐不一致时（如某个时间桶只覆盖了一分钟的 30 秒），
   不能使用预聚合值（预聚合是整个分钟的统计），需退回到逐条扫描摘要行来精确计算。

6. 崩溃安全的增量回填（backfill）：
   从 V1 升级到 V2 时，通过增量扫描已有主键记录生成 V2 索引和摘要。
   使用检查点键（checkpoint key）记录进度，崩溃后可从上次中断处恢复。

Pebble 键布局（key layout）：
  所有键使用前缀字节区分类型，后跟大端序编码的时间戳和计数器，确保键按时间排序。

  主要键类型（详见 logstore.go 前缀常量定义）：
  - 0x00: Meta 键（v2Ready 标记、回填检查点）
  - 0x04: 摘要时间索引 [0x04 + tsNano(8B) + counter(8B)] = 17 字节
  - 0x05: APIKey 索引 [0x05 + apiKeyID(8B) + tsNano(8B) + counter(8B)] = 25 字节
  - 0x06: 状态码索引 [0x06 + status(4B) + tsNano(8B) + counter(8B)] = 21 字节
  - 0x07: 模型索引 V2 [0x07 + model + 0x00 + tsNano(8B) + counter(8B)]
  - 0x08: APIKey+Model 复合索引
  - 0x09: APIKey+Status 复合索引
  - 0x0A: Model+Status 复合索引
  - 0x0B: APIKey+Model+Status 复合索引
  - 0x0C: 分钟预聚合统计 [0x0C + minuteMillis(8B)] = 9 字节
*/
package config

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// accessLogStatsMinuteMillis 一分钟的毫秒数常量，用于分钟级时间戳对齐。
const accessLogStatsMinuteMillis = int64(time.Minute / time.Millisecond)

var (
	// accessLogV2ReadyKey 标记 V2 升级是否完成的 meta 键。
	// 键布局: 0x00 + "alv2_r" — 前缀 0x00 表示 pebblePrefixMeta。
	// 写入时机: backfill 全部完成后原子设置。
	// 读取时机: LogStore 初始化时检查，决定走 V1 还是 V2 路径。
	accessLogV2ReadyKey = []byte{pebblePrefixMeta, 'a', 'l', 'v', '2', '_', 'r'}

	// accessLogV2BackfillCheckpointKey 回填过程的检查点键。
	// 键布局: 0x00 + "alv2_c" — 与 readyKey 共享 meta 前缀。
	// 值存储最后处理完的主键字节，崩溃恢复时从此处继续。
	accessLogV2BackfillCheckpointKey = []byte{pebblePrefixMeta, 'a', 'l', 'v', '2', '_', 'c'}
)

/*
AccessLogSummary 是 V2 列表查询和统计查询使用的紧凑行结构。

与完整的 AccessLog 对比：
  V1 AccessLog 包含 RequestBody 和 ResponseBody（可能数十 KB 的 JSON 字符串），
  列表/统计查询不需要这些字段，却每次都要反序列化全部 JSON。

  V2 AccessLogSummary 仅保留 16 个高频查询字段，省略了：
  - RequestBody、ResponseBody（仅主行保留）
  - 内部管理字段（如 CreatedAt、UpdatedAt）
  每条摘要行约 1/10 的 V1 行体积，查询时 I/O 和 CPU 开销大幅降低。

存储方式：
  - 摘要行作为独立键值对存储在 Pebble 中（键 = pebblePrefixSummaryTime + tsNano + counter）
  - 主行（含完整 payload）仍按 V1 格式存储（键 = pebblePrefixTime + tsNano + counter）
  - 索引键（apiKey/model/status 及其组合）值置空，指向摘要键
*/
type AccessLogSummary struct {
	ID           int    `json:"id"`
	Timestamp    int64  `json:"timestamp"`
	ApiKeyID     *int   `json:"api_key_id"`
	ApiKeyName   string `json:"api_key_name"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Model        string `json:"model"`
	StatusCode   int    `json:"status_code"`
	TokensIn     int    `json:"tokens_in"`
	TokensOut    int    `json:"tokens_out"`
	DurationMs   int    `json:"duration_ms"`
	RemoteIP     string `json:"remote_ip"`
	RequestID    string `json:"request_id"`
	ProviderName string `json:"provider_name"`
	ErrorMsg     string `json:"error_msg"`
	QuotaBefore  string `json:"quota_before"`
	QuotaAfter   string `json:"quota_after"`
	AttemptsJSON string `json:"attempts_json,omitempty"`
	HasFailover  bool   `json:"has_failover"`
}

// accessLogSummaryFromLog 从完整 AccessLog 提取摘要行，并标记是否包含故障转移。
// 仅复制列表/统计所需字段，省略 RequestBody、ResponseBody 等大字段。
func accessLogSummaryFromLog(log *AccessLog, hasFailover bool) AccessLogSummary {
	return AccessLogSummary{
		ID:           log.ID,
		Timestamp:    log.Timestamp,
		ApiKeyID:     log.ApiKeyID,
		ApiKeyName:   log.ApiKeyName,
		Method:       log.Method,
		Path:         log.Path,
		Model:        log.Model,
		StatusCode:   log.StatusCode,
		TokensIn:     log.TokensIn,
		TokensOut:    log.TokensOut,
		DurationMs:   log.DurationMs,
		RemoteIP:     log.RemoteIP,
		RequestID:    log.RequestID,
		ProviderName: log.ProviderName,
		ErrorMsg:     log.ErrorMsg,
		QuotaBefore:  log.QuotaBefore,
		QuotaAfter:   log.QuotaAfter,
		AttemptsJSON: log.AttemptsJSON,
		HasFailover:  hasFailover,
	}
}

func (s AccessLogSummary) accessLog() AccessLog {
	return AccessLog{
		ID:           s.ID,
		Timestamp:    s.Timestamp,
		ApiKeyID:     s.ApiKeyID,
		ApiKeyName:   s.ApiKeyName,
		Method:       s.Method,
		Path:         s.Path,
		Model:        s.Model,
		StatusCode:   s.StatusCode,
		TokensIn:     s.TokensIn,
		TokensOut:    s.TokensOut,
		DurationMs:   s.DurationMs,
		RemoteIP:     s.RemoteIP,
		RequestID:    s.RequestID,
		ProviderName: s.ProviderName,
		ErrorMsg:     s.ErrorMsg,
		QuotaBefore:  s.QuotaBefore,
		QuotaAfter:   s.QuotaAfter,
		AttemptsJSON: s.AttemptsJSON,
	}
}

// accessLogHasFailover 从 AttemptsJSON 字符串判断该请求是否发生了故障转移。
// 判定条件：存在多条 attempt 记录 或 某条 attempt 的 AttemptNum > 1。
// 这会决定统计时计入 with_failover 还是 without_failover 桶。
func accessLogHasFailover(raw string) bool {
	if raw == "" || raw == "null" {
		return false
	}
	var attempts []AttemptRecord
	if err := json.Unmarshal([]byte(raw), &attempts); err != nil {
		return false
	}
	if len(attempts) > 1 {
		return true
	}
	for _, a := range attempts {
		if a.AttemptNum > 1 {
			return true
		}
	}
	return false
}

/*
accessLogStatDelta 是统计增量结构，用于分钟级预聚合。

设计思路（为什么用 delta 而非全量）：
  每分钟的统计（请求数、token 出入）由多条日志组成。
  如果存储全量值，每次新增/删除一条日志都需要重新遍历该分钟所有记录来计算。
  使用 delta（增量），只需计算该条日志的贡献值（sign=+1 为新增，sign=-1 为删除），
  与已有值累加即可，无需遍历其他记录。这就是 O(1) 统计查询的数学基础。

字段命名规则（with_failover / without_failover）：
  含故障转移的请求和不含的分开统计，因为用户体验差异显著。
  写入时通过 accessLogHasFailover 判定，计入对应桶。

编码格式（encodeStatDelta / decodeStatDelta）：
  二进制大端序，6 个 int64 字段 = 48 字节定长。
  顺序: CountWithFailover(8B) | CountWithoutFailover(8B) | TokensInWithFailover(8B) |
         TokensInWithoutFailover(8B) | TokensOutWithFailover(8B) | TokensOutWithoutFailover(8B)
*/
type accessLogStatDelta struct {
	CountWithFailover        int64
	CountWithoutFailover     int64
	TokensInWithFailover     int64
	TokensInWithoutFailover  int64
	TokensOutWithFailover    int64
	TokensOutWithoutFailover int64
}

// statDeltaFromSummary 从一条摘要行生成其对该分钟统计的增量贡献（delta）。
// sign = +1 表示新增记录，sign = -1 表示删除记录。
// 根据 HasFailover 决定贡献到 with_failover 还是 without_failover 桶。
func statDeltaFromSummary(s AccessLogSummary, sign int64) accessLogStatDelta {
	if s.HasFailover {
		return accessLogStatDelta{
			CountWithFailover:     sign,
			TokensInWithFailover:  sign * int64(s.TokensIn),
			TokensOutWithFailover: sign * int64(s.TokensOut),
		}
	}
	return accessLogStatDelta{
		CountWithoutFailover:     sign,
		TokensInWithoutFailover:  sign * int64(s.TokensIn),
		TokensOutWithoutFailover: sign * int64(s.TokensOut),
	}
}

func (d *accessLogStatDelta) add(other accessLogStatDelta) {
	d.CountWithFailover += other.CountWithFailover
	d.CountWithoutFailover += other.CountWithoutFailover
	d.TokensInWithFailover += other.TokensInWithFailover
	d.TokensInWithoutFailover += other.TokensInWithoutFailover
	d.TokensOutWithFailover += other.TokensOutWithFailover
	d.TokensOutWithoutFailover += other.TokensOutWithoutFailover
}

// clamp 将统计值截断到非负范围，防止并发更新或异常数据导致负数。
// 统计值（计数、token 数）在语义上不应为负，此方法确保不变量。
func (d *accessLogStatDelta) clamp() {
	if d.CountWithFailover < 0 {
		d.CountWithFailover = 0
	}
	if d.CountWithoutFailover < 0 {
		d.CountWithoutFailover = 0
	}
	if d.TokensInWithFailover < 0 {
		d.TokensInWithFailover = 0
	}
	if d.TokensInWithoutFailover < 0 {
		d.TokensInWithoutFailover = 0
	}
	if d.TokensOutWithFailover < 0 {
		d.TokensOutWithFailover = 0
	}
	if d.TokensOutWithoutFailover < 0 {
		d.TokensOutWithoutFailover = 0
	}
}

// isZero 判断统计增量是否全为零（无有效数据）。
// 用于 Commit 时清理无效键：如果某分钟的统计归零，删除该分钟对应的 Pebble 键。
func (d accessLogStatDelta) isZero() bool {
	return d.CountWithFailover == 0 &&
		d.CountWithoutFailover == 0 &&
		d.TokensInWithFailover == 0 &&
		d.TokensInWithoutFailover == 0 &&
		d.TokensOutWithFailover == 0 &&
		d.TokensOutWithoutFailover == 0
}

// addSummaryToBucket 将一条摘要行的数据累加到统计桶中。
// 用于部分分钟回退场景：无法使用预聚合值时，逐条累加摘要行到对应时间桶。
func addSummaryToBucket(bucket *BucketStat, s AccessLogSummary) {
	if s.HasFailover {
		bucket.CountWithFailover++
		bucket.TokensInWithFailover += s.TokensIn
		bucket.TokensOutWithFailover += s.TokensOut
		return
	}
	bucket.CountWithoutFailover++
	bucket.TokensInWithoutFailover += s.TokensIn
	bucket.TokensOutWithoutFailover += s.TokensOut
}

// addDeltaToBucket 将预聚合的分钟统计增量累加到 BucketStat 中。
// 用于 statsV2 快速路径：直接读取预计算值，O(1) 操作，无需扫描摘要行。
func addDeltaToBucket(bucket *BucketStat, d accessLogStatDelta) {
	bucket.CountWithFailover += int(d.CountWithFailover)
	bucket.CountWithoutFailover += int(d.CountWithoutFailover)
	bucket.TokensInWithFailover += int(d.TokensInWithFailover)
	bucket.TokensInWithoutFailover += int(d.TokensInWithoutFailover)
	bucket.TokensOutWithFailover += int(d.TokensOutWithFailover)
	bucket.TokensOutWithoutFailover += int(d.TokensOutWithoutFailover)
}

// encodeStatDelta 将统计增量编码为 48 字节定长二进制格式（大端序）。
// 编码前先 clamp 确保无负数。格式详见 accessLogStatDelta 的注释。
func encodeStatDelta(d accessLogStatDelta) []byte {
	d.clamp()
	out := make([]byte, 48)
	binary.BigEndian.PutUint64(out[0:8], uint64(d.CountWithFailover))
	binary.BigEndian.PutUint64(out[8:16], uint64(d.CountWithoutFailover))
	binary.BigEndian.PutUint64(out[16:24], uint64(d.TokensInWithFailover))
	binary.BigEndian.PutUint64(out[24:32], uint64(d.TokensInWithoutFailover))
	binary.BigEndian.PutUint64(out[32:40], uint64(d.TokensOutWithFailover))
	binary.BigEndian.PutUint64(out[40:48], uint64(d.TokensOutWithoutFailover))
	return out
}

func decodeStatDelta(raw []byte) (accessLogStatDelta, error) {
	if len(raw) != 48 {
		return accessLogStatDelta{}, fmt.Errorf("invalid stat length %d", len(raw))
	}
	return accessLogStatDelta{
		CountWithFailover:        int64(binary.BigEndian.Uint64(raw[0:8])),
		CountWithoutFailover:     int64(binary.BigEndian.Uint64(raw[8:16])),
		TokensInWithFailover:     int64(binary.BigEndian.Uint64(raw[16:24])),
		TokensInWithoutFailover:  int64(binary.BigEndian.Uint64(raw[24:32])),
		TokensOutWithFailover:    int64(binary.BigEndian.Uint64(raw[32:40])),
		TokensOutWithoutFailover: int64(binary.BigEndian.Uint64(raw[40:48])),
	}, nil
}

/*
minuteStartMillis 将毫秒时间戳向下对齐到该分钟的起点。

用途：
  - 作为 stats map 的键，将同一分钟内的所有日志归入同一个统计桶。
  - 生成 encodeStatsMinuteKey 的时间部分。
  - partialStatMinutes 中标识部分覆盖的分钟。
*/
func minuteStartMillis(ts int64) int64 {
	return ts / accessLogStatsMinuteMillis * accessLogStatsMinuteMillis
}

/*
encodeSummaryKey 构建摘要行的 Pebble 键。

键布局 (17 字节):
  [0]    = pebblePrefixSummaryTime (0x04)
  [1:9]  = tsNano (纳秒级时间戳，大端序，8 字节)
  [9:17] = counter (自增 ID，大端序，8 字节)

大端序编码确保 Pebble 按时间+ID 字典序排序，迭代器可高效范围扫描。
*/
func encodeSummaryKey(tsNano, counter uint64) []byte {
	return encodeTimeIDKey(pebblePrefixSummaryTime, tsNano, counter)
}

/*
encodeStatsMinuteKey 构建分钟预聚合统计键。

键布局 (9 字节):
  [0]   = pebblePrefixStatsMinute (0x0C)
  [1:9] = minuteMillis (该分钟起点的毫秒时间戳，大端序，8 字节)
*/
func encodeStatsMinuteKey(minuteMillis int64) []byte {
	k := make([]byte, 9)
	k[0] = pebblePrefixStatsMinute
	binary.BigEndian.PutUint64(k[1:9], uint64(minuteMillis))
	return k
}

/*
encodeTimeIDKey 构建通用 "前缀 + 纳秒时间戳 + 计数器" 键。

键布局 (17 字节):
  [0]    = prefix (根据用途不同，可以是 0x05~0x0B 中的索引前缀)
  [1:9]  = tsNano (纳秒级时间戳，大端序)
  [9:17] = counter (自增 ID，大端序)

这是所有复合索引键的底层构建函数。前缀决定索引类型，时间戳确保排序，
计数器确保同一纳秒内的记录也能区分。
*/
func encodeTimeIDKey(prefix byte, tsNano, counter uint64) []byte {
	k := make([]byte, 17)
	k[0] = prefix
	binary.BigEndian.PutUint64(k[1:9], tsNano)
	binary.BigEndian.PutUint64(k[9:17], counter)
	return k
}

// appendUint64Key 将大端序的 uint64 追加到字节切片末尾。用于逐段构建复合键。
func appendUint64Key(k []byte, v uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], v)
	return append(k, buf[:]...)
}

// appendUint32Key 将大端序的 uint32 追加到字节切片末尾。用于状态码等四字节字段。
func appendUint32Key(k []byte, v uint32) []byte {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], v)
	return append(k, buf[:]...)
}

// appendTimeIDSuffix 将 tsNano + counter（均为大端序 uint64）追加到键末尾。
// 等效于 append(appendUint64Key(k, tsNano), counter 的大端编码...)
func appendTimeIDSuffix(k []byte, tsNano, counter uint64) []byte {
	k = appendUint64Key(k, tsNano)
	return appendUint64Key(k, counter)
}

/*
prefixSuccessor 计算给定前缀在字节序中的"下一个"前缀——即该前缀之后最小的不可达键。

Pebble 使用 [lower, upper) 区间表示范围扫描。当 upper 为 nil 时扫描到末尾。
但我们需要一个明确的 upper bound，prefixSuccessor 通过将最后一个非 0xFF 字节加 1
来生成刚好"大于"所有以该前缀开头的键的最小值。

例如: prefixSuccessor([]byte{0x05, 0x00}) => []byte{0x05, 0x01}
     prefixSuccessor([]byte{0xFF, 0xFF}) => nil (无后继，返回 nil 表示无限)

返回 nil 时调用方应将 UpperBound 设为 nil（表示无上限）。
*/
func prefixSuccessor(prefix []byte) []byte {
	out := append([]byte(nil), prefix...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] != 0xFF {
			out[i]++
			return out[:i+1]
		}
	}
	return nil
}

/*
timeBoundsForPrefix 为指定的前缀生成带时间范围约束的 Pebble 迭代器上下界。

参数:
  - prefix: 索引前缀（如 apiKeyModelStatusIndexPrefix 的结果）
  - startAt: 查询起始时间（毫秒），nil 表示不限
  - endAt:   查询结束时间（毫秒，开区间），nil 表示不限

下界 (lower):
  - 有 startAt: prefix + startAt(纳秒) + 0 (最小 counter)
  - 无 startAt: 仅前缀（匹配该前缀的所有键）

上界 (upper):
  - 有 endAt:   prefix + (endAt+1)(纳秒) + 0 (Uint64 溢出自然循环，无需特殊处理)
  - 无 endAt:   prefixSuccessor(prefix) (该前缀的下一个不可达键)
*/
func timeBoundsForPrefix(prefix []byte, startAt, endAt *int64) (lower, upper []byte) {
	if startAt != nil {
		lower = appendTimeIDSuffix(append([]byte(nil), prefix...), unixMilliToNano(*startAt), 0)
	} else {
		lower = append([]byte(nil), prefix...)
	}
	if endAt != nil {
		upper = appendTimeIDSuffix(append([]byte(nil), prefix...), unixMilliToNano(*endAt)+1, 0)
	} else {
		upper = prefixSuccessor(prefix)
	}
	return lower, upper
}

/*
decodeIndexedTimeID 从复合索引键的末尾提取 tsNano 和 counter。

复合索引键结构: [前缀...][tsNano(8B)][counter(8B)]
此函数读取键的最后 16 字节（倒数第 16-9 字节为 tsNano，倒数第 8-1 字节为 counter）。

用于 summaryFromIterator 中：当迭代器不在 SummaryTime 前缀上时，
从复合索引键中提取时间信息，再通过 readSummary 查找摘要行。
*/
func decodeIndexedTimeID(k []byte) (tsNano, counter uint64) {
	n := len(k)
	return binary.BigEndian.Uint64(k[n-16 : n-8]), binary.BigEndian.Uint64(k[n-8:])
}

// apiKeyIndexPrefix 构建 APIKey 单字段索引前缀。
// 键前缀: [0x05][apiKeyID(8B, 大端序)]，后续追加 tsNano+counter 组成完整键。
func apiKeyIndexPrefix(apiKeyID int) []byte {
	return appendUint64Key([]byte{pebblePrefixIndexAPIKey}, uint64(apiKeyID))
}

// statusIndexPrefix 构建 HTTP 状态码单字段索引前缀。
// 键前缀: [0x06][status(4B, 大端序)]，后续追加 tsNano+counter 组成完整键。
func statusIndexPrefix(status int) []byte {
	return appendUint32Key([]byte{pebblePrefixIndexStatus}, uint32(status))
}

/*
modelIndexV2Prefix 构建模型名称单字段索引前缀。
键前缀: [0x07][model 字符串][0x00]，后续追加 tsNano+counter 组成完整键。

与 V1 的 pebblePrefixModel (0x03) 索引不同，V2 索引支持按时间范围高效扫描，
而 V1 仅支持按模型+时间间隔的游标扫描。
*/
func modelIndexV2Prefix(model string) []byte {
	p := make([]byte, 1, 1+len(model)+1)
	p[0] = pebblePrefixIndexModelV2
	p = append(p, model...)
	return append(p, 0)
}

// apiKeyModelIndexPrefix 构建 APIKey+Model 双字段复合索引前缀。
// 键前缀: [0x08][apiKeyID(8B)][model 字符串][0x00]，后续追加 tsNano+counter。
func apiKeyModelIndexPrefix(apiKeyID int, model string) []byte {
	p := apiKeyIndexPrefixWithPrefix(pebblePrefixIndexAPIKeyModel, apiKeyID)
	p = append(p, model...)
	return append(p, 0)
}

// apiKeyStatusIndexPrefix 构建 APIKey+Status 双字段复合索引前缀。
// 键前缀: [0x09][apiKeyID(8B)][status(4B)]，后续追加 tsNano+counter。
func apiKeyStatusIndexPrefix(apiKeyID int, status int) []byte {
	p := apiKeyIndexPrefixWithPrefix(pebblePrefixIndexAPIKeyStatus, apiKeyID)
	return appendUint32Key(p, uint32(status))
}

// modelStatusIndexPrefix 构建 Model+Status 双字段复合索引前缀。
// 键前缀: [0x0A][model 字符串][0x00][status(4B)]，后续追加 tsNano+counter。
func modelStatusIndexPrefix(model string, status int) []byte {
	p := make([]byte, 1, 1+len(model)+1+4)
	p[0] = pebblePrefixIndexModelStatus
	p = append(p, model...)
	p = append(p, 0)
	return appendUint32Key(p, uint32(status))
}

// apiKeyModelStatusIndexPrefix 构建 APIKey+Model+Status 三字段复合索引前缀。
// 键前缀: [0x0B][apiKeyID(8B)][model 字符串][0x00][status(4B)]，后续追加 tsNano+counter。
// 这是过滤条件最精确的索引，查询效率最高。
func apiKeyModelStatusIndexPrefix(apiKeyID int, model string, status int) []byte {
	p := apiKeyIndexPrefixWithPrefix(pebblePrefixIndexAPIKeyModelStatus, apiKeyID)
	p = append(p, model...)
	p = append(p, 0)
	return appendUint32Key(p, uint32(status))
}

func apiKeyIndexPrefixWithPrefix(prefix byte, apiKeyID int) []byte {
	return appendUint64Key([]byte{prefix}, uint64(apiKeyID))
}

/*
accessLogIndexPrefixForQuery 根据查询条件自动选择最优复合索引前缀。

索引选择策略（按优先级从高到低）：
  1. APIKey+Model+Status (0x0B) — 三字段全覆盖，扫描范围最小
  2. APIKey+Model       (0x08) — 双字段，次优
  3. APIKey+Status      (0x09) — 双字段，与上条同级
  4. Model+Status       (0x0A) — 双字段（无 APIKey 限制）
  5. APIKey             (0x05) — 单字段
  6. Model              (0x07) — 单字段（V2 模型索引）
  7. Status             (0x06) — 单字段
  8. SummaryTime        (0x04) — 无任何过滤条件，回退到时间排序的摘要索引

原理：
  Pebble 的键按字典序排序。复合索引将多个维度编码到键前缀中，
  相同的查询过滤条件对应的键在 Pebble 中连续存储，一次范围扫描即可覆盖所有匹配项。
  选择字段最多的索引意味着【前缀最长 → 匹配范围最窄 → 扫描键最少】。
*/
func accessLogIndexPrefixForQuery(q AccessLogQuery) []byte {
	hasAPIKey := q.ApiKeyID != nil
	hasModel := q.Model != ""
	hasStatus := q.Status != nil

	switch {
	case hasAPIKey && hasModel && hasStatus:
		return apiKeyModelStatusIndexPrefix(*q.ApiKeyID, q.Model, *q.Status)
	case hasAPIKey && hasModel:
		return apiKeyModelIndexPrefix(*q.ApiKeyID, q.Model)
	case hasAPIKey && hasStatus:
		return apiKeyStatusIndexPrefix(*q.ApiKeyID, *q.Status)
	case hasModel && hasStatus:
		return modelStatusIndexPrefix(q.Model, *q.Status)
	case hasAPIKey:
		return apiKeyIndexPrefix(*q.ApiKeyID)
	case hasModel:
		return modelIndexV2Prefix(q.Model)
	case hasStatus:
		return statusIndexPrefix(*q.Status)
	default:
		return []byte{pebblePrefixSummaryTime}
	}
}

// initAccessLogV2Ready 在 LogStore 初始化时检查 V2 就绪标记。
// 如果 accessLogV2ReadyKey 存在，说明回填已完成，v2Ready 标志设为 true。
func (s *LogStore) initAccessLogV2Ready() {
	if _, closer, err := s.db.Get(accessLogV2ReadyKey); err == nil {
		closer.Close()
		s.v2Ready.Store(true)
	}
}

// isAccessLogV2Ready 检查 V2 存储引擎是否已就绪（回填是否完成）。
// 此函数作为 V1/V2 路径的分发器：所有公开查询接口在调用前都检查此标记，
// 若未就绪则回退到 V1 逻辑（legacy 全量扫描）。
func (s *LogStore) isAccessLogV2Ready() bool {
	return s.v2Ready.Load()
}

/*
AccessLogBatch 是 V2 的批量写入控制器，封装了 Pebble 的 Batch 操作。

职责（每次批量写入执行以下操作）：
  1. 写入 V1 主行（含完整 payload 的 JSON）
  2. 写入 V1 ID 索引（按 ID 查找）
  3. 写入 V1 模型索引
  4. 写入 V2 摘要行（紧凑 JSON，不含 payload）
  5. 写入 V2 复合索引键（apiKey/model/status 及其组合，共 7 个键，值置空）
  6. 按分钟累积统计增量（stats map）

通过 Pebble Batch 的原子性保证：上述所有写操作在一次 Commit 中原子提交，
不会出现"V1 主行写了但 V2 摘要行没写"的部分成功状态。

字段说明：
  - store:    反向引用 LogStore，用于获取 nextID 等
  - batch:    底层 Pebble Batch
  - stats:    按分钟累积的统计增量（key=分钟起点毫秒，value=delta）
  - rows:     本批次写入的行数（统计用）
  - addTotal: 是否在 Commit 后更新全局行计数器（回填时设为 false，常规写入时设为 true）
  - closed:   防止重复提交/关闭
*/
type AccessLogBatch struct {
	store    *LogStore
	batch    *pebble.Batch
	stats    map[int64]accessLogStatDelta
	rows     int64
	addTotal bool
	closed   bool
}

// NewAccessLogBatch 创建常规写入批次（addTotal=true，Commit 后更新全局计数器）。
func (s *LogStore) NewAccessLogBatch() *AccessLogBatch {
	return s.newAccessLogBatch(true)
}

// newAccessLogBatch 创建批次（内部使用）。
// addTotal=false 用于回填场景：回填已有记录不应更新全局计数器。
func (s *LogStore) newAccessLogBatch(addTotal bool) *AccessLogBatch {
	return &AccessLogBatch{
		store:    s,
		batch:    s.db.NewBatch(),
		stats:    make(map[int64]accessLogStatDelta),
		addTotal: addTotal,
	}
}

func (b *AccessLogBatch) Rows() int64 {
	return b.rows
}

func (b *AccessLogBatch) Count() int {
	if b.batch == nil {
		return 0
	}
	return int(b.batch.Count())
}

// Append 向批次添加一条新的访问日志（常规写入路径）。
// 自动分配 ID，如果 Timestamp 未设置则使用当前时间。
func (b *AccessLogBatch) Append(log *AccessLog) error {
	id := b.store.NextID()
	log.ID = int(id)
	if log.Timestamp <= 0 {
		log.Timestamp = time.Now().UnixMilli()
	}
	return b.appendAccessLog(log, unixMilliToNano(log.Timestamp), id, true)
}

func (b *AccessLogBatch) appendExisting(log *AccessLog, tsNano, counter uint64) error {
	log.ID = int(counter)
	if log.Timestamp <= 0 {
		log.Timestamp = int64(tsNano / uint64(time.Millisecond))
	}
	return b.appendAccessLog(log, tsNano, counter, false)
}

/*
appendAccessLog 是 V2 写入的核心实现，执行以下步骤（在 Pebble Batch 内全部原子提交）：

  1. 主行写入（仅常规路径，includePrimary=true）：
     - 编码主行 JSON（含完整 payload）到 pebblePrefixTime 键
     - 写入 ID 反向索引（EncodeIndexKey）
  2. V1 模型索引写入（向后兼容，仅在 model 非空时）
  3. 从主行提取摘要行，计算 failover 标记
  4. 调用 setV2Keys 写入摘要行 + 全部复合索引键
  5. 累积分钟统计增量到 stats map
*/
func (b *AccessLogBatch) appendAccessLog(log *AccessLog, tsNano, counter uint64, includePrimary bool) error {
	if includePrimary {
		jsonData, err := json.Marshal(log)
		if err != nil {
			return fmt.Errorf("marshal log %d: %w", counter, err)
		}
		if err := b.batch.Set(EncodePrimaryKey(tsNano, counter), jsonData, nil); err != nil {
			return err
		}
		if err := b.batch.Set(EncodeIndexKey(counter), EncodeTimestampValue(tsNano), nil); err != nil {
			return err
		}
	}
	if log.Model != "" {
		if err := b.batch.Set(EncodeModelIndexKey(log.Model, tsNano, counter), nil, nil); err != nil {
			return err
		}
	}

	summary := accessLogSummaryFromLog(log, accessLogHasFailover(log.AttemptsJSON))
	if err := b.setV2Keys(summary, tsNano, counter); err != nil {
		return err
	}
	b.stageSummaryStat(summary, 1)
	b.rows++
	return nil
}

/*
setV2Keys 写入一条记录的 V2 摘要行和所有复合索引键（双写模式）。

写入的键列表（共 8 个 Pebble 键）：
  1. 摘要行（值 = 摘要 JSON）:
     - pebblePrefixSummaryTime + tsNano + counter
  2. 单字段索引（值 = nil，仅键存在即可）:
     - APIKey 索引 (0x05):    仅在 apiKeyID != nil 时
     - Status 索引 (0x06):    始终写入（statusCode 总是有值）
     - Model 索引 (0x07):     仅在 model != "" 时
  3. 双字段复合索引（值 = nil）:
     - APIKey+Model (0x08):    apiKeyID != nil && model != ""
     - APIKey+Status (0x09):   apiKeyID != nil（status 始终有值）
     - Model+Status (0x0A):    model != ""
  4. 三字段复合索引（值 = nil）:
     - APIKey+Model+Status (0x0B): apiKeyID != nil && model != ""

索引键的设计原理：
  索引键的值均为 nil（仅键存在），查询时通过迭代器的键提取 tsNano/counter，
  然后反查摘要行获取实际数据。这样避免数据冗余，同时保持索引查找效率。
*/
func (b *AccessLogBatch) setV2Keys(summary AccessLogSummary, tsNano, counter uint64) error {
	jsonData, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("marshal log summary %d: %w", counter, err)
	}
	if err := b.batch.Set(encodeSummaryKey(tsNano, counter), jsonData, nil); err != nil {
		return err
	}

	suffix := func(prefix []byte) []byte {
		return appendTimeIDSuffix(prefix, tsNano, counter)
	}
	if summary.ApiKeyID != nil {
		if err := b.batch.Set(suffix(apiKeyIndexPrefix(*summary.ApiKeyID)), nil, nil); err != nil {
			return err
		}
	}
	if err := b.batch.Set(suffix(statusIndexPrefix(summary.StatusCode)), nil, nil); err != nil {
		return err
	}
	if summary.Model != "" {
		if err := b.batch.Set(suffix(modelIndexV2Prefix(summary.Model)), nil, nil); err != nil {
			return err
		}
	}
	if summary.ApiKeyID != nil && summary.Model != "" {
		if err := b.batch.Set(suffix(apiKeyModelIndexPrefix(*summary.ApiKeyID, summary.Model)), nil, nil); err != nil {
			return err
		}
	}
	if summary.ApiKeyID != nil {
		if err := b.batch.Set(suffix(apiKeyStatusIndexPrefix(*summary.ApiKeyID, summary.StatusCode)), nil, nil); err != nil {
			return err
		}
	}
	if summary.Model != "" {
		if err := b.batch.Set(suffix(modelStatusIndexPrefix(summary.Model, summary.StatusCode)), nil, nil); err != nil {
			return err
		}
	}
	if summary.ApiKeyID != nil && summary.Model != "" {
		if err := b.batch.Set(suffix(apiKeyModelStatusIndexPrefix(*summary.ApiKeyID, summary.Model, summary.StatusCode)), nil, nil); err != nil {
			return err
		}
	}
	return nil
}

/*
deleteV2Keys 删除一条记录对应的所有 V2 键，并更新统计（反向增量）。
此处删除的键与 setV2Keys 写入的键完全对称：摘要行 + 7 个索引键。
删除后调用 stageSummaryStat(summary, -1) 将统计贡献撤回。
*/
func (b *AccessLogBatch) deleteV2Keys(summary AccessLogSummary, tsNano, counter uint64) error {
	if err := b.batch.Delete(encodeSummaryKey(tsNano, counter), nil); err != nil {
		return err
	}

	suffix := func(prefix []byte) []byte {
		return appendTimeIDSuffix(prefix, tsNano, counter)
	}
	if summary.ApiKeyID != nil {
		if err := b.batch.Delete(suffix(apiKeyIndexPrefix(*summary.ApiKeyID)), nil); err != nil {
			return err
		}
	}
	if err := b.batch.Delete(suffix(statusIndexPrefix(summary.StatusCode)), nil); err != nil {
		return err
	}
	if summary.Model != "" {
		if err := b.batch.Delete(suffix(modelIndexV2Prefix(summary.Model)), nil); err != nil {
			return err
		}
	}
	if summary.ApiKeyID != nil && summary.Model != "" {
		if err := b.batch.Delete(suffix(apiKeyModelIndexPrefix(*summary.ApiKeyID, summary.Model)), nil); err != nil {
			return err
		}
	}
	if summary.ApiKeyID != nil {
		if err := b.batch.Delete(suffix(apiKeyStatusIndexPrefix(*summary.ApiKeyID, summary.StatusCode)), nil); err != nil {
			return err
		}
	}
	if summary.Model != "" {
		if err := b.batch.Delete(suffix(modelStatusIndexPrefix(summary.Model, summary.StatusCode)), nil); err != nil {
			return err
		}
	}
	if summary.ApiKeyID != nil && summary.Model != "" {
		if err := b.batch.Delete(suffix(apiKeyModelStatusIndexPrefix(*summary.ApiKeyID, summary.Model, summary.StatusCode)), nil); err != nil {
			return err
		}
	}
	b.stageSummaryStat(summary, -1)
	return nil
}

/*
stageSummaryStat 在内存中暂存一条摘要行的统计贡献（写入前暂存在 stats map 中）。

工作机制：
  - 计算摘要行时间戳所属的分钟起点（minuteStartMillis）
  - 生成该摘要行的 delta（sign=+1 为新增，sign=-1 为删除）
  - 与 stats map 中已有的同分钟增量累加

暂存而非直接写入的原因：
  同一批次中同一分钟可能有多条记录，先合并再一次性写入 Pebble，
  减少 Pebble 键的读写次数，提高吞吐量。
*/
func (b *AccessLogBatch) stageSummaryStat(summary AccessLogSummary, sign int64) {
	minute := minuteStartMillis(summary.Timestamp)
	delta := b.stats[minute]
	delta.add(statDeltaFromSummary(summary, sign))
	b.stats[minute] = delta
}

/*
Commit 提交批次，原子写入所有暂存的键值对到 Pebble，并合并分钟统计。

提交流程：
  1. 获取 v2Mu 锁（保护 stats map 的读-改-写操作）
  2. 遍历 stats map 中所有受影响的分钟：
     a. 从 Pebble 中读取该分钟当前已有的统计值（readMinuteStat）
     b. 将本批次的增量（delta）与当前值合并
     c. clamp 确保无非负数
     d. 如果合并后值为零 → 删除该分钟的统计键（清理无效数据）
     e. 如果合并后值非零 → 写入更新后的统计值
  3. 调用 batch.Commit 原子提交所有键值对
  4. 如果 addTotal=true，更新全局行计数器

为什么需要 v2Mu 锁？
  同一分钟的统计可能被多个并发的批次写入修改。
  读-改-写操作不是原子的：如果两个批次同时读到同一个分钟值，
  后一个批次的写入会覆盖前一个批次的修改（lost update）。
  v2Mu 确保同一时间只有一个批次在执行统计合并，避免写冲突。
*/
func (b *AccessLogBatch) Commit(opts *pebble.WriteOptions) error {
	if b.closed || b.batch == nil {
		return nil
	}

	b.store.v2Mu.Lock()
	defer b.store.v2Mu.Unlock()

	for minute, delta := range b.stats {
		current, err := b.store.readMinuteStat(minute)
		if err != nil {
			return err
		}
		current.add(delta)
		current.clamp()
		key := encodeStatsMinuteKey(minute)
		if current.isZero() {
			if err := b.batch.Delete(key, nil); err != nil {
				return err
			}
		} else if err := b.batch.Set(key, encodeStatDelta(current), nil); err != nil {
			return err
		}
	}

	if err := b.batch.Commit(opts); err != nil {
		return err
	}
	b.batch.Close()
	b.batch = nil
	b.closed = true
	if b.addTotal && b.rows > 0 {
		b.store.AddTotal(b.rows)
	}
	return nil
}

// Close 关闭未提交的批次。已提交的批次（b.closed=true）直接返回。
func (b *AccessLogBatch) Close() {
	if b.closed || b.batch == nil {
		return
	}
	b.batch.Close()
	b.batch = nil
	b.closed = true
}

/*
readMinuteStat 从 Pebble 读取指定分钟的预聚合统计值。

这是 statsV2 O(1) 查询的关键函数：
  统计查询时，对于完全覆盖的分钟，直接调用此函数获取预计算值，
  无需遍历该分钟内的所有摘要行。

键结构: pebblePrefixStatsMinute(0x0C) + minuteMillis(8B 大端序)，共 9 字节。
值结构: 48 字节定长 stat delta 二进制编码。
*/
func (s *LogStore) readMinuteStat(minuteMillis int64) (accessLogStatDelta, error) {
	data, closer, err := s.db.Get(encodeStatsMinuteKey(minuteMillis))
	if errors.Is(err, pebble.ErrNotFound) {
		return accessLogStatDelta{}, nil
	}
	if err != nil {
		return accessLogStatDelta{}, err
	}
	defer closer.Close()
	return decodeStatDelta(data)
}

// readSummary 从 Pebble 读取单条摘要行（JSON 反序列化）。
// 键: encodeSummaryKey(tsNano, counter) — pebblePrefixSummaryTime + 时间 + ID。
func (s *LogStore) readSummary(tsNano, counter uint64) (AccessLogSummary, error) {
	data, closer, err := s.db.Get(encodeSummaryKey(tsNano, counter))
	if err != nil {
		return AccessLogSummary{}, err
	}
	defer closer.Close()
	var summary AccessLogSummary
	if err := json.Unmarshal(data, &summary); err != nil {
		return AccessLogSummary{}, err
	}
	return summary, nil
}

// summaryExists 检查给定 tsNano+counter 对应的摘要行是否已存在。
// 用于回填中避免重复写入：如果摘要行已存在，跳过该条记录。
func (s *LogStore) summaryExists(tsNano, counter uint64) bool {
	_, closer, err := s.db.Get(encodeSummaryKey(tsNano, counter))
	if err != nil {
		return false
	}
	closer.Close()
	return true
}

/*
queryV2 执行 V2 优化路径的访问日志分页查询。

查询策略：
  1. 通过 accessLogIndexPrefixForQuery 选择最优复合索引前缀
  2. 生成该前缀的 [lower, upper) 时间范围键
  3. 用 countPebbleKeys 统计总数（用于前端分页）
  4. 使用反向扫描（SeekLT + Prev，从最新开始），利用 Pebble 的字典序排序
  5. 从迭代器的键中提取摘要行数据，转换为 AccessLog 返回

为什么反向扫描？
  复合索引键按 [前缀 + tsNano(大端) + counter(大端)] 编码，
  大端序意味着时间最近的键在字典序的末尾。
  用户期望列表按时间倒序（最新在前），因此使用 SeekLT(upper) + Prev() 反向遍历。

total 计数优化：
  当无任何过滤条件（无 apiKey/model/status/时间范围）时，
  使用全局计数器 s.Total() 而非遍历计数，避免全量扫描。
*/
func (s *LogStore) queryV2(q AccessLogQuery) ([]AccessLog, int, error) {
	prefix := accessLogIndexPrefixForQuery(q)
	lower, upper := timeBoundsForPrefix(prefix, q.StartAt, q.EndAt)
	total := s.countPebbleKeys(lower, upper)
	if q.ApiKeyID == nil && q.Model == "" && q.Status == nil && q.StartAt == nil && q.EndAt == nil {
		total = s.Total()
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return nil, 0, err
	}
	defer iter.Close()

	offset := (q.Page - 1) * q.PerPage
	matches := make([]AccessLog, 0, q.PerPage)
	seen := 0

	for iter.SeekLT(upper); iter.Valid(); iter.Prev() {
		if seen < offset {
			seen++
			continue
		}

		summary, err := s.summaryFromIterator(iter)
		if err != nil {
			continue
		}
		matches = append(matches, summary.accessLog())
		seen++
		if len(matches) >= q.PerPage {
			break
		}
	}
	return matches, total, iter.Error()
}

/*
summaryFromIterator 从 Pebble 迭代器获取摘要行数据，根据键前缀采用不同策略。

键前缀启发式 (pebblePrefixSummaryTime heuristic)：
  当前的迭代器位置有两种可能：
  1. 键前缀 == pebblePrefixSummaryTime (0x04) —— 迭代器停在摘要行本身上
     → 直接反序列化 iter.Value()（摘要 JSON）
  2. 键前缀是复合索引（0x05~0x0B）—— 迭代器停在索引键上（值为 nil）
     → 从键末尾提取 tsNano 和 counter，通过 readSummary() 反查摘要行

为什么需要这个区分？
  accessLogIndexPrefixForQuery 根据查询条件选择的前缀可能是任意索引。
  当没有任何过滤条件时，前缀是 pebblePrefixSummaryTime，迭代器直接停在摘要行上，
  此时 iter.Value() 就是摘要 JSON，无需再查。
  当有过滤条件时，前缀是复合索引，此时 iter.Value() 为 nil，
  必须从键提取时间信息再反查。
*/
func (s *LogStore) summaryFromIterator(iter *pebble.Iterator) (AccessLogSummary, error) {
	if len(iter.Key()) > 0 && iter.Key()[0] == pebblePrefixSummaryTime {
		var summary AccessLogSummary
		if err := json.Unmarshal(iter.Value(), &summary); err != nil {
			return AccessLogSummary{}, err
		}
		return summary, nil
	}
	tsNano, counter := decodeIndexedTimeID(iter.Key())
	return s.readSummary(tsNano, counter)
}

// countPebbleKeys 统计 [lower, upper) 范围内的 Pebble 键数量。
// 用于 queryV2 中的分页总数计算。注意：这不是高效操作，需要遍历全部键。
func (s *LogStore) countPebbleKeys(lower, upper []byte) int {
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return 0
	}
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

/*
statsV2 执行 V2 优化路径的统计查询（按时间区间聚合）。

核心逻辑：混合使用预聚合值与逐条扫描

快速路径（完全覆盖的分钟）：
  当某个分钟完全落在统计区间内时（如查询 12:00~12:05，区间为 5 分钟桶，
  12:00 和 12:01 都是完整分钟），直接读取预聚合的分钟统计值（readMinuteStat），
  O(1) 操作。

部分分钟回退（partial-minute fallback）：
  当统计区间边界与分钟边界不对齐时（如区间只覆盖某分钟的 30 秒），
  不能使用预聚合值（预聚合是整个分钟的完整统计），
  必须退回到逐条扫描该时间范围内的摘要行，逐条累加到 BucketStat。

partialStatMinutes 决定哪些分钟需要回退：
  partialStatMinutes 将以下位置的分钟起点标记为"部分"：
    - query start_at 所在的分钟（开头可能不完整）
    - query end_at 所在的分钟（结尾可能不完整）
    - 每个 bucket 的 start 和 end 所在的分钟（桶边界可能不完整）

对于标记为"部分"的分钟，调用 addSummariesToBuckets 扫描所有摘要行并逐条归入对应桶。
对于非部分分钟，直接用 readMinuteStat 读取预聚合值，归入对应的整体桶。
*/
func (s *LogStore) statsV2(startAt, endAt int64, intervalMinutes int) ([]BucketStat, error) {
	if intervalMinutes <= 0 {
		return nil, fmt.Errorf("interval must be positive")
	}
	start := time.UnixMilli(startAt)
	end := time.UnixMilli(endAt)
	if !start.Before(end) {
		return nil, fmt.Errorf("start_at must be before end_at")
	}

	intervalMillis := int64(intervalMinutes) * int64(time.Minute/time.Millisecond)
	duration := endAt - startAt
	numBuckets := int(math.Ceil(float64(duration) / float64(intervalMillis)))
	buckets := make([]BucketStat, numBuckets)
	for i := range buckets {
		bs := startAt + int64(i)*intervalMillis
		be := bs + intervalMillis
		if be > endAt {
			be = endAt
		}
		buckets[i].Start = bs
		buckets[i].End = be
	}

	partialMinutes := partialStatMinutes(startAt, endAt, buckets)
	for minute := minuteStartMillis(startAt); minute < endAt; minute += accessLogStatsMinuteMillis {
		rangeStart := maxInt64(minute, startAt)
		rangeEnd := minInt64(minute+accessLogStatsMinuteMillis, endAt)
		if rangeStart >= rangeEnd {
			continue
		}
		if _, ok := partialMinutes[minute]; ok {
			if err := s.addSummariesToBuckets(startAt, intervalMillis, buckets, rangeStart, rangeEnd); err != nil {
				return nil, err
			}
			continue
		}
		delta, err := s.readMinuteStat(minute)
		if err != nil {
			return nil, err
		}
		if delta.isZero() {
			continue
		}
		bucketIdx := int((minute - startAt) / intervalMillis)
		if bucketIdx >= 0 && bucketIdx < len(buckets) {
			addDeltaToBucket(&buckets[bucketIdx], delta)
		}
	}
	return buckets, nil
}

/*
partialStatMinutes 计算哪些分钟是"部分覆盖"的——即统计区间与该分钟不完全重叠。

返回的 map 中的分钟不能使用预聚合值，必须回退到逐条扫描。

触发部分回退的分钟包括：
  - startAt 所在分钟：区间可能从该分钟的中间开始
  - endAt 所在分钟：区间可能在该分钟的中间结束
  - 每个 bucket 的 start 和 end 所在分钟：桶边界可能在分钟中间
*/
func partialStatMinutes(startAt, endAt int64, buckets []BucketStat) map[int64]struct{} {
	partial := map[int64]struct{}{
		minuteStartMillis(startAt): {},
		minuteStartMillis(endAt):   {},
	}
	for _, b := range buckets {
		partial[minuteStartMillis(b.Start)] = struct{}{}
		partial[minuteStartMillis(b.End)] = struct{}{}
	}
	return partial
}

/*
addSummariesToBuckets 逐条扫描摘要行并将其累加到对应的 BucketStat 桶中。

这是部分分钟回退的核心实现：
  1. 构建 pebblePrefixSummaryTime 前缀+时间范围的迭代器
  2. 遍历该范围内的每一条摘要行
  3. 根据摘要行的 Timestamp 计算应落入哪个 bucket
  4. 调用 addSummaryToBucket 累加

时间复杂度：O(范围内的摘要行数)，慢于预聚合路径。
仅在无法使用预聚合值（部分分钟）时调用。
*/
func (s *LogStore) addSummariesToBuckets(startAt, intervalMillis int64, buckets []BucketStat, rangeStart, rangeEnd int64) error {
	if rangeEnd <= rangeStart {
		return nil
	}
	endInclusive := rangeEnd - 1
	lower, upper := timeBoundsForPrefix([]byte{pebblePrefixSummaryTime}, &rangeStart, &endInclusive)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return err
	}
	defer iter.Close()

	for iter.First(); iter.Valid(); iter.Next() {
		var summary AccessLogSummary
		if err := json.Unmarshal(iter.Value(), &summary); err != nil {
			continue
		}
		if summary.Timestamp < rangeStart || summary.Timestamp >= rangeEnd {
			continue
		}
		bucketIdx := int((summary.Timestamp - startAt) / intervalMillis)
		if bucketIdx >= 0 && bucketIdx < len(buckets) {
			addSummaryToBucket(&buckets[bucketIdx], summary)
		}
	}
	return iter.Error()
}

/*
StartAccessLogV2Backfill 启动异步 V2 回填任务。

回填场景：
  当 V1 已存储了大量日志后升级到 V2，需要为 V1 主行生成对应的 V2 摘要行和索引键。
  此函数启动一个后台 goroutine，增量扫描 V1 主键，为每条记录创建 V2 键。

并发安全：
  - backfillStarted.CompareAndSwap(false, true) 确保回填只启动一次
  - 若 isAccessLogV2Ready() 已为 true，无需回填，直接返回
  - 通过 backfillCancel 支持外部取消
  - 每批 500 条或 500 次扫描后 commit（balance between throughput and crash recovery）
  - throttle 参数控制每批之间的暂停时间（异步启动时 5ms，同步调用时 0ms）
*/
func (s *LogStore) StartAccessLogV2Backfill(ctx context.Context) {
	if s.isAccessLogV2Ready() || !s.backfillStarted.CompareAndSwap(false, true) {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	s.backfillDoneMu.Lock()
	s.backfillCancel = cancel
	s.backfillDone = done
	s.backfillDoneMu.Unlock()

	go func() {
		defer close(done)
		n, err := s.backfillAccessLogV2(ctx, 0, 5*time.Millisecond)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("access_log_v2_backfill_failed", "err", err, "rows", n)
			return
		}
		if err == nil {
			slog.Info("access_log_v2_backfill_done", "rows", n)
		}
	}()
}

// stopAccessLogV2Backfill 取消运行中的回填任务，并等待其优雅退出（最多等待 2 秒）。
func (s *LogStore) stopAccessLogV2Backfill() {
	s.backfillDoneMu.Lock()
	cancel := s.backfillCancel
	done := s.backfillDone
	s.backfillDoneMu.Unlock()
	if cancel == nil || done == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		slog.Warn("access_log_v2_backfill_stop_timeout")
	}
}

func (s *LogStore) BackfillAccessLogV2(ctx context.Context) (int, error) {
	return s.backfillAccessLogV2(ctx, 0, 0)
}

/*
backfillAccessLogV2 执行增量 V2 回填（崩溃安全，支持断点续传）。

回填策略（增量扫描 + 检查点）：
  1. 读取 accessLogV2BackfillCheckpointKey 获取上次中断位置（如无则从 primaryKeyMin 开始）
  2. 从检查点开始遍历 V1 主键（pebblePrefixTime 前缀）
  3. 对每条 V1 主键记录：
     a. 解码出 tsNano 和 counter
     b. 通过 summaryExists 检查摘要行是否已存在（幂等性保证）
     c. 若不存在 → 反序列化 V1 JSON，调用 appendExisting 写入 V2 键（不含主行写入）
     d. 更新检查点为当前处理的主键
  4. 每 500 行或 500 次扫描后 flush（提交批次并创建新批次）
  5. 全部完成后：
     a. 删除 accessLogV2BackfillCheckpointKey（清理检查点）
     b. 设置 accessLogV2ReadyKey = 1（标记 V2 就绪）
     c. 设置 s.v2Ready.Store(true)（内存标记）
     d. 以 pebble.Sync 提交（确保重启后不会重新回填）

错误恢复机制：
  如果在回填过程中崩溃：
  - 已提交的批次（flush 后的数据）持久化到磁盘
  - 检查点键记录了当前批次的最后处理位置
  - 重启后从检查点继续，已处理的记录被 summaryExists 跳过

批处理参数：
  - scannedInBatch >= 500: 限制单次事务大小（避免 Pebble 批量过大）
  - b.Rows() >= 500: 已写入 500 行时提交
  - maxRows > 0: 外部可限制单次回填的行数（用于异步 throttle 场景）

为什么不写入 V1 主行？
  回填时 includePrimary=false：V1 主行已存在，只需写入 V2 摘要行 + 索引键。
*/
func (s *LogStore) backfillAccessLogV2(ctx context.Context, maxRows int, throttle time.Duration) (int, error) {
	if s.isAccessLogV2Ready() {
		return 0, nil
	}

	startKey := primaryKeyMin
	if data, closer, err := s.db.Get(accessLogV2BackfillCheckpointKey); err == nil {
		startKey = append([]byte(nil), data...)
		closer.Close()
	}

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: primaryKeyMin, UpperBound: primaryKeyMax})
	if err != nil {
		return 0, err
	}
	defer iter.Close()

	b := s.newAccessLogBatch(false)
	defer func() { b.Close() }()

	processed := 0
	scannedInBatch := 0
	var lastKey []byte

	flush := func() error {
		if b.Count() == 0 {
			return nil
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			return err
		}
		b = s.newAccessLogBatch(false)
		scannedInBatch = 0
		if throttle > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(throttle):
			}
		}
		return nil
	}

	for ok := iter.SeekGE(startKey); ok; ok = iter.Next() {
		if bytes.Equal(iter.Key(), startKey) && !bytes.Equal(startKey, primaryKeyMin) {
			continue
		}
		select {
		case <-ctx.Done():
			if err := flush(); err != nil {
				return processed, err
			}
			return processed, ctx.Err()
		default:
		}

		keyCopy := append([]byte(nil), iter.Key()...)
		tsNano, counter := decodePrimaryKey(keyCopy)
		if !s.summaryExists(tsNano, counter) {
			var log AccessLog
			if err := json.Unmarshal(iter.Value(), &log); err == nil {
				if err := b.appendExisting(&log, tsNano, counter); err != nil {
					return processed, err
				}
				processed++
			}
		}
		lastKey = keyCopy
		if err := b.batch.Set(accessLogV2BackfillCheckpointKey, lastKey, nil); err != nil {
			return processed, err
		}
		scannedInBatch++
		if scannedInBatch >= 500 || b.Rows() >= 500 {
			if err := flush(); err != nil {
				return processed, err
			}
		}
		if maxRows > 0 && processed >= maxRows {
			if err := flush(); err != nil {
				return processed, err
			}
			return processed, nil
		}
	}
	if err := iter.Error(); err != nil {
		return processed, err
	}
	if err := flush(); err != nil {
		return processed, err
	}

	done := s.db.NewBatch()
	defer done.Close()
	if err := done.Set(accessLogV2ReadyKey, []byte{1}, nil); err != nil {
		return processed, err
	}
	if err := done.Delete(accessLogV2BackfillCheckpointKey, nil); err != nil {
		return processed, err
	}
	if err := done.Commit(pebble.Sync); err != nil {
		return processed, err
	}
	s.v2Ready.Store(true)
	return processed, nil
}

// maxInt64 返回两个 int64 中较大的值。用于时间范围比较。
func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// minInt64 返回两个 int64 中较小的值。用于时间范围边界钳制。
func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
