# AI Router V1 技术架构

> 状态：已确认，可进入实施设计与开发阶段  
> 实现仓库：`/Users/naive/naive/realmrouter/new-api-router`  
> 文档范围：V1 架构、接口、请求生命周期、配置、数据、错误处理、目录规划与明确边界  
> 本文不包含业务代码。

## 1. 背景与目标

现有公网 New API 已承担客户鉴权、分组、额度、计费和统一接入。当前需要在其后部署一套内网 New API Router，统一管理多个提供 `kimi-k3` 的真实渠道，包括自部署、号池反代和官转资源。

这些渠道虽然提供相同模型，但在文本速度、图像输入、视频输入、稳定性、成本、工具调用和限流等方面存在差异。V1 的核心目标是：

> 在 New API 选中真实渠道之前，根据请求体结构判断本次请求需要的输入能力，严格过滤不兼容渠道，再复用 New API 原有的优先级、权重、Provider Adapter、重试、流式响应和错误处理完成调用。

V1 选择的是提供同一模型的“渠道资源”，不是在多个模型之间选择模型。内部领域对象应使用 `ChannelCandidate`、`RouteDemand` 和 `RouteDecision` 等名称，避免将当前能力误称为多模型选择。

## 2. 部署架构

```text
                         公网
                           │
                           ▼
┌──────────────────────────────────────────────┐
│ 外层 New API                                 │
│                                              │
│ - 客户鉴权、分组、额度和计费                 │
│ - 接收客户请求                               │
│ - 将 kimi-k3 指向内网 Router 渠道            │
└───────────────────────┬──────────────────────┘
                        │ 内网 HTTP + Keep-Alive
                        ▼
┌──────────────────────────────────────────────┐
│ 内层 new-api-router（本仓库）                │
│                                              │
│ 1. 复用 New API 原有入口与鉴权               │
│ 2. 匹配全局目标模型列表                      │
│ 3. 快速识别 text/image/video/unknown         │
│ 4. 过滤不兼容和本次已失败渠道                │
│ 5. priority + weight 选择渠道                │
│ 6. 复用原有 Provider Adapter 调用上游        │
│ 7. 可重试失败时执行渠道 fallback             │
│ 8. 复用原有响应处理返回外层                  │
│                                              │
│       ┌─────────────┐   ┌─────────────────┐  │
│       │ 异步路由记录│   │ 管理员 Web 面板 │  │
│       └──────┬──────┘   └────────┬────────┘  │
└──────────────┼───────────────────┼───────────┘
               │                   │
               ▼                   ▼
        New API 现有数据库    路由配置与观测
               │
               ▼
┌──────────────────────────────────────────────┐
│ 真实 K3 渠道                                 │
│ 自部署 / 号池反代 / 官转                     │
└──────────────────────────────────────────────┘
```

外层 New API 将内层 Router 配置为普通 OpenAI 兼容渠道。真实 K3 渠道全部配置在内层 Router 中。Router 不建立第二套客户体系，也不重新实现协议转发。

同机内网部署增加的普通网络时延相对模型调用可以忽略。Router 热路径必须保持无额外网络调用、无数据库查询、无媒体解码和无同步日志写入，不能形成串行判断队列。

## 3. 核心架构原则

1. **内嵌选渠，不独立决策服务**：Router 是 New API 选渠流程的增强，不是返回渠道 ID 的外部微服务。
2. **只决定渠道**：选渠后继续走 New API 原有 Relay 和 Provider Adapter。
3. **能力是硬约束**：视频请求只能进入明确标记支持视频的渠道。
4. **人工维护能力标签**：能力来自接入前验收报告，不做在线自动能力测试。
5. **配置按渠道级生效**：V1 不实现“渠道 × 模型”能力矩阵。
6. **先静态、后动态**：V1 使用能力、priority 和 weight，不做实时评分、成本评分或动态 TPM 调度。
7. **主链路优先**：观测数据允许尽力写入，任何统计故障不得阻塞客户请求。
8. **不改写客户响应**：成功及最终错误均沿用 New API 原有响应处理。

## 4. 请求生命周期

