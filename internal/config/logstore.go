// ───────────────────────────────────────────────────────────────────
// LogStore — 基于 Pebble 的访问日志存储引擎
// ───────────────────────────────────────────────────────────────────
//
// 设计架构：
//   LogStore 使用 Pebble（CockroachDB 的 LSM-tree 嵌入式 KV 存储）替代
//   早期的 SQLite 方案。Pebble 提供单机高性能顺序写入和范围扫描能力，
//   非常适合按时间序列存储的访问日志场景。
//
// 三大索引布局（二进制键设计）：
//   1. 主索引（按时间）: [0x01][ts_ns:8字节大端][counter:8字节大端] → JSON
//      17字节固定宽度，大端编码保证键按时间升序排列，支持 SeekLT/SeekGE
//   2. ID 反查索引: [0x02][counter:8字节大端] → ts_ns:8字节大端
//      9字节固定宽度，通过自增 ID 快速定位到主索引的时序键
//   3. 模型二级索引: [0x03][model字符串][0x00分隔符][ts_ns:8B][counter:8B] → 空值
//      模型前缀 + 0x00 分隔符实现按模型过滤的范围扫描，无需读取值
//
// 键设计核心原则：
//   - BigEndian 编码 = 字典序 = 时序序。Pebble 按字节字典序排序键，
//     大端编码的整数与时间先后顺序一致，因此 SeekLT/SeekGE 等价于
//     "时间 < T" / "时间 >= T" 的范围扫描，无需额外比较逻辑。
//   - 固定宽度键使每个索引的扫描成本可预测，无变长字段干扰。
//   - 空值在 LSM-tree 中仍然占用键空间，可通过 IterOptions 的 KeyOnly
//     或只遍历键来省去值读取开销。
//
// 查询策略路由：
//   - V2 就绪 → 优先走 V2 多索引组合路径（更高效），V2 出错时降级到 legacy
//   - Legacy 无过滤 → queryLatest（快路径，仅反向扫描 + 缓存总数）
//   - Legacy 仅模型过滤 → queryByModel（模型索引快路径，跳过非匹配键）
//   - Legacy 时间/其他过滤 → queryByTime（必须反序列化每条候选 JSON 来匹配）
//
// V1 → V2 升级路径：
//   - 启动时自动后台回填（backfill），将 Legacy 三索引入口转为 V2 多维度索引
//   - 查询和统计接口在 V2 就绪前走 legacy 代码路径，就绪后走 V2
//   - 回填完成后 legacy 路径保留作为降级兜底
//
// 迁移策略：
//   - SQLite → Pebble: migrateFromSQLite 通过 checkpoint 机制实现
//     崩溃安全的增量迁移（每 500 行提交一次，记录断点）
//   - 时间戳格式: migrateAccessLogTimestampJSON 将历史字符串格式
//     的 timestamp 字段统一转换为 int64 毫秒值

package config

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/jmoiron/sqlx"
)

// ── 二进制键布局 / Binary Key Layout ─────────────────────────────
//
// 三大索引全部使用固定或半固定宽度的二进制键，利用 Pebble 按字节字典序排序
// 的特性，使扫描操作天然等同于时间范围查询。
//
// 核心设计原则：BigEndian（大端）编码与字节排序
//   Pebble 的键比较是逐字节的字典序：从第一个字节开始，如果不同则立即分出
//   大小，与 Go 的 bytes.Compare 一致。对于 uint64 类型：
//     - LittleEndian（小端）：低位字节在前，0x0102030405060708 的字节序
//       是 [08 07 06 05 04 03 02 01]，不遵循自然大小顺序
//     - BigEndian（大端）：高位字节在前，字节序 [01 02 03 04 05 06 07 08]
//       恰好与数值大小顺序一致
//   因此 BigEndian 编码的时间戳（ts_ns）和计数器（counter）在 Pebble 键中
//   天然按时间/counter 升序排列，无需任何额外的比较器。
//
// 时间前缀的优势：
//   - 主索引键以 0x01 开头，紧接着 8 字节 BigEndian ts_ns。由于 ts_ns 越大
//     对应的时间越晚，键的字典序 = 时间的先后序。SeekLT 等价于"找小于 T
//     的最大键"，SeekGE 等价于"找大于等于 T 的最小键"。
//   - 这使按时间范围扫描（如"最近 7 天的日志"）转化为简单的迭代器操作，
//     无需反序列化键来比较时间。
//
// 固定宽度键的收益：
//   - 主索引 17 字节（1 prefix + 8 ts_ns + 8 counter）、ID 索引 9 字节
//     （1 prefix + 8 counter），所有键等长。这意味着 Pebble 的块索引
//     和布隆过滤器有可预测的密度，不会因键长变化导致空间浪费。
//
// 索引布局详情：
//
//   主索引 (Primary Index, prefix=0x01):
//     Key:   [0x01][ts_ns:8B BigEndian][counter:8B BigEndian]  (17 字节)
//     Value: JSON 编码的 AccessLog（~1-5 KB）
//     用途：按时间范围扫描，支持 queryLatest / queryByTime / stats
//
//   ID 反查索引 (ID Lookup Index, prefix=0x02):
//     Key:   [0x02][counter:8B BigEndian]  (9 字节)
//     Value: ts_ns:8B BigEndian  (8 字节)
//     用途：通过自增 ID 查找对应的时间戳，再定位到主索引获取完整 JSON。
//           用于 Get(id) 和 Cleanup 中的级联删除。
//
//   模型二级索引 (Model Secondary Index, prefix=0x03):
//     Key:   [0x03][model:N bytes][0x00 separator][ts_ns:8B][counter:8B]
//     Value: ∅（空值，仅利用键的排序）
//     用途：按模型过滤查询。0x00 是 model 字符串与时间戳之间的分隔符，
//           其值 (0x00) 小于任何 UTF-8 可打印字符，确保 model+"\x00"+ts
//           的排序在 model 前缀范围内保持一致。



