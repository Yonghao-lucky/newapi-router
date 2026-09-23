# Chat Completions 响应模型名统一化

## 2026-09-23 渠道级配置（当前行为）

本节取代下文首版的全局启用语义。开发基线 `7bd1798c5`，分支 `feat/channel-response-model-alias`。本次用户已授权测试后推送并更新生产 13000 应用，保留回退镜像；其他服务、数据库、计费和视频配置不在修改范围。

进入 **AI Router → 渠道配置 → 对应渠道的配置 → 响应模型名还原**：

- **渠道单独配置**：默认选项；“需要还原的模型”标签留空表示关闭。添加 `kimi-k3` 后保存，仅该渠道处理的对应客户模型响应会还原。
- **跟随全局设置**：必须主动选择并保存，才使用“AI Router → 全局设置 → 响应模型名还原”的模型标签。
- 所有旧渠道缺少新配置时一律关闭，即使全局已经配置也不自动启用。无需批量写库或删除全局配置。要关闭一个已跟随全局的渠道，切换至渠道单独配置并清空标签。
- 标签精确匹配客户模型名，区分大小写。客户调用 `kimi-k3`，命中时返回 `kimi-k3`；不是任意名称映射。
- 配置仅作用于 AI Router 的 Chat Completions，涵盖非流式与流式。只改已有的顶层字符串 `model`，其余字段及上游请求不变。

存储使用既有渠道 `settings` JSON 中的 `response_model_alias: {mode: "channel" | "global", models: []}`，独立于 `router`。设置此功能不会创建 Router profile；旧管理客户端只更新 Router 时保留该设置。没有数据库迁移。

每次实际选中渠道后决定本次尝试的规则；切换到未启用的渠道会清除上次尝试的规则。全局列表在请求开始时取得快照；流式事件沿用已选中渠道的规则，不逐事件查询配置或数据库。原有最终写出替换器保持不变，没有新增整段流缓冲。

本次验证涵盖旧配置默认关闭、渠道开启/关闭/跟随全局、精确匹配、非法配置拒绝、保存回显、Router profile 独立、其他渠道不受影响、切换渠道清除规则、JSON/SSE 其余字节保护，以及既有 Stop/usage/取消/视频路由回归。前端复用既有 SideDrawerSection、Select、MultiSelect 和表单保存流程；新增文案覆盖七种语言。

实际通过：`GOWORK=off go test ./dto`、`GOWORK=off go build ./...`（relaykit），根模块 common/settings/relay/controller/service 回归及相关 race 检查。`TestAIRouterChannelManagementDatabaseMatrix` 在 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 15.19 均通过，使用下文相同隔离 DSN；没有 schema 修改。前端 AI Router 三文件 18 项测试通过，最终修改后相关设置 10 项再次通过；typecheck、涉及五文件 oxlint（0 warning/error）、生产构建通过；七语言 missingCount 均为 0。测试数据没有使用真实客户或付费上游。

## 首版记录（历史，仅供追溯）

## 范围与工作边界

- 基线：`8f23cfb55284967104c579cbea2122328c640451`；开发分支：`feat/chat-response-model-alias`。
- 仅在本地隔离开发、测试；本次不更新生产应用、不重启容器、不修改生产配置，也不推送或合并主分支。
- 目的：客户以 `kimi-k3` 调用 AI Router 时，将正常 Chat Completions 响应顶层 `model` 从供应商名称（例如 `FW-Kimi-K3`）恢复为客户请求名称。
- 不修改发给上游的模型、路由/视频优先级、Stop、usage、工具调用、推理内容、超时/取消、钱包或计费逻辑。不启用 ForceFormat，也不补造缺失字段。

## 实施计划