```text
收到请求
  │
  ▼
New API 原有鉴权、限流与请求体缓存
  │
  ▼
解析请求模型
  │
  ├─ 不在目标模型列表
  │    └─ 完全沿用 New API 原有选渠与 Relay
  │
  └─ 命中目标模型（例如 kimi-k3）
       │
       ▼
  Request Capability Detector
  - 查看请求路径与结构化 content part
  - 识别 text/image/video/unknown
  - 记录是否包含 tools
       │
       ▼
  生成 RouteDemand 与请求级 RouteContext
       │
       ▼
  Channel Capability Filter
  - 复用分组、模型和渠道状态等原有条件
  - 过滤未参与 Router 的渠道
  - 过滤不支持本次模态的渠道
  - 排除本次请求中已经失败的渠道
       │
       ▼
  Candidate Selector
  - 选择剩余渠道中的最高 priority
  - 同一 priority 内按 weight 选择
       │
       ▼
  New API 原有 Relay / Provider Adapter
       │
       ▼
  调用真实渠道
       │
       ├─ 成功
       │    ├─ 异步提交路由摘要与尝试记录
       │    └─ 沿用原有响应返回客户
       │
       └─ 失败
            │
            ▼
       New API 原有错误分类
            │
       ┌────┴─────────────────────────────┐
       │ 不可重试、已输出 SSE 或达到上限 │ 可重试且未输出 SSE
       ▼                                  ▼
  返回最后一次错误                 记录并排除当前渠道
                                           │
                                           ▼
                                  重新过滤并选择渠道
```

### 4.1 模态判定

判定优先级为：

```text
video > image > text
```

基本规则：

| 结构化类型 | 判定 |
|---|---|
| `video_url` | video |
| `image_url`、`image`、`input_image` | image |
| `text`、`input_text` 或字符串 content | text |
| 未识别的 content part 类型 | unknown |

必须同时支持下列两种视频结构：

```json
{
  "type": "video_url",
  "video_url": "data:video/mp4;base64,..."
}
```

```json
{
  "type": "video_url",
  "video_url": {
    "url": "data:video/mp4;base64,..."
  }
}
```

检测器必须理解 JSON 结构位置，不能全文搜索 `video_url` 字符串，否则用户普通文本中的相同字样会产生误判。

扫描完整消息历史；发现视频后立即早停，发现图片后继续确认后续是否有视频。Router 不解码、不保存媒体数据，也不为判定创建与 base64 大小成比例的新副本。

### 4.2 未知内容策略

V1 固定采用保守策略：

```text
unknown → 只允许进入 unknown_fallback=true 的完整能力渠道
```

未知类型不退回普通文本池。明确识别为视频时，只检查 `supports_video`，不会以 `unknown_fallback` 代替视频能力。

### 4.3 工具调用

V1 检测并记录请求是否包含 `tools`，渠道能力支持 `unverified/supported/unsupported` 三态。工具能力过滤开关默认关闭，因此工具状态在 V1 中不参与渠道淘汰。

未来开启时，请求模态能力和工具能力应组合过滤，而不是互相覆盖。

## 5. 核心模块

### 5.1 Router Settings

负责：

- Router 全局开关。
- 目标模型精确列表。
- 最大不同渠道尝试数。
- 未知类型策略。
- 工具过滤开关。
- Telemetry 开关。
- 数据保留天数。
- 为请求热路径提供只读内存快照。

配置保存到 New API 现有 Option/Config 系统。请求不能每次访问数据库。更新配置时写数据库并原子替换进程内快照；已经开始的请求继续使用其进入时取得的快照。

### 5.2 Request Capability Detector

负责从请求路径和请求体结构产生统一 `RouteDemand`。按协议族拆分检测器，但不承担协议转换：

```text
Detector Registry
├── OpenAI Chat Detector
├── OpenAI Responses Detector
└── Anthropic Messages Detector
```

实际启用哪些检测器，以外层 New API 到内层 Router 的真实请求样本为准。外层协议转换不能被假定为对非标准 `video_url` 字段绝对无损，开发前必须进行端到端样本验证。

### 5.3 Channel Routing Profile

描述渠道是否参与 Router 以及渠道输入能力。跟随 New API 渠道进入内存缓存，由现有渠道编辑页面管理。

建议存放在渠道现有 `OtherSettings` JSON 中，不新增渠道能力数据表。priority 和 weight 继续复用 Channel 原字段。

### 5.4 Route Context

请求级状态，仅存在于当前 Gin Context 中：