// ── Pebble 键前缀常量 ──────────────────────────────────────────────
//
// 每个键的第一个字节是前缀，用于在 Pebble 的扁平键空间中划分不同的
// 逻辑索引。前缀的存在使得可以通过 LowerBound/UpperBound 在迭代器中
// 隔离不同的索引，避免跨索引扫描。
//
// 前缀分配：
//   0x00 — 元数据键（迁移标记、断点等，不参与查询）
//   0x01 — 主索引：按时间排序的 AccessLog JSON
//   0x02 — ID 反查索引：自增 ID → 时间戳
//   0x03 — 模型二级索引：模型名 → 时间戳 + 自增 ID（空值，纯键扫描）
//   0x04 — 分钟级预聚合统计数据（V2 统计快路径）
//   0x05 — API Key 索引（V2）：api_key_id → ...
//   0x06 — 状态码索引（V2）：status → ...
//   0x07 — 模型索引 V2（增强版）
//   0x08 — API Key + Model 组合索引（V2）
//   0x09 — API Key + Status 组合索引（V2）
//   0x0A — Model + Status 组合索引（V2）
//   0x0B — API Key + Model + Status 三维组合索引（V2）
//   0x0C — 预留前缀（未来扩展）
//
// 设计说明：
//   V2 索引 (0x05-0x0B) 是多维度组合索引，支持任意组合过滤查询。
//   启动时的后台回填（backfill）从 Legacy 三索引 (0x01-0x03) 派生
//   出 V2 多维索引。V1 索引在 V2 回填完成后仍保留，作为降级兜底。
const (
	pebblePrefixMeta  = 0x00
	pebblePrefixTime  = 0x01
	pebblePrefixID    = 0x02
	pebblePrefixModel = 0x03

	pebblePrefixSummaryTime            = 0x04
	pebblePrefixIndexAPIKey            = 0x05
	pebblePrefixIndexStatus            = 0x06
	pebblePrefixIndexModelV2           = 0x07
	pebblePrefixIndexAPIKeyModel       = 0x08
	pebblePrefixIndexAPIKeyStatus      = 0x09
	pebblePrefixIndexModelStatus       = 0x0A
	pebblePrefixIndexAPIKeyModelStatus = 0x0B
	pebblePrefixStatsMinute            = 0x0C
)

// LogStore 是基于 Pebble 的访问日志存储引擎，管理三个核心索引的读写。
//
// 字段说明：
//   db        — Pebble 数据库实例，所有读写操作的基础
//   nextID    — 原子自增 ID，用于生成全局唯一的日志 ID（启动时从 ID 索引恢复）
//   total     — 内存中的总条目数缓存，避免每次计数扫描全表（启动时从主索引统计）
//
// V2 回填相关字段：
//   v2Mu           — 保护回填启动的互斥锁（确保只启动一次）
//   v2Ready        — V2 多维索引是否已完全就绪（回填完成后设为 true）
//   backfillStarted — 回填 goroutine 是否已启动
//   backfillCancel  — 取消回填的 context（用于 Close 时优雅停止）
//   backfillDone    — 回填完成的信号 channel（用于 Close 时等待回填结束）
//   backfillDoneMu  — 保护 backfillDone channel 的互斥锁
type LogStore struct {
	db *pebble.DB

	nextID atomic.Uint64
	total  atomic.Int64

	v2Mu            sync.Mutex
	v2Ready         atomic.Bool
	backfillStarted atomic.Bool
	backfillCancel  context.CancelFunc
	backfillDone    chan struct{}
	backfillDoneMu  sync.Mutex
}

// OpenLogStore 打开（或创建）指定路径的 Pebble 数据库，并初始化 LogStore。
//
// 启动流程：
//   1. 打开 Pebble DB：若路径不存在则自动创建
//   2. initCounter()：扫描 ID 索引恢复自增 ID 起始值
//   3. initTotal()：通过 countTimeKeys 统计全表条目数
//   4. initAccessLogV2Ready()：检查 V2 回填是否完成，若未完成启动后台回填
//   5. migrateAccessLogTimestampJSON()：将历史字符串 timestamp 转为 int64 毫秒值
//
// 出错时返回 nil + error，调用方负责处理。
func OpenLogStore(path string) (*LogStore, error) {
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("pebble open %s: %w", path, err)
	}
	s := &LogStore{db: db}
	s.initCounter()
	s.initTotal()
	s.initAccessLogV2Ready()
	if err := s.migrateAccessLogTimestampJSON(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close flushes and closes the database.
func (s *LogStore) Close() error {
	s.stopAccessLogV2Backfill()
	return s.db.Close()
}

// DB exposes the underlying *pebble.DB for direct batch use by the writer.
func (s *LogStore) DB() *pebble.DB { return s.db }

// NextID atomically returns the next monotonic log id.
func (s *LogStore) NextID() uint64 { return s.nextID.Add(1) }

// AddTotal adjusts the in-memory total count by delta.
func (s *LogStore) AddTotal(delta int64) { s.total.Add(delta) }

// Total returns the in-memory total entry count.
func (s *LogStore) Total() int { return int(s.total.Load()) }

// ── 键编码辅助函数（公开，供 gateway writer 使用）────────────────
//
// 所有编码函数使用 BigEndian 确保键的字节序与时间/ID 的自然序一致。
// 键长度固定，使 Pebble 的块索引和布隆过滤器行为可预测。

// EncodePrimaryKey 构建 17 字节主索引键。
// 格式：[0x01][tsNano:8B BE][counter:8B BE]
func EncodePrimaryKey(tsNano uint64, counter uint64) []byte {
	k := make([]byte, 17)
	k[0] = pebblePrefixTime
	binary.BigEndian.PutUint64(k[1:9], tsNano)
	binary.BigEndian.PutUint64(k[9:17], counter)
	return k
}

// EncodeIndexKey 构建 9 字节 ID 反查索引键。
// 格式：[0x02][counter:8B BE]
// 用于通过自增 ID 查找对应时间戳。
func EncodeIndexKey(counter uint64) []byte {
	k := make([]byte, 9)
	k[0] = pebblePrefixID
	binary.BigEndian.PutUint64(k[1:9], counter)
	return k
}

// EncodeModelIndexKey builds the model secondary-index key.
// Format: [0x03][model][0x00][ts_ns:8B][counter:8B] — empty value.
func EncodeModelIndexKey(model string, tsNano uint64, counter uint64) []byte {
	n := len(model)
	k := make([]byte, 1+n+1+8+8)
	k[0] = pebblePrefixModel
	copy(k[1:], model)
	k[1+n] = 0x00
	binary.BigEndian.PutUint64(k[1+n+1:], tsNano)
	binary.BigEndian.PutUint64(k[1+n+9:], counter)
	return k
}

// EncodeTimestampValue 将纳秒时间戳编码为 8 字节 BigEndian 值。
// 用于 ID 索引键的值部分（[0x02][counter] → ts_ns:8B）。
func EncodeTimestampValue(tsNano uint64) []byte {
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, tsNano)
	return v
}

