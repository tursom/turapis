# Turapis API 格式文档

## 概述

Turapis 是一个 AI API 透明聚合代理。对外提供三种 AI API 协议入口和一组 Admin 管理接口，所有请求在统一端口（默认 `:8080`）上通过 PATH 区分。

所有 AI API 请求通过内部统一格式（UnifiedRequest → UnifiedResponse）转发到上游厂商，支持协议转码和透明故障转移。

---

## 1. POST /v1/messages — Anthropic Messages API

兼容 Anthropic Messages API (`https://api.anthropic.com/v1/messages`)。

### 1.1 请求格式

```
Content-Type: application/json
```

```json
{
  "model":         "string (必填)",
  "messages": [
    {
      "role":    "string (必填) — user | assistant",
      "content": [
        {
          "type": "string — text (v1 仅支持 text)",
          "text": "string — 消息正文"
        }
      ]
    }
  ],
  "system":        "string (可选) — 系统提示词",
  "max_tokens":    0,        "int (必填) — 最大输出 token 数",
  "temperature":   null,     "float | null (可选) — 0.0 ~ 1.0",
  "top_p":         null,     "float | null (可选)",
  "stop_sequences": [],      "[]string (可选) — 停止序列",
  "stream":        false     "bool (可选) — 是否流式输出"
}
```

**v1 限制**：`messages[].content[]` 仅支持 `"type": "text"`。发送 `"type": "image"` / `"type": "tool_use"` / `"type": "tool_result"` 会返回错误。

**不支持的高级特性**（返回 400）：

| 字段 | 错误信息 |
|------|----------|
| `tools` | `unsupported feature: tools/tool_use` |
| `tool_choice` | `unsupported feature: tool_choice` |
| `thinking` | `unsupported feature: thinking` |

### 1.2 非流式响应

```
HTTP 200
Content-Type: application/json
```

```json
{
  "id":          "string — msg_xxxxxxxxxxxxx",
  "model":       "string — 实际使用的模型名",
  "role":        "assistant",
  "content": [
    {
      "type": "text",
      "text": "string — 回复正文"
    }
  ],
  "stop_reason": "string — end_turn | max_tokens | stop_sequence",
  "usage": {
    "input_tokens":  0,
    "output_tokens": 0
  }
}
```

### 1.3 流式响应 (SSE)

```
Content-Type: text/event-stream
```

**正常流程**：

```
event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}

event: message_stop
data: {"type":"message_stop"}
```

**错误/中断**：

```
event: error
data: {"error": "error message"}
```

### 1.4 特殊处理

- 流式响应中途故障：已发送的 token 不丢失，连接直接断开。不重试、不回滚。
- **OpenAI system message 转换**：`/v1/chat/completions` 中的 `role: "system"` 消息会提取到 Anthropic 的顶层 `system` 字段。多个 system 消息用 `\n` 连接。
- **stop_reason 映射**：`end_turn` (Anthropic) ↔ `stop` (OpenAI)；`max_tokens` (Anthropic) ↔ `length` (OpenAI)

---

## 2. POST /v1/chat/completions — OpenAI Chat Completions API

兼容 OpenAI Chat Completions API (`https://api.openai.com/v1/chat/completions`)。

### 2.1 请求格式

```
Content-Type: application/json
```

```json
{
  "model":        "string (必填)",
  "messages": [
    {
      "role":    "string (必填) — system | user | assistant",
      "content": "string (必填) — 消息正文"
    }
  ],
  "max_tokens":   0,         "int (可选)",
  "temperature":  null,      "float | null (可选) — 0.0 ~ 2.0",
  "top_p":        null,      "float | null (可选)",
  "stop":         [],        "[]string (可选)",
  "stream":       false      "bool (可选)"
}
```

**不支持的高级特性**（返回 400）：

| 字段 | 错误信息 |
|------|----------|
| `tools` | `unsupported feature: tools/function_calling` |
| `tool_choice` | `unsupported feature: tool_choice` |
| `functions` | `unsupported feature: functions` |

**v1 注意**：`/v1/chat/completions` 仅透传纯文本对话。需要 Tool Use 的客户端应使用 `/v1/responses` 端点。

### 2.2 非流式响应

```
HTTP 200
Content-Type: application/json
```

```json
{
  "id":      "string — chatcmpl-xxxxxxxxxxxxx",
  "object":  "chat.completion",
  "model":   "string — 实际使用的模型名",
  "choices": [
    {
      "index": 0,
      "message": {
        "role":    "assistant",
        "content": "string — 回复正文"
      },
      "finish_reason": "string — stop | length"
    }
  ],
  "usage": {
    "prompt_tokens":     0,
    "completion_tokens": 0,
    "total_tokens":      0
  }
}
```