1. AI Router 全局配置增加 `response_model_aliases` 模型列表，默认 `[]`（关闭）。Web 复用现有 MultiSelect 标签输入组件；添加 `kimi-k3` 并保存即启用，删除标签并保存即关闭。精确区分大小写匹配客户请求模型；每个匹配模型恢复为自身名称，无通配符或模型名相互映射。
2. 在 AI Router 开始处理时固定本次请求的配置，仅 `/v1/chat/completions` 生效。普通 Relay、原生 Responses/Claude/Gemini 接口不启用；重试、切换渠道和请求期间的后台修改不改变本次响应名称。
3. 在现有客户端最终写出函数接入局部替换，不改上游解析和 usage 收集。非流式只替换完整 JSON 的顶层 model 字符串；重新计算 Content-Length。流式逐个现有 SSE JSON 事件替换，包含网关生成的 usage/Stop 尾事件；保持原有写出、Flush 和取消检查，不增加整段流缓冲。
4. 严格保持 model 值之外的字节（包括空白、字段顺序、数字字面量及扩展字段）。不修改嵌套 model 或字符串里的模型名。缺少 model、非字符串 model、重复顶层 model、无效 JSON、错误对象、心跳、`[DONE]` 均原样处理。已有 model 与目标相同也不重写。
5. 使用现有 option 存储，无表结构变更。验证真实 SQLite、MySQL、PostgreSQL 的保存、读取和重新加载。
6. 测试通过后记录实际结果并创建中文本地提交，交付未来上线后的 Web 用法；生产部署另行授权。

## 验收

- 非流式与 SSE 的开/关结果对比：仅顶层 model 值改变；正文、reasoning、工具参数、usage（含缺失）、结束原因、扩展字段保持一致。
- 多 chunk、末尾 usage、网关补充 usage、Stop 跨 chunk、ForceFormat、thinking_to_content 组合回归。
- 默认关闭、不匹配、其他协议、同名、异常/重复字段及嵌套字段保护。
- 确定性验证 SSE 在上游结束前已输出；取消、超时和写出失败仍走原流程。
- 保存配置、回显、删除标签关闭、旧版本缺字段默认关闭；七种语言齐全，前端测试/类型检查/lint/构建。
- 使用固定真实大小的 JSON/SSE fixture 进行 benchmark，报告新增开销，不承诺零开销。

## 结果

2026-09-22 已完成本地实现和隔离验证。未连接生产服务器，未重启/更新任何业务容器，未推送 GitHub。测试没有使用生产数据库或真实付费上游。

### Web 使用

以后部署本分支后，进入 **AI Router → 全局设置 → 响应模型名还原**：

1. 输入或选择 `kimi-k3`，按 Enter 确认成为标签，再点击保存。
2. 对实际进入 AI Router 的 `/v1/chat/completions` 请求生效；文本、图片和抽帧后的视频请求共用该处理。即使切换渠道，也恢复为本次请求原始名称。
3. 非流式完整响应和流式各事件中已有的顶层字符串 `model` 会还原；没有 `model` 不添加。原生 Responses/Claude/Gemini 和普通 Relay 不启用。
4. 删除标签并保存即可关闭；默认空列表。保存影响后续请求，进行中的请求使用原配置快照。

复用了 `web/src/components/multi-select.tsx`、现有标签行为、设置保存 API 和 option 表；按项目 shadcn-ui、React 和 i18n 规范完成，没有新建通用控件、引入依赖或修改公共控件。新增两条文案已提供七种语言，i18n 同步后各语言 missingCount 均为 0。

### 实际验证