// decodePrimaryKey 从 17 字节主索引键中提取时间戳和自增 ID。
// 跳过第一个字节（0x01 前缀），解码 ts_ns (byte 1-8) 和 counter (byte 9-16)。
func decodePrimaryKey(k []byte) (tsNano, counter uint64) {
	tsNano = binary.BigEndian.Uint64(k[1:9])
	counter = binary.BigEndian.Uint64(k[9:17])
	return
}

// ── init ──────────────────────────────────────────────────────────

// ── 启动初始化 ──────────────────────────────────────────────────
//
// initCounter 从 Pebble ID 索引恢复自增计数器。
//
// 原理：ID 索引的键为 [0x02][counter:8B]，counter 是写入时通过 NextID()
// 分配的自增 ID。Pebble 按字典序排序键，大端编码使得 counter 越大的键越靠后。
// 通过 iter.Last() 获取最后一个 ID 索引键（即已分配的最大 counter），
// 解码后存入 nextID 原子变量。后续调用 NextID() 时从此值继续递增。
//
// 注意：如果数据库为空（无任何日志），iter.Last() 返回 false，nextID 保持 0。
func (s *LogStore) initCounter() {
	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: []byte{pebblePrefixID, 0, 0, 0, 0, 0, 0, 0, 0},
		UpperBound: []byte{pebblePrefixID + 1, 0, 0, 0, 0, 0, 0, 0, 0},
	})
	defer iter.Close()
	if iter.Last() {
		s.nextID.Store(binary.BigEndian.Uint64(iter.Key()[1:9]))
	}
}

// initTotal 通过遍历主索引统计全表条目数，初始化内存总数。
//
// 使用 countTimeKeys 遍历 [primaryKeyMin, primaryKeyMax) 范围的所有主索引键，
// 跳过值读取，仅计数。结果存入 total 原子变量，供 queryLatest 等快路径直接读取。
//
// 安全上限：countTimeKeys 内部有 1000 万上限，超大 DB 启动时截断。
func (s *LogStore) initTotal() {
	s.total.Store(int64(s.countTimeKeys(primaryKeyMin, primaryKeyMax)))
}

// ── 查询边界键 ───────────────────────────────────────────────────
//
// Pebble 的迭代器通过 LowerBound（包含）和 UpperBound（不包含）限定扫描范围。
// 这些预计算的边界键用于隔离不同的索引前缀和时间范围。
//
// primaryKeyMin / primaryKeyMax 定义了主索引 (0x01) 的完整键空间。
// 使用全 0 和全 0xFF 的 8 字节 counter 填充，确保覆盖所有可能的 counter 值。

var primaryKeyMin = []byte{pebblePrefixTime,
	0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}

var primaryKeyMax = []byte{pebblePrefixTime,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

// primaryKeyLowerBound 构造时间范围扫描的起始键。
// 使用 counter=0 相当于"时间 >= tsNano 的最小键"。
func primaryKeyLowerBound(tsNano uint64) []byte {
	return EncodePrimaryKey(tsNano, 0)
}

// primaryKeyUpperBound 构造时间范围扫描的结束键。
// 使用 tsNano+1 + counter=0，相当于"时间 < tsNano+1 的所有键"。
// +1 是因为主键包含时间戳后还可有更大的 counter，需要排除 tsNano+1 时刻的数据。
func primaryKeyUpperBound(tsNano uint64) []byte {
	return EncodePrimaryKey(tsNano+1, 0)
}

// ── CRUD ───────────────────────────────────────────────────────────

// Insert adds a single log entry. Called from the batch writer goroutine.
func (s *LogStore) Insert(log *AccessLog) error {
	b := s.NewAccessLogBatch()
	defer func() { b.Close() }()
	if err := b.Append(log); err != nil {
		return err
	}
	if err := b.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("commit log %d: %w", log.ID, err)
	}
	return nil
}

// queryLegacy 是 V1 查询路由，根据是否有过滤器选择最优查询策略。
//
// 路由决策树：
//   model != ""                    → queryByModel（模型索引，最高效的过滤路径）
//   model == "" && 无任何过滤器     → queryLatest（反向扫描，缓存总数）
//   model == "" && 有时间/状态过滤器 → queryByTime（主索引扫描 + 反序列化匹配）
//
// 此函数被 Query 用作 V2 不可用时的降级路径。
func (s *LogStore) queryLegacy(q AccessLogQuery) ([]AccessLog, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 || q.PerPage > 100 {
		q.PerPage = 20
	}

	if q.Model != "" {
		return s.queryByModel(&q)
	}

	noFilters := q.ApiKeyID == nil && q.Status == nil && q.StartAt == nil && q.EndAt == nil
	if noFilters {
		return s.queryLatest(&q)
	}
	return s.queryByTime(&q)
}