### 2.3 流式响应 (SSE)

```
Content-Type: text/event-stream
```

**正常流程**（每行以 `data: ` 开头）：

```
data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":null}]}

data: {"id":"chatcmpl-xxx","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: [DONE]
```

**错误/中断**：

```
event: error
data: {"error": "error message"}
```

### 2.4 特殊处理

- **system 消息提取**：OpenAI 的 `role: "system"` 消息会转为 Unified 的 `System` 字段。多个 system 消息拼接。
- **流式故障**：上游断流时已输出的 token 不丢失，直接发送 `[DONE]` 结束。

---

## 3. POST /v1/responses — OpenAI Responses API

兼容新版 OpenAI Responses API (`https://api.openai.com/v1/responses`)。供 Codex 等新版客户端使用。

**支持工具调用（Tool Use）**：从 v1.2 起支持 `tools` 字段，包括嵌套的 MCP namespace 工具（自动展平为 OpenAI Chat Completions 格式再转发给上游，响应时重建 namespace 结构）。

### 3.1 请求格式

```
Content-Type: application/json
```

```json
{
  "model":             "string (必填)",
  "input":             "string | array (必填) — 纯文本或消息数组",
  "instructions":      "string (可选) — 系统指令",
  "max_output_tokens": 0,         "int (可选)",
  "temperature":       null,      "float | null (可选)",
  "top_p":             null,      "float | null (可选)",
  "stream":            false,     "bool (可选)",
  "tools":             [...],     "array (可选) — 工具定义",
  "tool_choice":       "auto | any | none | {...}  (可选)"
}
```

**`input` 字段三种格式**：

```json
// 格式 1: 纯字符串
{ "input": "Hello, how are you?" }

// 格式 2: 消息数组（支持结构化 content）
{
  "input": [
    {
      "role": "user",
      "content": [
        { "type": "input_text", "text": "Hello" }
      ]
    }
  ]
}

// 格式 3: 带 function_call 历史（多轮工具调用）
{
  "input": [
    { "role": "user", "content": "run ls" },
    { "role": "assistant", "content": [{"type": "output_text", "text": "Let me check..."}] },
    { "name": "shell", "call_id": "call_1", "arguments": "{\"command\":\"ls\"}" },
    { "call_id": "call_1", "output": "\"file1.txt\"" }
  ]
}
```

**`tools` 字段格式**：接受三种来源格式，内部统一转为 OpenAI Chat Completions 格式：

| 格式 | 特征 | 来源 |
|------|------|------|
| OpenAI Chat | `{"type":"function","function":{"name":"...","parameters":{...}}}` | ChatGPT / SDK |
| Responses API | `{"type":"function","name":"...","parameters":{...}}` | Codex |
| Anthropic | `{"name":"...","input_schema":{...}}` | Claude |

**特殊字段处理**：
- `namespace` 类型工具自动展平：`mcp__mcp_server_mysql__mysql_query → {name: "mysql_query", namespace: "mcp__mcp_server_mysql"}`，响应时重建完整名称
- `web_search` 类型工具自动提取 `web_search_options` 配置
- `custom` 类型工具（如 `apply_patch`）静默跳过，不影响请求

**系统指令处理**：
- `instructions` 字段截断至 4000 字符（避免上游 token 限制）
- 自动追加 "Always give a brief text response before executing any tool" 提示
- `role: "developer"` 的 input 消息合并进系统 prompt

### 3.2 非流式响应

```
HTTP 200
Content-Type: application/json
```

**纯文本响应**：

```json
{
  "id":     "string — resp_xxxxxxxxxxxxx",
  "model":  "string — 实际使用的模型名",
  "output": [
    {
      "type": "message",
      "role": "assistant",
      "content": [
        {
          "type": "output_text",
          "text": "string — 回复正文"
        }
      ]
    }
  ],
  "usage": {
    "input_tokens":  0,
    "output_tokens": 0
  }
}
```

**含工具调用的响应**：

```json
{
  "id":     "string — resp_xxxxxxxxxxxxx",
  "model":  "string — 实际使用的模型名",
  "output": [
    {
      "type": "message",
      "role": "assistant",
      "content": [
        { "type": "output_text", "text": "Let me run that command for you." }
      ]
    },
    {
      "type": "function_call",
      "id": "fc_1",
      "call_id": "call_01_xxx",
      "name": "shell",
      "arguments": "{\"command\":\"ls\"}",
      "namespace": "mcp__mcp_server_shell"
    }
  ],
  "usage": {
    "input_tokens":  50,
    "output_tokens": 30
  }
}
```

