# 远程视频 URL 实施与验收计划

更新日期：2026-09-22。需求已获授权：开发、隔离测试、中文提交并推送 GitHub；**不得更新、重启生产应用**。本文是后续续接任务的事实和范围记录，验收结果完成后补充，不能把计划当成已经通过的测试。

## 基线与边界

- 开发基线：`27a33dabc7b5c77b0e4f62757c4cff4beb8775dc`，已含独立 `video_priority`。分支：`feat/video-remote-url`。
- 所有渠道未设置 `video_priority` 时，视频沿用普通 `channel.priority`；设置了视频优先级时先走该集合，失败后回退普通渠道。普通请求始终使用普通优先级。本次保留该行为。
- 生产最近已核实镜像：`new-api-video-router:7480cb505`。上线前必须重新核实，不能以源码目录推测运行版本。
- 生产开发目录 `/root/new-api-router-video` 与运行目录 `/root/new-api-router-video-runtime` 分离；不得覆盖运行目录、环境文件或数据库，不改钱包、计费、余额以及其他服务。
- 既有回退快照：`/root/new-api-router-video-runtime/rollback-20260922T100954Z`。本次不执行任何回退或部署脚本。

## 用户可配置项

统一入口：**AI Router → 视频预处理设置 → 远程视频 URL**。复用现有设置保存、版本控制和权限体系。

| 字段 | 默认 | 行为 |
| --- | --- | --- |
| `remote_url_enabled` | false | 开启后允许 HTTPS 视频来源；Data URL 行为保留 |
| `remote_url_allowlist_enabled` | false | 可先不校验域名名单，但公网地址等安全检查始终生效 |
| `remote_url_allowed_hosts` | 空列表 | 精确域名，可添加、删除、批量粘贴；启用名单校验时不能为空 |
| `remote_url_allow_unknown_size` | true | 探测不出大小时允许限额下载；关闭则拒绝未知大小 |
| `remote_url_download_timeout_seconds` | 120 | 1～600 秒，包含下载槽位等待和 HEAD/Range/GET |
| `remote_url_max_downloads` | 8 | 1～128，同时受视频任务准入和资源预算约束 |

视频文件大小复用 `max_decoded_video_mb`，单位 MiB。100 表示 104857600 字节；不等于抽帧 JPEG 总大小或改写请求体大小。已有管理员设置不自动改写。保存后新请求使用新配置，处理中请求使用原快照。

## 数据流程

1. 解析 `video_url.url`，保留来源视频标记。
2. Data URL 继续现有 Base64 路径；HTTPS 在任务准入、下载并发和临时空间预留成功后才联网。
3. 所有请求先验证地址；HEAD 获取长度。已知超限直接拒绝，不读取视频正文。
4. HEAD 不支持或缺长度时尝试 `Range: bytes=0-0`，读取合法 `Content-Range` 总长度。
5. 大小未知时执行管理员策略。对端忽略 Range 的正文不允许无限读取。
6. GET 流式写入请求专属临时文件，以实际读取字节数强制限制；超限中止并清理。取消、期限和下载异常同样清理。
7. 文件魔数与既有 ffprobe 媒体验证通过后，复用分析、帧数预算、FFmpeg 抽帧、时间戳和 JPEG 改写。
8. 交给 AI Router 的是图片请求，保留视频优先级来源；原始视频 URL 不发送到模型上游。

## 安全与资源约束

- 只允许 HTTPS/443，拒绝 URL 用户凭据、IP 直连和不合法域名；签名查询参数用于取件但不记入日志。
- 可选精确域名匹配，规范化大小写、末尾点和 IDNA；不支持通配符。开名单时空名单拒绝全部远程视频。
- DNS 的全部结果均须为可用公网地址；拒绝环回、私网、链路本地、云元数据、保留段及 IPv6 对应地址。
- 连接固定到已验证 IP，TLS 仍校验原域名；不继承环境代理，不转发网关 Authorization/Cookie。
- 每次重定向重新验证协议、端口、域名和 IP，最多 3 次。
- HEAD/Range/GET 使用统一期限和受控传输；响应头大小、压缩编码和不一致长度有明确拒绝行为。
- URL 请求体很小，临时空间预留仍按每个视频允许的最大文件大小计算；占用既有处理任务预算，并另限下载并发。
- 媒体子进程只读本地媒体，禁用网络协议和外部引用入口；不改变原有线程、像素、帧数及执行期限限制。
- 域名白名单防不了视频内容中的提示注入，不能宣称内容可信。