// Query 返回分页、过滤的访问日志（按时间倒序，最新在前）。
//
// V2 gating 机制：
//   1. 检查 isAccessLogV2Ready() — 后台回填是否已完成
//   2. 若 V2 就绪 → 调用 queryV2（多索引组合查询，性能最优）
//   3. 若 V2 调用失败 → 记录 warn 日志，降级到 queryLegacy（兜底路径）
//   4. 若 V2 未就绪 → 直接走 queryLegacy（三索引 legacy 路径）
//
// 分页参数校验：Page >= 1, PerPage ∈ [1, 100]，默认 20 条/页。
func (s *LogStore) Query(q AccessLogQuery) ([]AccessLog, int, error) {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 || q.PerPage > 100 {
		q.PerPage = 20
	}

	if s.isAccessLogV2Ready() {
		logs, total, err := s.queryV2(q)
		if err == nil {
			return logs, total, nil
		}
		slog.Warn("access_log_v2_query_failed", "err", err)
	}
	return s.queryLegacy(q)
}

// statsLegacy 是 V1 统计路径：实时扫描主索引并计算分桶统计。
//
// 算法：
//   1. 根据 startAt/endAt 和 intervalMinutes 预分配分桶（buckets）
//   2. 从主索引反向扫描（最新在前），每条 JSON 反序列化后：
//      a. 判断是否包含 failover 尝试（attempts_json 的 has_failover 标记）
//      b. 计算所在分桶索引 bucketIdx = (logTs - start) / interval
//      c. 调用 accessLogSummaryFromLog 提取摘要：tokens_in/out、status_code
//      d. 调用 addSummaryToBucket 累加到对应分桶
//
// 性能保护：
//   - maxScan = 10000：最多扫描 10000 条主索引候选。数据量巨大时可能
//     不会覆盖全部时间范围，但保证了单次请求的响应时间。
//   - 此限制同样作用于 V2 统计路径（statsV2），保持行为一致。
func (s *LogStore) statsLegacy(startAt, endAt int64, intervalMinutes int) ([]BucketStat, error) {
	if intervalMinutes <= 0 {
		return nil, fmt.Errorf("interval must be positive")
	}

	start := time.UnixMilli(startAt)
	end := time.UnixMilli(endAt)
	if !start.Before(end) {
		return nil, fmt.Errorf("start_at must be before end_at")
	}

	interval := time.Duration(intervalMinutes) * time.Minute
	duration := end.Sub(start)
	numBuckets := int(math.Ceil(float64(duration) / float64(interval)))

	buckets := make([]BucketStat, numBuckets)
	for i := range buckets {
		bs := start.Add(time.Duration(i) * interval)
		be := bs.Add(interval)
		if be.After(end) {
			be = end
		}
		buckets[i].Start = bs.UnixMilli()
		buckets[i].End = be.UnixMilli()
	}

	lower, upper, err := s.queryBounds(&startAt, &endAt)
	if err != nil {
		return nil, err
	}

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	defer iter.Close()

	const maxScan = 10000
	scanned := 0

	for iter.SeekLT(upper); iter.Valid() && scanned < maxScan; iter.Prev() {
		var log AccessLog
		if err := json.Unmarshal(iter.Value(), &log); err != nil {
			continue
		}

		logTs := time.UnixMilli(log.Timestamp)

		hasFailover := accessLogHasFailover(log.AttemptsJSON)

		bucketIdx := int(logTs.Sub(start).Minutes()) / intervalMinutes
		if bucketIdx < 0 || bucketIdx >= numBuckets {
			continue
		}

		addSummaryToBucket(&buckets[bucketIdx], accessLogSummaryFromLog(&log, hasFailover))

		scanned++
	}

	return buckets, nil
}

// Stats 返回时间范围内按 intervalMinutes 分桶的统计信息。
//
// 与 Query 相同的 V2 gating 机制：
//   - V2 就绪 → statsV2（分钟级预聚合统计，性能最优）
//   - V2 失败 → warn 日志，降级到 statsLegacy（实时扫描计算）
//   - V2 未就绪 → 直接走 statsLegacy
//
// legacy 路径 (statsLegacy) 通过 maxScan=10000 保护，
// 在数据量巨大时只统计最近 10000 条而非全量扫描。
func (s *LogStore) Stats(startAt, endAt int64, intervalMinutes int) ([]BucketStat, error) {
	if s.isAccessLogV2Ready() {
		stats, err := s.statsV2(startAt, endAt, intervalMinutes)
		if err == nil {
			return stats, nil
		}
		slog.Warn("access_log_v2_stats_failed", "err", err)
	}
	return s.statsLegacy(startAt, endAt, intervalMinutes)
}

// queryLatest 无过滤器的快速路径：直接从主索引反向扫描最新 N 条。
//
// 为什么快：
//   1. 无过滤器开销 — 不做任何 model/api_key_id/status 匹配，跳过 matchFilters
//   2. 总数用缓存 — total 从内存原子变量读取（initTotal 在启动时通过 countTimeKeys
//      统计整表），无需扫描计数
//   3. Pebble 反向扫描 — SeekLT(primaryKeyMax) + iter.Prev() 利用 LSM-tree
//      的反向遍历能力，从最新条目向旧条目遍历，恰好符合"最新 N 条"的查询语义
//
// 分页逻辑：遍历时按 offset 跳过前 (Page-1)*PerPage 条，取接下来的 PerPage 条。
func (s *LogStore) queryLatest(q *AccessLogQuery) ([]AccessLog, int, error) {
	total := s.Total()

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: primaryKeyMin,
		UpperBound: primaryKeyMax,
	})
	defer iter.Close()

	offset := (q.Page - 1) * q.PerPage
	matches := make([]AccessLog, 0, q.PerPage)
	skipped := 0

	for iter.SeekLT(primaryKeyMax); iter.Valid(); iter.Prev() {
		if skipped < offset {
			skipped++
			continue
		}
		var log AccessLog
		if err := json.Unmarshal(iter.Value(), &log); err != nil {
			continue
		}
		matches = append(matches, log)
		if len(matches) >= q.PerPage {
			break
		}
	}
	return matches, total, nil
}