- 每个 `function_call` 输出项包含 `namespace` 字段（如上游返回了嵌套 namespace）
- `call_id` 用于后续 `function_call_output` 输入项的匹配

### 3.3 流式响应 (SSE)

```
Content-Type: text/event-stream
```

**纯文本流程**：

```
event: response.created
data: {"type":"response.created","response":{"id":"resp_turapis","status":"in_progress"}}

event: response.in_progress
data: {"type":"response.in_progress","response":{"id":"resp_turapis","status":"in_progress"}}

event: response.text.delta
data: {"type":"response.text.delta","item_id":"msg_xxx","output_index":0,"delta":"Hello"}

event: response.text.delta
data: {"type":"response.text.delta","item_id":"msg_xxx","output_index":0,"delta":" world"}

event: response.text.done
data: {"type":"response.text.done","item_id":"msg_xxx","output_index":0,"text":"Hello world"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"id":"msg_xxx","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hello world"}]}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_turapis","model":"gpt-4","output":[...],"usage":{"input_tokens":10,"output_tokens":5}}}
```

**含工具调用的流程**：

```
event: response.created
data: {"type":"response.created","response":{"id":"resp_turapis","status":"in_progress"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"id":"msg_xxx","type":"message","role":"assistant","content":[]}}

event: response.text.delta
data: {"type":"response.text.delta","item_id":"msg_xxx","output_index":0,"delta":"Let me run that."}

event: response.text.done
data: {"type":"response.text.done","item_id":"msg_xxx","output_index":0,"text":"Let me run that."}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"id":"fc_1","type":"function_call","name":"shell","arguments":"","namespace":"mcp__mcp_server_shell","call_id":"call_01_xxx"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"command\":\"ls\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":1,"arguments":"{\"command\":\"ls\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":1,"item":{"id":"fc_1","type":"function_call","name":"shell","arguments":"{\"command\":\"ls\"}","namespace":"mcp__mcp_server_shell","call_id":"call_01_xxx"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_turapis","model":"gpt-4","output":[...],"usage":{...}}}
```

- `response.function_call_arguments.delta` 用于流式发送工具参数（适配 DeepSeek 逐字符输出）
- `response.function_call_arguments.done` 发送最终完整参数
- 所有事件均包含 `output_index` 和 `item_id`（Codex 工具调度依赖这些字段）
- `function_call` 输出项包含 `namespace`（从上游 namespace 工具重建）

**错误/中断**：

```
event: response.failed
data: {"type":"error","error":{"type":"ModelError","message":"error message"}}

event: response.incomplete
data: {"type":"error","error":{"type":"ServerError","message":"error message"}}

event: error
data: {"error":{"message":"error message"}}
```

### 3.4 内部转换说明

**消息顺序保证**：连续 assistant + tool_use 消息在转码前自动合并，确保 tool 消息紧跟在 tool_calls 消息后（DeepSeek / OpenAI 严格校验要求）。纯文本 assistant 消息也会被吸收进前方的 tool_use 消息中。

**工具格式转换路径**：
```
Codex Responses API → normalizeToolsToOpenAI → OpenAI Chat Completions 格式 → 上游 Provider
```
- Responses API 的 `{"type":"function","name":"...","parameters":{...}}` 自动包裹为 `{"type":"function","function":{"name":"...","parameters":{...}}}`
- Anthropic 的 `{"name":"...","input_schema":{...}}` 自动转为 OpenAI 格式
- MCP namespace（`"type":"namespace"`）递归展平为多个独立的 function

**流式事件转码路径**：
```
上游 SSE (OpenAI Chat) → StreamEventToUnified → Responses SSE 事件
```
- OpenAI `tool_calls` delta → `response.function_call_arguments.delta`
- OpenAI `tool_calls` done → `response.output_item.added` + `response.function_call_arguments.done`
- delta 累积缓冲：DeepSeek 的逐字符 delta 在内部累积，`text.done` 时才输出完整 text

**上游 Provider 兼容**：
- 仅 OpenAI 协议 Provider 可处理工具调用（Anthropic Provider 仍返回 400）
- 使用 `supported_tools` 字段标记 Provider 是否支持工具调用
- 不支持 web_search 的 Provider 自动回退到本地 SearXNG 搜索引擎

---

## 4. GET /v1/models — 模型列表

```
HTTP 200
Content-Type: application/json
```

