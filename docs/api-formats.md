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
  "stream":            false      "bool (可选)"
}
```

**`input` 字段两种格式**：

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
```

**不支持的高级特性**（返回 400）：

| 字段 | 错误信息 |
|------|----------|
| `tools` | `unsupported feature: tools` |

### 3.2 非流式响应

```
HTTP 200
Content-Type: application/json
```

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

### 3.3 流式响应 (SSE)

```
Content-Type: text/event-stream
```

**完整事件流**：

```
event: response.created
data: {"response":{"id":"resp_turapis","status":"in_progress"}}

event: response.in_progress
data: {}

event: response.text.delta
data: {"delta":"Hello"}

event: response.text.delta
data: {"delta":" world"}

event: response.text.done
data: {}

event: response.output_item.done
data: {}

event: response.completed
data: {"response":{}}
```

**错误/中断**：

```
event: response.failed
data: {"error":{"message":"error message"}}

event: response.incomplete
data: {"error":{"message":"error message"}}

event: error
data: {"error":{"message":"error message"}}
```

### 3.4 特殊说明

- `response.created` 中的 `id` 为服务端生成占位 ID（非上游返回）。
- 流式响应中途故障：同其他端点，已发送数据不丢失，直接发错误事件后断开。
- `reasoning` / `audio` / `code_interpreter` / `file_search` 等高级事件类型暂不支持，上游返回这些事件时静默忽略。

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

## 8. 故障转移触发条件

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
| AuthError | 401, 403 | ❌ | 鉴权失败（不转移） |

流式请求：连接建立前按非流式逻辑重试；数据开始发送后任何错误直接断开，不重试。

---

## 9. v1 不支持特性汇总

| 特性 | 对应字段/格式 | 行为 |
|------|--------------|------|
| Tool Use / Function Calling | `tools`, `tool_choice`, `functions` | 返回 400 `unsupported_feature` |
| Thinking/Reasoning | `thinking`, `reasoning_effort` | 返回 400 `unsupported_feature` |
| 多模态 (图片/音频) | `content[].type: "image"` / `"audio"` | 返回 400 `unsupported_feature` |
| 非 text content block | Anthropic 非 `"text"` type | 返回 400 `unsupported_feature` |