- Request ID。
- RouteDemand。
- 配置快照。
- 已尝试渠道集合。
- 当前尝试序号。
- 是否已开始响应。
- 请求和尝试计时。
- 尝试链路。

不同请求之间不共享可变 RouteContext，不使用全局串行分类队列。

### 5.5 Channel Capability Filter

在 New API 原有候选条件之上增加：

- Router 是否启用。
- 渠道是否参与 Router。
- 渠道是否满足请求模态。
- 未知请求是否匹配完整能力兜底。
- 工具能力（V1 默认不启用）。
- 渠道是否已在本次请求中失败。

过滤只读取内存渠道缓存和请求级状态，不访问数据库。

### 5.6 Candidate Selector

负责从过滤后候选中选出渠道：

1. 找到剩余渠道中的最高 priority。
2. 在同一 priority 中按 weight 选择。
3. 可重试失败后，排除刚失败的渠道。
4. 优先尝试同 priority 尚未尝试的渠道。
5. 同级耗尽后，剩余候选中的下一 priority 自然成为最高优先级。
6. 最多尝试全局配置的 N 个不同渠道。

这与 New API 当前以 retry index 直接移动优先级层级的行为存在差异。该差异只应对命中 Router 的请求生效；非目标模型保留原行为。

示例：

```text
A(priority=100) ─失败─► 排除 A
B(priority=100) ─失败─► 排除 B
C(priority=50)  ─成功─► 返回
```

### 5.7 Attempt Coordinator

负责一次真实渠道调用的生命周期：

- 记录渠道、priority、attempt index 和开始时间。
- 记录流式首个有效 SSE 事件时间。
- 记录最终耗时、状态码和可靠 usage。
- 复用 New API 原有错误分类判断是否可重试。
- 在允许 fallback 时更新请求级排除集合。
- 检查最大尝试数和响应是否已经开始。

Attempt Coordinator 不实现厂商协议，也不建立第二套错误状态码表。

### 5.8 Router Telemetry

负责异步保存路由摘要和尝试链路，为 Web 面板提供最近 7 天数据。

热路径只向有界内存队列提交事件，不等待数据库：

```text
请求完成 → 非阻塞提交事件 → 客户响应结束
                         │
                         ▼
                 后台 Worker 批量写库
```

队列满或数据库异常时允许丢弃观测事件，并增加内部丢弃计数。观测系统故障不能改变客户响应或造成请求排队。

## 6. 核心接口与数据结构

以下内容描述接口契约，不限定最终 Go 文件或语法。

### 6.1 RouteDemand

```text
RouteDemand
├── model
├── modality: text | image | video | unknown
├── has_tools
├── unknown_content_types
└── detector_family
```

### 6.2 Request Detector

```text
Detect(path, content_type, body_storage)
    → RouteDemand
    → DetectionError
```

要求：

- 复用 New API 现有 body storage。
- 不消费或破坏原请求体。
- 不解码 base64。
- 不创建与媒体体积成比例的副本。
- 支持结构化检测和视频早停。

### 6.3 ChannelRoutingProfile

```text
ChannelRoutingProfile
├── enabled
├── supports_text
├── supports_image
├── supports_video
├── unknown_fallback
└── tool_support: unverified | supported | unsupported
```

安全默认值：

```text
enabled          = false
supports_text    = true
supports_image   = false
supports_video   = false
unknown_fallback = false
tool_support     = unverified
```

建议保存时校验：`unknown_fallback=true` 的渠道应同时具备文本、图片和视频能力。

### 6.4 ChannelRouteConstraints

```text
ChannelRouteConstraints
├── required_modality
├── require_tools
├── unknown_fallback_only
└── excluded_channel_ids
```

### 6.5 ChannelCandidate

```text
ChannelCandidate
├── channel_id
├── channel_type
├── group
├── model
├── priority
├── weight
└── routing_profile
```

候选数据来自 New API 已有渠道内存缓存。

### 6.6 RouteDecision

```text
RouteDecision
├── channel_id
├── priority
├── weight
├── modality
├── selection_reason
└── attempt_index
```

V1 的 `selection_reason` 使用固定枚举：

- `highest_priority`
- `same_priority_fallback`
- `lower_priority_fallback`
- `unknown_full_capability`

### 6.7 Selector