// countTimeKeys 统计主索引中 [lower, upper) 范围内的键数量，不读取值。
//
// 性能优化：Pebble 迭代器在只遍历键（不调用 Value()）时，LSM-tree
// 无需从 SST 文件的 value block 中读取数据，大幅减少 IO。此处仅遍历键
// 来计数，适用于：
//   - queryByModel 中仅模型过滤时的 total 精确计算
//   - initTotal 启动时初始化内存总数
//
// 安全上限：maxScan = 10,000,000（一千万），防止极端情况下（如
// 首次全量扫描）无限循环导致 OOM。达到上限时截断计数。
func (s *LogStore) countTimeKeys(lower, upper []byte) int {
	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid() && n < 10000000; iter.Next() {
		n++
	}
	return n
}

// queryByTime 按时间范围 + 附加过滤器（api_key_id/status）查询。
//
// 为什么必须反序列化每条候选：
//   主索引只按时间排序，不包含 model/api_key_id/status 信息。过滤器匹配
//   需要完整 JSON 数据，因此每条候选都必须 json.Unmarshal + matchFilters。
//   这与 queryByModel 不同——模型索引已按 model 过滤，只反序列化匹配的候选。
//
// maxScan 限制：最多反向扫描 10000 条主索引键。当时间范围内的数据量巨大时，
// 这是保护措施——防止一次查询导致长时间阻塞。达到 maxScan 上限时，
// total 返回实际扫描数（可能小于真实总数），前端据此判断是否需要缩小查询范围。
func (s *LogStore) queryByTime(q *AccessLogQuery) ([]AccessLog, int, error) {
	lowerBound, upperBound, err := s.queryBounds(q.StartAt, q.EndAt)
	if err != nil {
		return nil, 0, err
	}

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	defer iter.Close()

	const maxScan = 10000
	offset := (q.Page - 1) * q.PerPage
	matches := make([]AccessLog, 0, q.PerPage)
	scanned := 0

	for iter.SeekLT(upperBound); iter.Valid() && scanned < maxScan; iter.Prev() {
		var log AccessLog
		if err := json.Unmarshal(iter.Value(), &log); err != nil {
			continue
		}
		if !s.matchFilters(&log, q) {
			continue
		}
		scanned++
		if scanned > offset && len(matches) < q.PerPage {
			matches = append(matches, log)
		}
	}
	return matches, scanned, nil
}

// queryByModel 使用模型二级索引查询。模型索引的键已按 model 过滤，
// 因此只有该 model 的条目才会被遍历。
//
// 两条路径（根据是否有附加过滤器选择）：
//
//   1. 仅 model 过滤（modelOnly=true）：
//      - total 通过 countTimeKeys 精确计数（仅遍历键，不反序列化值）
//      - 按 offset 跳过键，需要日志时才通过 readPrimary 查询主索引获取完整 JSON
//      - 效率最高，因为大部分被分页跳过的条目完全不需要反序列化
//
//   2. model + 附加过滤器（modelOnly=false）：
//      - 每条候选都必须 readPrimary + matchFilters，判断是否匹配
//      - total 无法预知，返回实际扫描到的匹配数
//      - 比 queryByTime 快（因为模型索引已缩小候选集），但仍需反序列化
//
// maxScan 限制：与 queryByTime 相同，最多扫描 10000 条防止阻塞。
func (s *LogStore) queryByModel(q *AccessLogQuery) ([]AccessLog, int, error) {
	lowerBound, upperBound, err := s.modelBounds(q.Model, q.StartAt, q.EndAt)
	if err != nil {
		return nil, 0, err
	}

	modelOnly := q.ApiKeyID == nil && q.Status == nil
	var total int
	if modelOnly {
		total = s.countTimeKeys(lowerBound, upperBound)
	}

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: lowerBound,
		UpperBound: upperBound,
	})
	defer iter.Close()

	const maxScan = 10000
	offset := (q.Page - 1) * q.PerPage
	matches := make([]AccessLog, 0, q.PerPage)
	scanned := 0

	for iter.SeekLT(upperBound); iter.Valid() && scanned < maxScan; iter.Prev() {
		_, counter := decodeModelIndexKey(iter.Key())
		tsNano := decodeModelIndexTimestamp(iter.Key())

		if modelOnly {
			scanned++
			if scanned > offset && len(matches) < q.PerPage {
				log, err := s.readPrimary(tsNano, counter)
				if err != nil {
					continue
				}
				matches = append(matches, *log)
			}
			continue
		}

		log, err := s.readPrimary(tsNano, counter)
		if err != nil {
			continue
		}
		if !s.matchFilters(log, q) {
			continue
		}
		scanned++
		if scanned > offset && len(matches) < q.PerPage {
			matches = append(matches, *log)
		}
	}

	if !modelOnly {
		total = scanned
	}
	return matches, total, nil
}

// readPrimary 通过主索引键（时间戳 + ID）直接读取一条完整的 AccessLog。
// 用于模型索引查询中按需获取 JSON 数据（queryByModel 快路径）。
func (s *LogStore) readPrimary(tsNano, counter uint64) (*AccessLog, error) {
	key := EncodePrimaryKey(tsNano, counter)
	data, closer, err := s.db.Get(key)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	var log AccessLog
	if err := json.Unmarshal(data, &log); err != nil {
		return nil, err
	}
	return &log, nil
}

// Get 通过日志 ID 查找单条访问日志。
//
// 两步查询（利用 ID 反查索引）：
//   1. 从 ID 索引 ([0x02][id]) 获取对应的时间戳 ts_ns
//   2. 用 (ts_ns, id) 构造主索引键，读取完整 JSON
//
// 两步查询的设计避免了在无 key 结构的 Pebble 中做全表扫描来按 ID 查找。
func (s *LogStore) Get(id int) (*AccessLog, error) {
	indexKey := EncodeIndexKey(uint64(id))
	tsVal, closer, err := s.db.Get(indexKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("access log %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get access log %d: %w", id, err)
	}
	tsNano := binary.BigEndian.Uint64(tsVal)
	closer.Close()

	primaryKey := EncodePrimaryKey(tsNano, uint64(id))
	jsonData, closer2, err := s.db.Get(primaryKey)
	if err == pebble.ErrNotFound {
		return nil, fmt.Errorf("access log %d not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get access log %d data: %w", id, err)
	}
	defer closer2.Close()

	var log AccessLog
	if err := json.Unmarshal(jsonData, &log); err != nil {
		return nil, fmt.Errorf("unmarshal access log %d: %w", id, err)
	}
	return &log, nil
}