| 验证 | 结果 |
| --- | --- |
| 原始字节保护 | 顶层 model 以外字节保持一致，包括空白、字段顺序、超大整数、浮点数字面量、reasoning、工具参数和扩展字段 |
| 非流式/SSE 11 组处理器对照 | 通过：普通响应、Stop、ForceFormat、thinking_to_content、组合模式、上游 usage、补充 usage、未请求 usage |
| 流式时序 | 管道测试确认上游尚未 EOF 时已经输出还原后的首个事件；没有新增缓冲（处理器原有的一事件缓冲保留） |
| 异常边界 | 无效 JSON、重复顶层 model、缺失/非字符串 model、错误对象、非成功 HTTP 响应、心跳和 DONE 不被改写 |
| 终止行为 | 取消、无进展超时 504、写出失败回归通过；未更改预算、退款、结算代码 |
| 配置 | 默认关闭、精确匹配、其他协议排除、快照隔离、修改后新请求生效、非法类型拒绝通过 |
| 数据库 | 真实 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 15.19 上保存/读取/通过启动解析器重新发布通过；测试事务回滚，没有新增表结构 |
| 后端回归 | common、AI Router settings、relay 全部子包、controller、service 全部子包通过；含视频处理和路由既有测试 |
| race | 新增核心/处理器测试及 Stop、请求预算相关回归通过 |
| 前端 | 修改涉及的设置测试 8/8 通过，typecheck 通过，涉及文件 lint 0 warning/0 error，构建通过 |
| 应用构建 | 根模块编译通过；构建产物仅在 `/tmp/newapi-chat-response-model-alias`，未启动或部署 |

关键命令（工作目录为本分支工作树）：

```sh
# 第一项矩阵使用两个既有隔离测试库，DSN 从测试环境传入。
# MySQL: 127.0.0.1:13316；PostgreSQL: 127.0.0.1:15439；库名 video_priority_test。
go test ./controller -run TestAIRouterChannelManagementDatabaseMatrix -count=1 -v
GOMAXPROCS=4 go test -p 2 ./common ./setting/ai_router_setting ./relay/... ./controller ./service/... -skip '^TestAIRouterChannelManagementDatabaseMatrix$' -count=1
GOMAXPROCS=4 go test -race -p 2 ./common ./setting/ai_router_setting ./relay/channel/openai -run 'TestChatResponseModel|TestResponseModelAlias|TestVisibleStop|TestRouterResponse|TestRouter.*Cancel|TestRouterWrite' -count=1
GOMAXPROCS=4 go build -p 2 -o /tmp/newapi-chat-response-model-alias .
GOMAXPROCS=2 go test ./common -run '^$' -bench '^BenchmarkChatResponseModel$' -benchmem -count=1
# 以下命令在 web/ 下、限制 4 CPU / 8 GiB 的临时 Bun 1.4.0 容器中执行。
bun run i18n:sync
bun run test src/features/ai-router/__tests__/retry-settings.test.tsx
bun run typecheck
bunx oxlint -c .oxlintrc.json src/features/ai-router/global-config.tsx src/features/ai-router/types.ts src/features/ai-router/__tests__/retry-settings.test.tsx
bun run build
```

数据库矩阵需设置 `AI_ROUTER_TEST_MYSQL_DSN` 与 `AI_ROUTER_TEST_POSTGRES_DSN`；未设置时相应引擎会跳过，本次执行均已设置且实际通过。

### 性能与边界

本地 Go 1.25.1 / AMD EPYC 7K62 / GOMAXPROCS=2，固定 JSON fixture 的微基准：

| 模式 | 关闭 | 启用 | 启用时分配 |
| --- | ---: | ---: | ---: |
| 小 SSE（128 字节正文加 JSON 外壳） | 17.96 ns/事件 | 843.5 ns/事件（约 0.84 μs） | 224 B，1 次 |
| 非流式（32 KiB 正文加 JSON 外壳，字节数组入口） | 18.03 ns/响应 | 79,420 ns/响应（约 0.079 ms） | 122,880 B，3 次 |

这是本机局部改写函数的测量，不是生产负载下的端到端延迟保证。处理成本随单个事件/响应大小增长；开启后只扫描当前已有 JSON，不增加外部请求，不读取数据库，不等待后续 SSE，也不复制整段历史输出。关闭路径不解析 JSON、无内存分配。

本次不修复供应商缺 usage、空正文或原有可选格式化造成的结构变化；测试比较同一原有配置下启用/关闭本功能的结果，确认只增加 model 值的变化。没有进行真实供应商调用或生产压测，不能承诺所有第三方自定义协议都已覆盖。