```text
Select(candidates, route_context)
    → RouteDecision
    → NoAvailableChannel
```

### 6.8 AttemptResult

```text
AttemptResult
├── channel_id
├── attempt_index
├── priority
├── status_code
├── error_code
├── retryable
├── response_started
├── ttft_ms
├── duration_ms
├── input_tokens
├── output_tokens
└── outcome
```

`outcome` 固定为：

- `success`
- `retryable_failure`
- `terminal_failure`
- `stream_interrupted`
- `cancelled`

### 6.9 Telemetry Sink

```text
SubmitRouteSummary(summary)
SubmitAttempt(attempt)
```

要求：非阻塞、有界队列、批量写入、失败不影响主链路，且接口不接受请求正文或响应正文。

## 7. Provider 解耦

V1 不创建新的 `OpenAIProvider`、`KimiProvider` 或 `ClaudeProvider` 抽象。New API 已经在 `relay/channel/` 中拥有 Provider Adapter。再次抽象会形成重复层级。

正确依赖边界：

```text
AI Router
只认识 ChannelCandidate 与 channel_id
               │
               ▼
New API Relay 根据 Channel Type 选择现有 Adapter
               │
               ▼
具体厂商或上游 API
```

因此：

- 新增 New API 已支持协议的厂商：只新增渠道配置。
- 新厂商需要新协议：按 New API 原有方式增加 Adapter，Router 不变。
- 同一厂商多个资源地址：配置为多个 Channel。
- Router 不读取 API Key、Base URL 或具体厂商请求格式。

## 8. 配置设计

### 8.1 全局配置

建议使用独立命名空间：

```text
ai_router.enabled
ai_router.target_models
ai_router.max_attempts
ai_router.unknown_policy
ai_router.tool_filter_enabled
ai_router.telemetry_enabled
ai_router.retention_days
```

V1 默认值：

| 配置 | 默认值 |
|---|---|
| `enabled` | `false`，完成部署验证后开启 |
| `target_models` | `["kimi-k3"]` |
| `max_attempts` | `3` |
| `unknown_policy` | `full_capability_only` |
| `tool_filter_enabled` | `false` |
| `telemetry_enabled` | `true` |
| `retention_days` | `7` |

校验规则：

- 目标模型使用精确匹配，去空、去重。
- `max_attempts` 建议限制为 1–10。
- `retention_days` 建议限制为 1–30。
- V1 未知策略固定为 `full_capability_only`，不实现规则表达式。

### 8.2 渠道配置

建议存入 Channel `OtherSettings`：

```text
router
├── enabled
├── supports_text
├── supports_image
├── supports_video
├── unknown_fallback
└── tool_support
```

priority 和 weight 保持使用 New API 现有字段。

## 9. 管理接口

管理员 API 建议如下：

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/api/ai-router/settings` | 获取全局配置 |
| `PUT` | `/api/ai-router/settings` | 更新全局配置 |
| `GET` | `/api/ai-router/overview` | 查询最近 7 天统计 |
| `GET` | `/api/ai-router/requests` | 分页查询路由记录 |
| `GET` | `/api/ai-router/requests/:request_id` | 查询请求摘要和完整尝试链路 |

渠道能力不需要独立 CRUD API，直接扩展 New API 现有渠道新增、编辑和详情接口。所有接口复用现有管理员鉴权。

## 10. Web 管理页面

### 10.1 Router 页面

可操作：

- Router 总开关。
- 目标模型列表。
- 最大尝试渠道数。
- 工具能力过滤开关。
- Telemetry 开关。
- 数据保留天数。

可查看：

- 最近 7 天请求量和成功率。
- 文本、图片、视频和未知请求量。
- fallback 数量和比例。
- 各渠道请求量、成功率和平均耗时。
- 流式首事件 TTFT。
- 可靠的 input/output token 数据。
- Telemetry 丢弃数量。
- 最近路由记录和单次请求尝试链路。

### 10.2 渠道编辑页

直接在现有渠道编辑页面增加“路由能力”区域：

```text
参与 AI Router           [开关]
支持文本                 [开关]
支持图像                 [开关]
支持视频                 [开关]
未知类型兜底             [开关]
工具调用能力             [未验证 / 支持 / 不支持]