// Cleanup 删除超过保留天数的访问日志，返回删除行数。
//
// 实现细节：
//   1. 计算截止时间 cutoffNano，构造主索引扫描范围 [primaryKeyMin, cutoffNano)
//   2. 正向遍历主索引（按时间升序，最老在前），逐条删除三个索引的键
//   3. 每 1000 条提交一次 batch（Pebble batch 有单次写入上限和内存占用控制）
//   4. 删除完成后对清理范围执行 compaction，回收磁盘空间
//   5. 更新内存中的 total 计数器（total.Add(-count)），保持一致性
//
// 注意：此操作在 V2 索引可用时也会同步删除 V2 索引键，
// 确保不会产生孤立的二级索引条目。
func (s *LogStore) Cleanup(retentionDays int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	cutoffNano := uint64(cutoff.UnixNano())

	start := primaryKeyMin
	end := primaryKeyLowerBound(cutoffNano)

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: start,
		UpperBound: end,
	})
	defer iter.Close()

	b := s.newAccessLogBatch(false)
	defer func() { b.Close() }()

	var count int64
	flush := func() error {
		if b.Count() == 0 {
			return nil
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			return err
		}
		b = s.newAccessLogBatch(false)
		return nil
	}

	for iter.First(); iter.Valid(); iter.Next() {
		keyCopy := append([]byte(nil), iter.Key()...)
		tsNano, counter := decodePrimaryKey(keyCopy)

		var log AccessLog
		_ = json.Unmarshal(iter.Value(), &log)

		_ = b.batch.Delete(keyCopy, nil)
		_ = b.batch.Delete(EncodeIndexKey(counter), nil)
		if log.Model != "" {
			_ = b.batch.Delete(EncodeModelIndexKey(log.Model, tsNano, counter), nil)
		}
		if summary, err := s.readSummary(tsNano, counter); err == nil {
			_ = b.deleteV2Keys(summary, tsNano, counter)
		}
		count++
		if b.Count() >= 1000 {
			if err := flush(); err != nil {
				return count, err
			}
		}
	}
	if err := flush(); err != nil {
		return count, err
	}

	_ = s.db.Compact(context.Background(), start, end, true)
	s.total.Add(-count)
	return count, nil
}

// ── 迁移标记键（Meta 前缀 0x00）───────────────────────────────────
//
// migrationMarkerKey    — SQLite → Pebble 迁移完成标记（写入后跳过迁移）
// migrationCheckpointKey — 迁移断点（记录已迁移的最后 SQLite ID，崩溃恢复用）
// timestampJSONMigrationKey — timestamp 字符串 → int64 迁移完成标记

var migrationMarkerKey = []byte{pebblePrefixMeta, 'm', 'i', 'g', 'r', 'a', 't', 'e', 'd'}
var migrationCheckpointKey = []byte{pebblePrefixMeta, 'm', 'i', 'g', 'c', 'h', 'k'}
var timestampJSONMigrationKey = []byte{pebblePrefixMeta, 't', 's', '_', 'm', 's'}

// legacyAccessLog 对应旧 SQLite access_logs 表的一行。
// 用于 MigrateFromSQLite 中将 SQLite 行映射到 AccessLog 结构体。
type legacyAccessLog struct {
	ID           int    `db:"id" json:"id"`
	Timestamp    string `db:"timestamp" json:"timestamp"`
	ApiKeyID     *int   `db:"api_key_id" json:"api_key_id"`
	ApiKeyName   string `db:"api_key_name" json:"api_key_name"`
	Method       string `db:"method" json:"method"`
	Path         string `db:"path" json:"path"`
	Model        string `db:"model" json:"model"`
	StatusCode   int    `db:"status_code" json:"status_code"`
	TokensIn     int    `db:"tokens_in" json:"tokens_in"`
	TokensOut    int    `db:"tokens_out" json:"tokens_out"`
	DurationMs   int    `db:"duration_ms" json:"duration_ms"`
	RemoteIP     string `db:"remote_ip" json:"remote_ip"`
	RequestID    string `db:"request_id" json:"request_id"`
	ProviderName string `db:"provider_name" json:"provider_name"`
	ErrorMsg     string `db:"error_msg" json:"error_msg"`
	RawBody      string `db:"raw_body" json:"raw_body"`
	RawResponse  string `db:"raw_response" json:"raw_response"`
	ClientReq    string `db:"client_req" json:"client_req"`
	ClientResp   string `db:"client_resp" json:"client_resp"`
	UpstreamReq  string `db:"upstream_req" json:"upstream_req"`
	UpstreamResp string `db:"upstream_resp" json:"upstream_resp"`
	QuotaBefore  string `db:"quota_before" json:"quota_before"`
	QuotaAfter   string `db:"quota_after" json:"quota_after"`
	AttemptsJSON string `db:"attempts_json" json:"attempts_json,omitempty"`
}

// accessLog 将 SQLite 行数据转换为内部 AccessLog 结构体。
// timestamp 字段通过 parseLegacyUnixMillis 解析，fallbackMillis 通常为当前时间。
func (l legacyAccessLog) accessLog(fallbackMillis int64) AccessLog {
	ts := parseLegacyUnixMillis(l.Timestamp, fallbackMillis)
	return AccessLog{
		ID:           l.ID,
		Timestamp:    ts,
		ApiKeyID:     l.ApiKeyID,
		ApiKeyName:   l.ApiKeyName,
		Method:       l.Method,
		Path:         l.Path,
		Model:        l.Model,
		StatusCode:   l.StatusCode,
		TokensIn:     l.TokensIn,
		TokensOut:    l.TokensOut,
		DurationMs:   l.DurationMs,
		RemoteIP:     l.RemoteIP,
		RequestID:    l.RequestID,
		ProviderName: l.ProviderName,
		ErrorMsg:     l.ErrorMsg,
		RawBody:      l.RawBody,
		RawResponse:  l.RawResponse,
		ClientReq:    l.ClientReq,
		ClientResp:   l.ClientResp,
		UpstreamReq:  l.UpstreamReq,
		UpstreamResp: l.UpstreamResp,
		QuotaBefore:  l.QuotaBefore,
		QuotaAfter:   l.QuotaAfter,
		AttemptsJSON: l.AttemptsJSON,
	}
}

