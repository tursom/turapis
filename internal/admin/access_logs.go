package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/tursom/turapis/internal/config"
)

// getAccessLog 返回单条访问日志的完整详情，包括请求体、响应体和 provider 信息。
// 路由: GET /admin/access-logs/{id}
// 与 listAccessLogs 不同，此接口返回完整数据（不裁剪 payload 字段），
// 用于日志详情弹窗中展示完整的请求/响应内容。
func (a *Admin) getAccessLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}

	log, err := a.store.GetAccessLog(id)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, log)
}

// accessLogStats 返回时间分桶的访问统计（请求数、token 消耗），区分有/无 failover。
// 路由: GET /admin/access-logs/stats?from={ts}&to={ts}&interval={minutes}
//
// 参数:
//   from     起始时间戳（毫秒），必填
//   to       结束时间戳（毫秒），必填
//   interval 分桶间隔（分钟），选填，默认 10 分钟
//
// 后端通过 V2 的分钟级预聚合统计实现 O(桶数) 查询，避免全表扫描。
// 当 interval 不能被 1 分钟整除时，部分分钟会通过 summary 索引回退到逐条聚合。
func (a *Admin) accessLogStats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	fromRaw := q.Get("from")
	toRaw := q.Get("to")
	if fromRaw == "" || toRaw == "" {
		writeError(w, http.StatusBadRequest, "from and to query params are required")
		return
	}
	from, err := strconv.ParseInt(fromRaw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid from")
		return
	}
	to, err := strconv.ParseInt(toRaw, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid to")
		return
	}

	// 默认 interval = 10 分钟，前端图表据此计算桶数量进行渲染
	interval := 10
	if v := q.Get("interval"); v != "" {
		i, err := strconv.Atoi(v)
		if err != nil || i <= 0 {
			writeError(w, http.StatusBadRequest, "invalid interval")
			return
		}
		interval = i
	}

	// 委托给 LogStore：V2 就绪时使用 pre-aggregated 统计，否则回退 legacy 全扫
	stats, err := a.store.GetAccessLogStats(from, to, interval)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"buckets": stats,
	})
}

// listAccessLogs 返回分页、可筛选的访问日志列表。
// 路由: GET /admin/access-logs?key_id=&model=&status=&from=&to=&page=&per_page=
//
// 性能优化：列表接口会清空日志的请求/响应体字段（ClientReq, ClientResp,
// UpstreamReq, UpstreamResp），因为这些字段可能包含大量 JSON 数据（如完整的
// messages 数组），在列表中不必要展示。详情通过 getAccessLog 单独请求获取。
// 这大幅减少了列表接口的响应体积和序列化开销。
//
// 查询路由（由 LogStore 内部决策）：
//   无筛选 → V2 时间倒序扫（queryLatest 使用 cached total）
//   仅 model → model 二级索引（无需 unmarshal 即可定位）
//   有 time/apiKey/status 筛选 → V2 复合索引 或 legacy 时间扫+过滤
//   V2 就绪时优先走 V2 复合索引路径，出错时自动回退 legacy
func (a *Admin) listAccessLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := config.AccessLogQuery{}

	if v := q.Get("key_id"); v != "" {
		if id, err := strconv.Atoi(v); err == nil {
			params.ApiKeyID = &id
		}
	}
	params.Model = q.Get("model")
	if v := q.Get("status"); v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			params.Status = &s
		}
	}
	if v := q.Get("from"); v != "" {
		from, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid from")
			return
		}
		params.StartAt = &from
	}
	if v := q.Get("to"); v != "" {
		to, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid to")
			return
		}
		params.EndAt = &to
	}

	// 分页参数：非持久化游标式，直接传 page/per_page
	params.Page = 1
	if v := q.Get("page"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			params.Page = p
		}
	}
	params.PerPage = 20
	if v := q.Get("per_page"); v != "" {
		if pp, err := strconv.Atoi(v); err == nil && pp > 0 && pp <= 100 {
			params.PerPage = pp
		}
	}

	logs, total, err := a.store.QueryAccessLogs(params)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query access logs: "+err.Error())
		return
	}
	// 裁剪 payload 字段 — 列表中不需要完整的请求/响应体，
	// 这能显著减少 JSON 序列化体积（单条 body 可达数 KB）
	for i := range logs {
		logs[i].ClientReq = ""
		logs[i].ClientResp = ""
		logs[i].UpstreamReq = ""
		logs[i].UpstreamResp = ""
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"logs":     logs,
		"total":    total,
		"page":     params.Page,
		"per_page": params.PerPage,
	})
}