Priority                 [复用现有字段]
Weight                   [复用现有字段]
```

渠道能力由管理员根据接入前验收报告人工设置。V1 不提供在线能力测试，不根据响应自动修改标签。

## 11. 路由数据存储

V1 复用 New API 已有数据库实例，但需要持久化最近 7 天路由摘要和尝试链路。建议新增两张简单表，而不是把路由链路硬塞入常规消费日志。

### 11.1 `ai_router_requests`

主要字段：

```text
request_id
model
modality
has_tools
final_channel_id
attempt_count
fallback_used
success
final_status_code
ttft_ms
total_duration_ms
input_tokens
output_tokens
created_at
```

建议索引：

- `request_id`
- `created_at`
- `(model, created_at)`
- `(final_channel_id, created_at)`
- `(modality, created_at)`

### 11.2 `ai_router_attempts`

主要字段：

```text
request_id
attempt_index
channel_id
priority
status_code
retryable
outcome
ttft_ms
duration_ms
input_tokens
output_tokens
error_code
error_message
created_at
```

建议索引：

- `(request_id, attempt_index)`
- `(channel_id, created_at)`
- `created_at`
- `(outcome, created_at)`

数据约束：

- 仅保存路由元数据和脱敏、截断后的错误。
- 错误信息最多保存 1–2 KiB。
- 不保存 Prompt、messages、工具参数、图片/视频 URL、base64、完整请求体、完整响应体或完整上游错误响应。
- usage 缺失时保持未知，不将未知值伪装成 0。

每天定时分批删除 7 天以前的数据。数据库实现必须兼容 SQLite、MySQL 和 PostgreSQL。

## 12. TTFT 定义

V1 的 TTFT 固定定义为：

> 从 Router 开始调用真实渠道，到收到上游第一个有效 SSE 事件的时间。

约束：

- 仅流式请求记录 TTFT。
- SSE 注释、空行和心跳不算有效事件。
- 第一个有效 reasoning 或正文增量均算首事件。
- 非流式请求 TTFT 留空，只记录总耗时。
- V1 不单独保存“正文首字时间”。

## 13. 错误处理

### 13.1 429 限流

- 复用 New API 原有错误分类。
- 若可重试，排除当前渠道并优先尝试同级其他渠道。
- 不在 V1 中动态降低权重或构建实时限流评分。

### 13.2 5xx

- New API 原有规则认定可重试的 5xx 执行 fallback。
- 配置为不可重试的错误立即结束。
- Router 不维护第二套状态码规则。

### 13.3 网络错误

连接失败、DNS、连接拒绝等在尚未输出响应且原有规则允许时执行 fallback。若最终仍失败，使用 New API 现有网络错误格式；此类情况没有可原样返回的上游响应体。

### 13.4 超时

- 复用 New API 当前超时和重试策略。
- Router 不额外建立另一套业务超时。
- 外层等待时间必须覆盖内层上游调用和最多 N 次尝试的总时间。
- 外层不应与内层同时执行多轮业务重试，避免调用次数相乘。

### 13.5 400 和请求错误

请求字段错误、无效 JSON、参数错误和上下文超限通常不可通过换渠道解决，沿用 New API 原有不可重试行为。

### 13.6 无兼容渠道

例如视频请求没有任何已启用且支持视频的渠道：

- 保持严格过滤。
- 不退回文本或图片渠道。
- 沿用 New API “无可用渠道”错误响应。
- 不增加 Router 专属客户错误格式。

生产配置应始终保留至少一个低 priority、明确通过视频验收的兜底渠道。

### 13.7 SSE 中断

首个有效 SSE 事件之前发生可重试错误，可以切换渠道。

一旦已经向客户输出有效 SSE 数据，不能无感切换渠道，否则会产生内容拼接、重复 token、推理链断裂或重复工具调用。此时：

- 设置 `response_started=true`。
- 禁止 fallback。
- 沿用 New API 原有流中断处理。
- 记录 `stream_interrupted`。

### 13.8 Telemetry 异常

队列满、数据库变慢或写入失败不得改变客户响应。系统丢弃观测事件并记录内部计数。

## 14. 模块依赖关系

```text
Web AI Router 页面
        │
        ▼
Admin Controller
        │
        ├────────────► Router Settings ─────► New API Options
        │
        ├────────────► Channel Service ─────► Channel DB / Cache
        │
        └────────────► Router Query Service ► Router Log Models