// migrateAccessLogTimestampJSON 将历史 AccessLog 中字符串格式的 timestamp
// 字段统一转换为 int64 毫秒值。
//
// 背景：早期写入的 AccessLog JSON 中 timestamp 为字符串（如 "1700000000000"
// 或 RFC3339 格式），后续 V2 索引和批量查询统一使用 int64 毫秒值。
// 此函数一次性扫描全部主索引，将字符串 timestamp 替换为整数并重写。
//
// 幂等：通过 pebblePrefixMeta + "ts_ms" 标记键判断是否已完成迁移。
// 扫描和重写过程中每 500 行提交一次批处理，限制内存占用。
func (s *LogStore) migrateAccessLogTimestampJSON() error {
	if _, c, err := s.db.Get(timestampJSONMigrationKey); err == nil {
		c.Close()
		return nil
	}

	iter, _ := s.db.NewIter(&pebble.IterOptions{
		LowerBound: primaryKeyMin,
		UpperBound: primaryKeyMax,
	})
	defer iter.Close()

	b := s.db.NewBatch()
	defer b.Close()

	flush := func() error {
		if b.Count() == 0 {
			return nil
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			return err
		}
		b.Close()
		b = s.db.NewBatch()
		return nil
	}

	rewritten := 0
	for iter.First(); iter.Valid(); iter.Next() {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(iter.Value(), &raw); err != nil {
			continue
		}
		tsRaw, ok := raw["timestamp"]
		if !ok || len(tsRaw) == 0 || tsRaw[0] != '"' {
			continue
		}

		var tsString string
		if err := json.Unmarshal(tsRaw, &tsString); err != nil {
			continue
		}
		tsNano, _ := decodePrimaryKey(iter.Key())
		raw["timestamp"], _ = json.Marshal(parseLegacyUnixMillis(tsString, int64(tsNano/uint64(time.Millisecond))))
		jsonData, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if err := b.Set(iter.Key(), jsonData, nil); err != nil {
			return err
		}
		rewritten++
		if b.Count() >= 500 {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	b.Close()
	if err := s.db.Set(timestampJSONMigrationKey, []byte{1}, pebble.Sync); err != nil {
		return err
	}
	if rewritten > 0 {
		slog.Info("access_log_timestamp_json_migrated", "rows", rewritten)
	}
	return nil
}

// MigrateFromSQLite 从旧的 SQLite access_logs 表迁移数据到 Pebble。
//
// 崩溃安全设计（基于 checkpoint 机制）：
//   1. 迁移开始前检查 migrationMarkerKey (0x00 + "migrated")，
//      已存在则跳过（幂等）。
//   2. 读取 migrationCheckpointKey (0x00 + "migchk") 获取上次迁移
//      到的最大 SQLite ID，从该断点继续。
//   3. 每次 Append 一条日志后立即写入 checkpoint（记录当前 SQLite ID），
//      确保崩溃后可以从最后成功的 ID 恢复。
//   4. 每 500 行批量提交一次 Pebble batch。
//   5. 全部迁移完成后：
//      - 写入 migrationMarkerKey 标记迁移完成
//      - DROP TABLE access_logs 删除 SQLite 源表
//      - 删除 migrationCheckpointKey 断点键
//
// 注意事项：
//   - SQLite 为空或无 access_logs 表时静默返回。
//   - 时间戳转换：SQLite 中 timestamp 为字符串格式，通过 parseLegacyUnixMillis
//     解析为毫秒值，fallback 为当前时间。
//   - 迁移过程中启动的后台 V2 回填会索引新写入的数据（含本次迁移的数据）。
func (s *LogStore) MigrateFromSQLite(sqlDB *sqlx.DB) (int, error) {
	if _, c, err := s.db.Get(migrationMarkerKey); err == nil {
		c.Close()
		return 0, nil
	}

	var tableExists int
	if err := sqlDB.Get(&tableExists,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='access_logs'"); err != nil || tableExists == 0 {
		return 0, nil
	}

	var lastSQLiteID int64
	if val, c, err := s.db.Get(migrationCheckpointKey); err == nil {
		lastSQLiteID = int64(binary.BigEndian.Uint64(val))
		c.Close()
	}

	rows, err := sqlDB.Queryx("SELECT * FROM access_logs WHERE id > ? ORDER BY id ASC", lastSQLiteID)
	if err != nil {
		return 0, fmt.Errorf("query legacy access_logs: %w", err)
	}
	defer rows.Close()

	b := s.NewAccessLogBatch()
	defer func() { b.Close() }()
	migrated := 0

	flush := func() error {
		if b.Count() == 0 {
			return nil
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			return err
		}
		b = s.NewAccessLogBatch()
		return nil
	}

	for rows.Next() {
		var legacy legacyAccessLog
		if err := rows.StructScan(&legacy); err != nil {
			slog.Warn("pebble_migration_scan_failed", "err", err)
			continue
		}

		sqliteID := int64(legacy.ID)
		log := legacy.accessLog(time.Now().UnixMilli())
		if err := b.Append(&log); err != nil {
			continue
		}

		var ck [8]byte
		binary.BigEndian.PutUint64(ck[:], uint64(sqliteID))
		_ = b.batch.Set(migrationCheckpointKey, ck[:], nil)
		migrated++

		if b.Rows() >= 500 {
			if err := flush(); err != nil {
				return migrated, fmt.Errorf("pebble migration commit: %w", err)
			}
		}
	}
	if err := flush(); err != nil {
		return migrated, fmt.Errorf("pebble migration commit: %w", err)
	}

	if err := rows.Err(); err != nil {
		return migrated, fmt.Errorf("iterate legacy access_logs: %w", err)
	}

	if err := s.db.Set(migrationMarkerKey, []byte{1}, pebble.Sync); err != nil {
		return migrated, fmt.Errorf("write migration marker: %w", err)
	}

	if _, err := sqlDB.Exec("DROP TABLE IF EXISTS access_logs"); err != nil {
		slog.Warn("pebble_migration_drop_table_failed", "err", err)
	}

	_ = s.db.Delete(migrationCheckpointKey, pebble.Sync)

	if migrated > 0 {
		slog.Info("access_logs_migrated", "from", "sqlite", "to", "pebble", "rows", migrated)
	}
	return migrated, nil
}

// ── 辅助函数 ────────────────────────────────────────────────────────

// unixMilliToNano 将 int64 毫秒时间戳转换为 uint64 纳秒时间戳。
// 用于将 API 层的毫秒参数转换为内部 Pebble 键中使用的纳秒精度。
func unixMilliToNano(tsMillis int64) uint64 {
	return uint64(time.UnixMilli(tsMillis).UnixNano())
}

// parseLegacyUnixMillis 解析历史时间戳字符串为 int64 毫秒值。
// 支持三种格式：
//   1. 纯数字字符串 — 若值 < 10^11（2001年前）则视为秒，乘 1000 转为毫秒；
//      否则视为毫秒（UnixMillis 约 13 位，10^12 量级）。
//   2. RFC3339Nano 格式（如 "2006-01-02T15:04:05.999999999Z07:00"）
//   3. "2006-01-02 15:04:05" 格式（不带时区，按 UTC 解析）
// 解析失败时返回 fallback（通常取自主键中的纳秒时间戳）。
func parseLegacyUnixMillis(raw string, fallback int64) int64 {
	if raw == "" {
		return fallback
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n < 100000000000 {
			return n * 1000
		}
		return n
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return t.UnixMilli()
	}
	if t, err := time.Parse("2006-01-02 15:04:05", raw); err == nil {
		return t.UnixMilli()
	}
	return fallback
}

// queryBounds 根据可选的时间范围构造主索引的扫描边界。
// startAt 为 nil 时用 primaryKeyMin（最早），endAt 为 nil 时用 primaryKeyMax（最新）。
func (s *LogStore) queryBounds(startAt, endAt *int64) (lower, upper []byte, err error) {
	if startAt != nil {
		lower = primaryKeyLowerBound(unixMilliToNano(*startAt))
	} else {
		lower = primaryKeyMin
	}

	if endAt != nil {
		upper = primaryKeyUpperBound(unixMilliToNano(*endAt))
	} else {
		upper = primaryKeyMax
	}

	return
}

// matchFilters 检查一条 AccessLog 是否满足查询中的所有过滤条件。
// 只检查非 nil / 非空的条件，nil 视为"不过滤"。
func (s *LogStore) matchFilters(log *AccessLog, q *AccessLogQuery) bool {
	if q.ApiKeyID != nil && (log.ApiKeyID == nil || *log.ApiKeyID != *q.ApiKeyID) {
		return false
	}
	if q.Model != "" && log.Model != q.Model {
		return false
	}
	if q.Status != nil && log.StatusCode != *q.Status {
		return false
	}
	return true
}

// ── 模型索引边界 ─────────────────────────────────────────────────
//
// modelBounds 根据可选的 model 和 startAt/endAt 构造模型索引的扫描范围。
// 使用 modelLowerBound / modelUpperBound 作为无时间限制时的边界。

func (s *LogStore) modelBounds(model string, startAt, endAt *int64) (lower, upper []byte, err error) {
	if startAt != nil {
		lower = EncodeModelIndexKey(model, unixMilliToNano(*startAt), 0)
	} else {
		lower = modelLowerBound(model)
	}

	if endAt != nil {
		upper = EncodeModelIndexKey(model, unixMilliToNano(*endAt)+1, 0)
	} else {
		upper = modelUpperBound(model)
	}
	return
}

// modelLowerBound 构造模型索引的扫描下界。
// 键格式: [0x03][model][0x00]，其中 0x00 是 model 字符串与时间戳之间的分隔符。
// 返回键比该模型+最小时间戳对应的键还要小，保证 SeekGE 从该模型的第一条开始。
func modelLowerBound(model string) []byte {
	k := make([]byte, 1+len(model)+1)
	k[0] = pebblePrefixModel
	copy(k[1:], model)
	k[1+len(model)] = 0x00
	return k
}

// modelUpperBound 构造模型索引的扫描上界。
// 键格式: [0x03][model][0x01]，注意最后一个字节是 0x01 而非 0x00。
// 0x01 > 0x00，且介于分隔符 0x00 和下一个可打印字符之间，使得 model+0x01
// 恰好排在 model+0x00+时间戳 之后，形成精确的 model 前缀范围边界。
func modelUpperBound(model string) []byte {
	k := make([]byte, 1+len(model)+1)
	k[0] = pebblePrefixModel
	copy(k[1:], model)
	k[1+len(model)] = 0x01
	return k
}

// decodeModelIndexKey 从模型索引键中提取时间戳和自增 ID。
// 通过定位 0x00 分隔符，跳过变长 model 字段后读取固定宽度的 ts_ns 和 counter。
func decodeModelIndexKey(k []byte) (tsNano, counter uint64) {
	nullPos := bytes.IndexByte(k, 0x00)
	tsNano = binary.BigEndian.Uint64(k[nullPos+1:])
	counter = binary.BigEndian.Uint64(k[nullPos+9:])
	return
}

// decodeModelIndexTimestamp 从模型索引键中提取纳秒时间戳。
// 键格式: [prefix:1B][model:N][0x00:1B][ts_ns:8B][counter:8B]
// 通过定位 0x00 分隔符，直接跳过变长 model 部分读取时间戳。
func decodeModelIndexTimestamp(k []byte) (tsNano uint64) {
	nullPos := bytes.IndexByte(k, 0x00)
	return binary.BigEndian.Uint64(k[nullPos+1:])
}