```json
{
  "object": "list",
  "data": []
}
```

当前返回空列表。模型发现通过 Admin API 触发，结果存储在 SQLite 中，后续可扩展此端点返回已发现的模型。

---

## 5. GET /health — 健康检查

```
HTTP 200
Content-Type: application/json
```

```json
{
  "status": "ok"
}
```

---

## 6. Admin API — /admin/*

所有 Admin 端点挂载在 `/admin` 路径下。Response body 均为 JSON。

### 6.1 错误响应格式（通用）

```json
{
  "error": "string — 错误描述"
}
```

### 6.2 Provider 管理

#### POST /admin/providers — 创建 Provider

```json
// Request
{
  "name":      "string (必填) — 唯一名称",
  "base_url":  "string (必填) — 上游 API 地址，如 https://api.openai.com/v1",
  "api_key":   "string (必填) — API 密钥",
  "protocol":  "string (必填) — openai | anthropic",
  "auth_mode": "api_key | oauth (可选，默认 api_key) — 鉴权模式",
  "priority":  100,       "int (可选，默认 100) — 数字越小优先级越高",
  "enabled":   true       "bool (可选，默认 true)"
}

// Response (201 Created)
{
  "id":         1,
  "name":       "my-provider",
  "base_url":   "https://api.example.com/v1",
  "api_key":    "sk-***",
  "protocol":   "openai",
  "priority":   1,
  "enabled":    true,
  "created_at": "2026-05-12T12:00:00Z",
  "updated_at": "2026-05-12T12:00:00Z"
}
```

#### GET /admin/providers — 列出所有 Provider

```
HTTP 200
```

```json
[
  {
    "id":         1,
    "name":       "my-provider",
    "base_url":   "https://api.example.com/v1",
    "api_key":    "sk-***",
    "protocol":   "openai",
    "priority":   1,
    "enabled":    true,
    "created_at": "2026-05-12T12:00:00Z",
    "updated_at": "2026-05-12T12:00:00Z"
  }
]
```

按 `priority ASC` 排序。

#### GET /admin/providers/{id} — 获取单个 Provider

```
HTTP 200
```

```json
{
  "id":         1,
  "name":       "my-provider",
  "base_url":   "...",
  "api_key":    "sk-***",
  "protocol":   "openai",
  "priority":   1,
  "enabled":    true,
  "created_at": "...",
  "updated_at": "..."
}
```

404: `{"error": "provider N not found"}`

#### PUT /admin/providers/{id} — 更新 Provider

```json
// Request（与 POST 格式相同，包含 id）
{
  "id":       1,
  "name":     "my-provider",
  "base_url": "...",
  "api_key":  "new-key",
  "protocol": "openai",
  "priority": 2,
  "enabled":  true
}

// Response (200)
{ ... 更新后的完整 Provider 对象 ... }
```

更新后自动同步 Registry：旧的 Provider 实例被移除，新实例注册。**热生效，无需重启**。

#### DELETE /admin/providers/{id} — 删除 Provider

```
HTTP 200
```

```json
{
  "status": "deleted"
}
```

级联删除关联的 `model_mappings` 和 `provider_models`。

### 6.3 模型映射管理

#### POST /admin/model-mappings — 创建模型映射

```json
// Request
{
  "model_name":  "string (必填) — 模型名，如 claude-sonnet-4-20250514",
  "provider_id": 1,        "int (必填) — Provider ID",
  "priority":    100,      "int (可选，默认 100)",
  "enabled":     true      "bool (可选，默认 true)"
}

// Response (201)
{
  "id":          1,
  "model_name":  "claude-sonnet-4-20250514",
  "provider_id": 1,
  "priority":    100,
  "enabled":     true,
  "created_at":  "2026-05-12T12:00:00Z"
}
```

#### GET /admin/model-mappings — 列出所有映射

```
HTTP 200
```

```json
[
  {
    "id": 1,
    "model_name": "claude-sonnet-4-20250514",
    "provider_id": 1,
    "priority": 100,
    "enabled": true,
    "created_at": "..."
  }
]
```

按 `model_name, priority ASC` 排序。

#### PUT /admin/model-mappings/{id} — 更新模型映射

```json
// Request
{
  "model_name":  "claude-sonnet-4-20250514",
  "provider_id": 2,
  "priority":    50,
  "enabled":     true
}

// Response (200)
{ ... 更新后的完整对象 ... }
```

#### DELETE /admin/model-mappings/{id} — 删除模型映射

```
HTTP 200
```

```json
{
  "status": "deleted"
}
```

### 6.4 模型发现