Relay Request
    │
    ▼
Distribute Middleware
    │
    ├────────► Request Capability Detector
    ├────────► Router Settings Snapshot
    └────────► Channel Selection Service
                    │
                    ├────► Channel Cache
                    ├────► Capability Filter
                    └────► Candidate Selector
                                      │
                                      ▼
                              Selected Channel
                                      │
                                      ▼
                            Existing Relay Adapter
                                      │
                                      ▼
                                Real Provider
                                      │
                                      ▼
                              Attempt Coordinator
                                      │
                           ┌──────────┴──────────┐
                           ▼                     ▼
                     Retry/Fallback       Async Telemetry
```

依赖约束：

- Detector 不依赖 Provider。
- Selector 不依赖具体 Relay Adapter。
- Provider Adapter 不依赖 Router Web 页面。
- Telemetry 不参与选渠。
- Web 不直接访问数据库。
- Model 层不依赖 Service 层。
- `relaykit` 保持独立可构建。

## 15. 计划中的项目目录

以下结构表示职责边界，不要求为了目录完整性一次性创建所有文件。优先保持代码直接、集中和可测试。

```text
new-api-router/
├── router/
│   ├── relay-router.go              # 现有 Relay 路由
│   └── api-router.go                # 注册 Router 管理 API
├── middleware/
│   └── distributor.go               # 初次选渠入口，挂载路由约束
├── dto/
│   ├── channel_constraints.go       # 扩展渠道过滤约束
│   └── ai_router.go                 # Router DTO
├── setting/
│   └── ai_router_setting/
│       ├── config.go                # 全局配置和内存快照
│       └── validation.go            # 配置校验
├── service/
│   ├── channel_select.go            # 接入能力过滤和请求级排除
│   └── ai_router/
│       ├── detector.go              # 检测器入口与公共类型
│       ├── detector_openai.go       # OpenAI Chat 检测
│       ├── detector_responses.go    # Responses 检测
│       ├── detector_anthropic.go    # Anthropic 检测
│       ├── policy.go                # 目标模型与能力策略
│       ├── selector.go              # 同级优先、降级选择
│       ├── context.go               # 请求级路由状态
│       ├── attempt.go               # 尝试生命周期
│       ├── telemetry.go             # 异步事件写入
│       └── query.go                 # 面板查询服务
├── model/
│   ├── channel.go                   # 现有渠道模型
│   ├── channel_cache.go             # 缓存候选过滤
│   ├── channel_constraint.go        # 能力约束求值
│   ├── ai_router_request.go         # 路由摘要模型
│   └── ai_router_attempt.go         # 尝试记录模型
├── controller/
│   ├── relay.go                     # 接入尝试与 fallback 生命周期
│   └── ai_router.go                 # 设置和观测管理 API
├── relay/
│   └── ...                          # 复用现有 Provider Adapter
├── relaykit/
│   └── dto/
│       └── channel_settings.go      # 渠道路由能力配置
├── web/
│   └── src/
│       ├── features/ai-router/
│       │   ├── api.ts
│       │   ├── types.ts
│       │   ├── hooks/
│       │   └── components/
│       │       ├── router-settings.tsx
│       │       ├── router-overview.tsx
│       │       ├── route-records.tsx
│       │       └── route-detail.tsx
│       └── features/channels/       # 扩展现有渠道编辑页
└── docs/
    └── ai-router-v1-architecture.md # 本文