## 日志

复用视频处理日志及每个视频的 JSON 详情：来源类型、规范化域名、下载字节、下载耗时、失败阶段和已有错误码。由请求 ID 关联；不记录完整 URL、查询参数、签名和凭据。不增加账务字段或改动结算。

## 实施顺序与验收

1. 设置字段、默认值、前后端校验和旧配置兼容。
2. 受保护下载器及确定性测试：已知超限、未知长度、忽略 Range、长度异常、重定向、DNS 重绑定、非公网地址、慢响应、取消。
3. 接入既有视频流水线、下载槽位和空间预算，验证 Base64 回归、混合输入、清理、签名脱敏、图片路由和视频优先级。
4. Web 配置区及可读日志，七语言文案、表单保存/回读、错误和禁用状态测试。
5. 受影响 Go 测试、真实 SQLite/MySQL/PostgreSQL 持久化验证、前端测试/typecheck/lint/build。
6. 隔离应用 + 模拟模型上游端到端验收；使用用户提供的两个 R2 视频验证实际下载、ffprobe、抽帧和改写。不得调用客户付费模型或生产数据库。
7. 更新本文实际证据、中文提交并推送 `Yonghao-lucky/newapi-router`。记录候选提交及原生产镜像，不运行生产 Compose 更新。

验收视频域名：`pub-ef3013c53fb4493f8d5fd64a42eaf234.r2.dev`；文件 `IMG_6250_base64_80mb.mp4`、`IMG_6250_base64_40mb.mp4`。历史观测文件分别 59730414、29533179 字节，本次以实际重新读取结果为准。

## 实施结果与使用方式

功能已实现。Web 的域名输入复用现有 MultiSelect，支持逐项输入、批量粘贴和删除；保存版本控制、错误提示和权限继承现有表单。未新增依赖或替代公共组件。日志详情和阶段统计把来源获取统一显示为“视频获取”，并区分地址校验、HEAD、Range、下载槽位等待与下载失败。

未来更新应用后，在 AI Router 视频预处理设置中打开“启用远程视频 URL”即可接受公网 HTTPS 视频；域名白名单默认不强制。需要收紧时，添加精确域名并打开名单校验。文件上限继续使用原视频大小设置，已有生产 100 MiB 值不会被默认值覆盖。

客户端示例：

```json
{
  "model": "kimi-k3",
  "messages": [{"role": "user", "content": [
    {"type": "text", "text": "请分析这个视频"},
    {"type": "video_url", "video_url": {"url": "https://pub-ef3013c53fb4493f8d5fd64a42eaf234.r2.dev/IMG_6250_base64_40mb.mp4"}}
  ]}]
}
```

保持 HTTPS URL 的路径及签名查询参数原样用于取件，日志只保存规范化域名。探测长度不等于可信文件证明；真实读取始终受上限限制，取件后仍验证媒体内容。下载超时与整个视频处理期限同时生效，先到者中断。

## 已完成验收（2026-09-22）

### 自动化