#### POST /admin/providers/{id}/discover — 触发自动模型嗅探

调用上游 Provider 的 `/v1/models`（OpenAI）或 `/v1/models`（Anthropic）端点，将发现的模型存入 `provider_models` 表。

```
HTTP 200
```

```json
{
  "provider": "my-provider",
  "models": [
    { "id": 1, "model_id": "gpt-4o", "model_name": "gpt-4o" },
    { "id": 2, "model_id": "gpt-4o-mini", "model_name": "gpt-4o-mini" }
  ],
  "count": 2
}
```

### 6.5 全局设置

#### GET /admin/settings — 获取全局设置

```
HTTP 200
```

```json
{
  "default_priority_chain": "[\"provider-a\",\"provider-b\"]"
}
```

#### PUT /admin/settings — 更新全局设置

```json
// Request
{
  "default_priority_chain": "[\"provider-a\",\"provider-b\"]"
}

// Response (200)
{
  "status": "updated"
}
```

`default_priority_chain` 为 JSON 数组字符串，定义当某模型没有专属映射时使用的全局默认优先级链。

### 6.6 服务状态

#### GET /admin/status — 服务状态

```
HTTP 200
```

```json
{
  "status":           "ok",
  "provider_count":   2,
  "registered_count": 2,
  "providers": [
    {
      "name":              "provider-a",
      "enabled":           true,
      "discovered_models": 15,
      "registered":        true
    }
  ]
}
```

---

## 7. 优先级链解析规则

路由 `POST /v1/*` 时：

1. 从请求中提取 `model` 字段
2. 查询 `model_mappings` 表中 `model_name = <model>` 且 `enabled = 1` 的记录
3. **若找到**：按 `priority ASC` 排序，返回关联的 Provider 列表
4. **若未找到**：使用 `global_settings.default_priority_chain` 中定义的 Provider 名称列表
5. **兜底**：返回所有 `enabled = 1` 的 Provider，按 `priority ASC` 排序

同一 Provider 在优先级链中只出现一次。

---

## 8. 鉴权与故障转移

### 8.1 API Key 鉴权

所有 AI API 端点（`/v1/*`）支持可选的 Bearer Token 鉴权：

| Header | 行为 |
|--------|------|
| 无 `Authorization` | 放行（向后兼容），响应头 `X-Api-Key-Auth: missing` |
| `Authorization: Bearer eyJ...` (JWT) | JWT 透传（不校验），响应头 `X-Api-Key-Auth: jwt-passthrough` |
| `Authorization: Bearer sk-...` | 查询 `api_keys` 表校验，不匹配返回 401 |

### 8.2 故障转移触发条件

| 错误类别 | HTTP 状态码 | 触发转移 | 说明 |
|----------|-------------|----------|------|
| QuotaExhausted | 429 + body 含 "quota"/"exhausted"/"insufficient" | ✅ | 额度耗尽 |
| RateLimit | 429（非 quota） | ✅ | 限速 |
| ServerError | 5xx | ✅ | 服务器错误 |
| Timeout | context deadline / net timeout | ✅ | 超时 |
| EmptyResponse | 空 body | ✅ | 空响应 |
| FormatError | JSON 解析失败 | ✅ | 格式错误 |
| ModelUnavailable | 404 + body 含 "model" | ✅ | 模型不可用 |
| Unknown | 其他 | ✅ | 未知错误 |
| AuthError | 401, 403 | ✅ | 鉴权失败（如 key 过期/被吊销，转移后可用备用 key） |

流式请求：连接建立前按非流式逻辑重试；数据开始发送后任何错误直接断开，不重试。

---

## 9. v1 不支持特性汇总

| 特性 | 对应字段/格式 | 行为 |
|------|--------------|------|
| Tool Use (Chat) | `/v1/chat/completions` 的 `tools`, `tool_choice`, `functions` | 返回 400 `unsupported_feature` |
| Tools (Anthropic) | `/v1/messages` 的 `tools`, `tool_choice` | 返回 400 `unsupported_feature` |
| Thinking/Reasoning | `thinking`, `reasoning_effort` | 返回 400 `unsupported_feature` |
| 多模态 (图片/音频) | `content[].type: "image"` / `"audio"` | 返回 400 `unsupported_feature` |
| 非 text content block | Anthropic 非 `"text"` type | 返回 400 `unsupported_feature` |

**注意**：`/v1/responses` 端点 **支持** `tools` 字段和完整的工具调用流程（包括 MCP namespace 展平、流式工具参数传输、连续 tool_use 消息合并等）。