```

## 16. 性能设计与验收目标

Router 热路径必须满足：

1. 不发起额外网络调用。
2. 不查询数据库。
3. 不解码媒体 base64。
4. 不保存或复制媒体内容。
5. 复用 New API 已缓存的请求体。
6. 使用结构感知扫描和视频早停。
7. 渠道能力从内存缓存读取。
8. 全局配置从内存快照读取。
9. 路由日志异步、批量、可降级写入。
10. 不设置单线程分类队列或全局请求锁。

性能目标：

- 小请求模态检测 P95 小于 1 ms。
- 候选过滤和选渠 P95 小于 1 ms。
- Router 新增总开销 P95 小于 3 ms，不包含原本的请求体接收和真实上游耗时。
- 1000 个小请求并发时不因 Router 产生全局锁等待或同步数据库等待。

系统整体并发能力仍取决于服务器、连接池、请求体大小和真实渠道容量，但 Router 判断层不应成为排队瓶颈。

## 17. 关键技术决策

| 决策 | 原因 |
|---|---|
| 在本仓库 New API fork 内开发 | 复用成熟入口、协议、渠道、计费、流式和后台 |
| Router 内嵌选渠流程 | 避免独立决策服务及额外 RPC |
| V1 选择渠道而非模型 | 当前资源均提供 kimi-k3 |
| 全局目标模型列表 | 控制 Router 影响范围，非目标模型保持原行为 |
| 能力按渠道配置 | 当前需求清楚，避免渠道×模型矩阵 |
| 能力写入 OtherSettings | 复用渠道存储和缓存，不增加能力表 |
| 快速结构扫描 | 不解析媒体内容，降低 CPU 和内存分配 |
| 严格能力过滤 | 防止视频误入文本渠道 |
| 同级优先、失败渠道排除 | 保证真正 fallback 且优先消耗同层资源 |
| 复用 New API 错误分类 | 避免维护第二套重试语义 |
| 复用 Provider Adapter | Router 不与厂商 API 强耦合 |
| 不改写响应 | 保持外层和客户兼容性 |
| 两张简单路由表 | 支撑 7 天摘要、链路和渠道统计 |
| Telemetry 异步可丢弃 | 观测不能拖垮主请求 |
| 复用管理员权限 | 内网服务无需第二套权限 |

## 18. 潜在风险

### 18.1 外层协议转换非绝对无损

非标准 `video_url` 字段可能在转换中改变或丢失。实施前必须抓取脱敏的外层到内层请求样本进行端到端验证。

### 18.2 重试语义需要定向修改

New API 当前重试索引主要表示优先级层级；V1 需要同级其他渠道优先。必须仅对 Router 目标模型启用新选择语义，并用确定性测试保护非目标模型行为。

### 18.3 已开始的 SSE 无法 fallback

这是流协议限制。Router 只能在首个有效输出前切换，不能承诺所有流中断都无感恢复。

### 18.4 双层重试放大

如果外层和内层均重试多次，真实上游调用数会相乘。资源级 fallback 应由内层负责，外层只保留必要的连接保护。

### 18.5 观测数据规模

短时高并发可由异步队列处理；如果长期维持极高 RPS，7 天逐请求逐尝试记录可能变得很大。V1 通过有界队列保护主链路，但不承诺观测数据绝不丢失。

### 18.6 人工标签可能错误

Router 只能根据配置路由，不能证明上游真正理解视频。渠道能力仍需以接入前验收报告为准。

### 18.7 输入结构继续演进

新 content 类型必须进入 `unknown` 保守路由并在日志中可见，后续根据真实请求补充检测器。

## 19. V1 明确不做

V1 不实现：

- 多模型质量选择。
- 自然语言任务分类。
- 使用大模型参与路由决策。
- 实时速度评分参与选渠。
- 成本综合评分参与选渠。
- 动态 RPM/TPM 调度。
- 自动调整 priority 或 weight。
- 熔断、半开和自动恢复状态机。
- 在线渠道能力测试。
- 自动根据测试修改能力标签。
- Router 插件系统。
- 独立 Router 微服务。
- 第二套 Provider SDK。
- 第二套数据库实例。
- 第二套客户、用户或权限体系。
- 按客户维护独立路由策略。
- 长期统计仓库。
- P50/P90/P95 复杂历史聚合。
- Prometheus/Grafana 强依赖。
- 请求或响应内容存储。
- 视频 base64 解码或内容真实性验证。
- 配置版本发布、审批和回滚平台。

## 20. 推荐实施顺序

1. 全局配置和渠道能力数据结构。
2. 请求模态检测及回归用例。
3. 渠道能力过滤和请求级失败排除。
4. 同级优先、降级和最大尝试数。
5. SSE 首事件与尝试生命周期观测。
6. 异步路由摘要和尝试记录。
7. 管理 API。
8. 渠道编辑页路由能力区域。
9. Router 配置、概览和记录页面。
10. 集成测试、数据库兼容验证和性能验收。

开始业务代码前，应先基于当前仓库版本补充一份源码级实施计划，列出具体修改函数、数据迁移方式、测试用例和可回滚节点。