- `GOMAXPROCS=4 go test -p 2 ./service/video_preprocess ./model -count=1`：通过，含真实 MP4、HEVC、VP9/AV1 WebM、4K、损坏媒体、外部引用拒绝、混合 Base64/HTTPS、空间准入和取消清理回归。
- `GOMAXPROCS=4 go test -p 2 ./controller ./middleware ./service/ai_router ./setting/video_preprocess_setting -count=1`：通过，已有路由和管理接口回归通过。
- `GOMAXPROCS=4 go test -p 2 -race ./service/video_preprocess -run 'TestRemote|TestDownloadSlots' -count=1`：通过。覆盖 DNS 重绑定、混合公网/内网结果、重定向、TLS、环境代理隔离、未知长度、忽略/拒绝 Range、长度不一致、分块超限、统一期限、连接取消和槽位释放。
- 数据库：`go test ./model -run '^TestVideoPreprocessDatabase$' -count=1 -v`，设置 `VIDEO_TEST_MYSQL_DSN`、`VIDEO_TEST_POSTGRES_DSN` 指向本机独立测试容器；SQLite **3.50.4**、MySQL **8.0.46**、PostgreSQL **15.19** 均通过。验证旧设置继承默认值、原资源值保留、新设置/每视频 JSON 详情反复保存及重复启动结构不变。没有新增数据库列。
- Web：`bun run test src/features/ai-router/video-processing` 共 **5 个测试文件、50 个用例全部通过**；`bun run typecheck`、受影响文件 lint 和 `bun run build` 均通过，Rsbuild 最终构建 12.3 秒。25 个新增文案键覆盖七种语言，通过统一脚本维护；复用现有组件，未新增依赖。

### 独立应用实际请求

测试应用仅监听 `127.0.0.1:13110`，独立 SQLite 数据和临时目录；模型上游为模拟服务，不使用生产渠道、客户令牌或供应商额度。

- 容器 `video-remote-url-test-app`，4 CPU / 8 GiB 上限。
- 文件位于 `/root/new-api-router-video-priority-test-runtime/remote-url`，不属于生产运行目录。
- 测试视频设置：2 个运行任务、每任务 1 个 FFmpeg 线程、100 MiB 文件上限、8 张目标帧、120 秒下载期限、300 秒处理期限。这里的实测耗时不是生产吞吐基准。
- 验收脚本 `video-remote-url-acceptance.py`，结果 `acceptance-results.jsonl` 同目录保留。

**13 项全部通过，0 失败**，总计 156.066 秒；完整真实下载仅 3 次（两条示例及开启白名单后重测小视频）。

| 场景 | 实际结果 |
| --- | --- |
| 80mb 文件名示例 | HTTP 200，59730414 字节，下载 2000 ms，8 帧，经视频优先级选 A |
| 40mb 文件名示例、流式 | HTTP 200，29533179 字节，下载 1382 ms，8 帧，经视频优先级选 A |
| 开启精确白名单的小视频 | HTTP 200，规范化域名生效，8 帧 |
| Base64 回归 | HTTP 200，4 帧，选 A |
| 视频 A/B 上游 502 | A → B → C，最终 HTTP 200 |
| 配置保存/读回/运行时版本 | 相同设置及版本立即生效 |
| 不在白名单、HTTP、IP 直连、关闭远程功能 | HTTP 400，无模型上游调用 |
| 开启名单但保存空列表 | HTTP 400，原配置与版本不变 |
| 临时将文件上限调为 1 MiB | HEAD 后 HTTP 413，视频正文字节 0，无模型上游调用 |
| 签名脱敏 | 响应与日志均不包含测试签名或完整 URL |
| 资源清理 | 运行/等待任务、FFmpeg、媒体进程、临时文件字节均为 0，日志完整落库 |

可复核请求：大视频 `202609221127480841257548268d9d6WMP3w6f6`；小视频流式 `202609221129029252226148268d9d6rViNjPBY`；超限 HEAD `202609221130234059956988268d9d6Ai5qZEH4`。这些是隔离测试请求，不是生产客户消费记录。

### 生产与发布边界

测试期间核对生产容器 `new-api-video-router-app` 仍为 `new-api-video-router:7480cb505`，启动时间 `2026-09-21T02:19:30.831530332Z`、状态 healthy。本次没有重启/更新生产，也没有修改生产设置、数据库或计费。代码完成中文提交后推送 GitHub；候选提交及镜像身份在交付记录中给出。

生产更新仍被禁止，必须等待用户另行明确指令。关闭远程 URL 设置可以关闭此能力；完整应用回退使用原有同栈镜像及已保存配置，当前不得执行。
