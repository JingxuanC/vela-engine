# AiToEarn 一键分发架构参考 — 对 Vela AI 社交模块的启示

> 作者：Hermes Agent 分析  
> 日期：2026-06-10  
> 目的：借鉴 AiToEarn 成熟的多平台一键分发机制，优化 Vela AI 现有社交发布模块

---

## 1. AiToEarn 一键分发架构总览

AiToEarn 是 Electron 桌面应用，核心能力是**一次编写内容，同时发布到多个平台**（抖音、小红书、快手、视频号）。架构分为**前端 → Electron IPC → 发布控制器 → 并行分发器 → 平台适配层 → 各平台 HTTP API** 五个层级。

### 组件图（简化）

```
[前端 React]  ←IPC/Event→  [VideoPubController]  →  [PublishService (PubRecord)]
                                   │
                                   ▼
                            [PlatController.videoPublish()]
                                   │
                              Promise.all()
                          ┌───────┼───────┐
                          │       │       │
                     [抖音]  [小红书]  [视频号] ...
                    PubItemVideo  ...     ...
                          │
                    PlatformBase.videoPublish()
                     (带进度回调)
                          │
              windowOperate.sendRenderMsg()
               → 实时进度推送到前端
```

### 关键特性

| 特性 | 说明 |
|------|------|
| **双层记录模型** | PubRecord = 发布任务（一次操作），VideoModel = 每个平台的结果（N条） |
| **并行分发** | `Promise.all()` 同时向所有选中平台发起发布 |
| **回调式进度** | 每个 `PlatformBase.videoPublish()` 接受 callback，实时推 IPC 到渲染层 |
| **状态聚合** | 全部成功→RELEASED，全部失败→FAIL，部分成功→PartSuccess，审核中→Audit |
| **差异化参数** | `diffParams` JSON 字段存储各平台独有参数（抖音热点、视频号声明原创等） |
| **代理 IP 注入** | 发布前从 account group 读取代理 IP 注入到每个 videoModel |

---

## 2. 核心流程拆解

### 2.1 数据流

```
                         pubRecord (1条)
                     /         |         \
              videoModel    videoModel    videoModel
              (抖音)        (小红书)       (视频号)
                  全部并行发布 ──── Promise.all()
                       │
                 每个 videoModel.status 独立更新
                       │
                 聚合状态 → 更新 pubRecord.status
```

### 2.2 状态机

```
                    ┌──────────┐
                    │ UNPUBLISH│ (0 - 草稿/未发布)
                    └────┬─────┘
                         │ 执行发布
                         ▼
                ┌──────────────────┐
                │  并行分发开始     │
                │  Promise.all()    │
                └────────┬─────────┘
                         │
              ┌──────────┼──────────┐
              ▼          ▼          ▼
          全部成功    部分成功    全部失败
              │          │          │
              ▼          ▼          ▼
          RELEASED  PartSuccess    FAIL
             (1)        (3)        (2)
                         │
                         ▼
                      Audit (4 - 审核中, 有些平台需要审核流程)
```

### 2.3 关键节点分解

| 节点 | 文件 | 行号 | 说明 |
|------|------|------|------|
| **入口** | `publish/video/controller.ts` | 57 | `@Icp('ICP_PUB_VIDEO') pubVideo()` 接收 IPC 调用 |
| **查询** | same file | 59-68 | 加载 PubRecord + 关联的 videoModel 列表 |
| **代理注入** | same file | 75-82 | 从 account group 读取 proxyIp 注入到 videoModel |
| **分发** | `plat/index.ts` | 79-103 | `videoPublish()` 遍历 videoModel，创建 PubItemVideo，`Promise.all()` |
| **单条执行** | `plat/pub/PubItemVideo.ts` | 44-90 | `publishVideo()` 调用 platform.videoPublish() + 进度回调 |
| **平台基类** | `plat/PlatformBase.ts` | 233-237 | `abstract videoPublish(params, callback)` |
| **状态聚合** | `publish/video/controller.ts` | 87-101 | 统计成功数，判定最终状态 |
| **记录更新** | same file | 102 | 写回 PubRecord 状态 |
| **进度推送** | `pub/PubItemVideo.ts` | 59 | `windowOperate.sendRenderMsg(SendChannelEnum.VideoPublishProgress, args)` |

---

## 3. Vela AI 当前社交模块 vs. AiToEarn 差异对比表

| 维度 | AiToEarn | Vela AI 当前 | 差距 |
|------|----------|--------------|------|
| **架构位置** | Electron 桌面端 + IPC | Go 后端 REST API | — |
| **平台数量** | 4 个 (抖音/小红书/快手/视频号) | 1 个 (Pinterest) | Vela 需增加平台 |
| **发布模式** | 一键多平台并行 (Promise.all) | 单平台单次 (每个请求发 1 个平台) | **核心差距** |
| **发布记录** | 双层: PubRecord + VideoModel | 单层: SocialPost | 无"发布任务"概念 |
| **状态模型** | UNPUBLISH/RELEASED/FAIL/PartSuccess/Audit | "published"/"draft" 字符串 | 缺少 PartSuccess 和 Audit 状态 |
| **进度推送** | IPC 实时进度回调 → 前端 | 无 (同步 HTTP 等待) | 大文件上传时体验差 |
| **差异化参数** | `diffParams` JSON (每个平台独有参数) | `PublishRequest` 固定字段 | 不可扩展 |
| **定时发布** | `timingTime` 字段 + 后端 cron | `ScheduledAt` 字段存在但无调度执行器 | Vela 有 schema 但没实现 |
| **代理 IP** | group 级别代理注入 | 无 | 对 TikTok/Instagram 等有风控的平台必要 |
| **图文发布** | `PubItemImgText` 与视频并列 | `PublishRequest` 只有 ImageURL | 不支持图文混排 |
| **OAuth** | cookie/loginCookie 方式 | OAuth 2.0 标准 (AuthURL/ExchangeCode) | AiToEarn 弱，Vela 强 |
| **运行环境** | Electron 本地进程 | Go 服务端 | 后端天然可做异步队列 |

### Vela AI 已经具备的优势

1. **OAuth 流程完整**：`PlatformPublisher` 接口已定义 `AuthURL` / `ExchangeCode` / `Publish` 三元组
2. **Token 加密存储**：`CryptoService` 加密 token 落库，安全性好
3. **Schema 有扩展空间**：`SocialPost` 已有 `ScheduledAt`、`Status`、`ErrorMessage` 字段
4. **ContentJob 模型**：已有 `ContentJob` 表（job_type / platform_targets / status），这实际是一键分发的雏形

---

## 4. 可借鉴的设计要点（6 项）

### 4.1 双层发布记录模型（最高优先级）

AiToEarn 把"一次发布操作"拆成两层：

```
PubRecord          ── 发布任务（1条）
  ├─ videoModel    ── 抖音结果（1条）
  ├─ videoModel    ── 小红书结果（1条）
  └─ videoModel    ── 视频号结果（1条）
```

**Vela 建议**：引入 `DistributionJob` 或复用现有 `ContentJob` 作为顶层任务，`SocialPost` 作为子结果。一个 DistributionJob 对应多个 SocialPost（每个平台一个）。这样原生支持"一键多发"语义。

### 4.2 状态聚合策略

AiToEarn 的聚合逻辑（`publish/video/controller.ts:87-101`）：

```typescript
const theStatus =
  successCount === 0 ? PubStatus.FAIL :
  successCount === pubRes.length ? PubStatus.RELEASED :
  PubStatus.PartSuccess; // 部分成功
```

**Vela 建议**：在 `DistributionJob` 上实现同样的聚合逻辑，用 Go 的 `slices` 或简单计数。前端可以直接展示 "3/4 平台发布成功" 的 UI。

### 4.3 差异化参数字段

AiToEarn 的 `WorkData.diffParams` 是 `Record<string, any>` (MongoDB JSON)，每个平台可以有自己专属的额外参数（抖音热点、视频号原创声明等）。

**Vela 建议**：`PublishRequest` 增加 `PlatformOptions map[string]interface{}` 字段，各平台 Publisher 实现自行解析。同时 `SocialPost` 增加 `PlatformMeta datatypes.JSON` 保存各平台的元数据。

### 4.4 进度回调机制

AiToEarn 的 `PlatformBase.videoPublish()` 接受 `callback: VideoCallbackType`，在发布过程中（上传、转码、提交）调用回调推送进度。

**Vela 建议**：对长时间操作（视频上传大文件），改用 Go 的 WebSocket 或 SSE 推送进度。`PlatformPublisher.Publish` 可以接受一个 `progress chan float64` 参数实现流式进度。

### 4.5 并行分发器模式

AiToEarn 使用 `Promise.all()` 直接并行调用所有平台，简单高效。

**Vela 建议**：Go 后端用 `errgroup` 或 `sync.WaitGroup` 实现等价的并行分发。同时增加超时控制和优雅降级：一个平台失败不影响其他平台。示例框架：

```go
g, ctx := errgroup.WithContext(ctx)
for _, target := range job.PlatformTargets {
    target := target
    g.Go(func() error {
        result, err := pub.Publish(ctx, token, req)
        // 写 SocialPost，即使失败也记录错误
        return nil // 不 propagate 错误
    })
}
_ = g.Wait() // 全部跑完，再聚合状态
```

### 4.6 代理 IP 注入

AiToEarn 在分发前从账户组读取代理 IP 并注入到每个 videoModel。这对 TikTok/Instagram 等有地域限制/风控的平台至关重要。

**Vela 建议**：在 `SocialConnection` 上增加 `ProxyConfig` 字段（JSON），`PlatformPublisher.Publish` 方法自动读取并配置 HTTP client 的代理。或更简单地，后端用 `httputil.ProxyRoundTripper` 包装。

---

## 5. 建议的演进路径（分阶段）

### Phase 1 — 基础设施加固（1-2 周）

| 事项 | 描述 | 涉及文件 |
|------|------|----------|
| **升级 PlatformPublisher 接口** | `Publish` 增加 `progressCh chan<- float64` 可选参数 + `map[string]interface{}` 选项 | `publisher.go` |
| **增加平台注册表** | 类似 AiToEarn `PlatController` 的 Map 模式，支持按名称查找 Publisher | `social.go` 或新建 `registry.go` |
| **完善状态枚举** | 将 string 状态改为 typed const: `StatusDraft` / `StatusPublished` / `StatusFailed` / `StatusPartial` / `StatusAudit` | `model/social.go` 或新建 `social_enums.go` |

### Phase 2 — 一键分发核心（2-3 周）

| 事项 | 描述 | 涉及文件 |
|------|------|----------|
| **引入 DistributionJob** | 顶层任务表，包含 `Status`、`PlatformTargets []string`、`ScheduledAt` | `model/social.go` |
| **Distribute 端点** | `POST /api/social/distribute` 接收内容 + 目标平台列表，创建 Job + N 个 SocialPost | 新建 `handler/distribute.go` |
| **并行分发引擎** | Go errgroup 实现的并行分发器，写 SocialPost 结果 | 新建 `service/social/distributor.go` |
| **状态聚合** | 发布完成后聚合 SocialPost 状态回写 DistributionJob.Status | `distributor.go` |
| **差分参数字段** | SocialPost 增加 `PlatformMeta datatypes.JSON` | `model/social.go` |

### Phase 3 — 高级特性（3-4 周）

| 事项 | 描述 | 涉及文件 |
|------|------|----------|
| **定时发布调度** | 用 `time.AfterFunc` 或 cron 库实现 `ScheduledAt` 触发 | 新建 `service/social/scheduler.go` |
| **WebSocket 进度推送** | 分发过程中通过 WebSocket 推送每个平台的实时进度 | 后端 WS 端点 |
| **代理 IP 支持** | SocialConnection 增加 Proxy 配置，HTTP Client 包装 | `publisher.go` 各平台实现 |
| **重试机制** | 失败的 SocialPost 自动重试（指数退避） | `distributor.go` |

### Phase 4 — 生态扩展（持续）

| 事项 | 描述 |
|------|------|
| **新增平台** | Instagram (Graph API)、YouTube (Data API v3)、TikTok (Content Post API)、Twitter/X |
| **图文混排** | 扩展 `PublishRequest` 支持多图片 + 富文本 |
| **发布历史看板** | 基于 DistributionJob + SocialPost 展示发布统计 |
| **AI 内容生成+分发联动** | ContentJob 完成后自动触发 DistributionJob |

---

## 6. 关键代码引用（文件路径+行号）

### AiToEarn 源码（参考实现）

| 功能 | 路径 | 行号 |
|------|------|------|
| **发布入口 IPC** | `project/aitoearn-electron/electron/main/publish/video/controller.ts` | 56-108 |
| **代理 IP 注入** | 同上 | 75-82 |
| **状态聚合** | 同上 | 87-101 |
| **并行分发** | `project/aitoearn-electron/electron/main/plat/index.ts` | 79-103 |
| **单条发布执行** | `project/aitoearn-electron/electron/main/plat/pub/PubItemVideo.ts` | 44-90 |
| **进度回调推送** | 同上 | 51-60, 66-71, 81-86 |
| **平台基类接口** | `project/aitoearn-electron/electron/main/plat/PlatformBase.ts` | 35-261 |
| **发布结果模型** | `project/aitoearn-electron/electron/main/plat/module.ts` | 27-52 |
| **VideoModel 数据模型** | `project/aitoearn-electron/electron/db/models/workData.ts` | 55-201 |
| **PubRecord MongoDB Schema** | `project/aitoearn-electron/server/src/db/schema/pubRecord.schema.ts` | 1-95 |
| **发布 DTO** | `project/aitoearn-electron/server/src/modules/publish/dto/publish.dto.ts` | 1-114 |
| **PubRecord 状态枚举** | `project/aitoearn-electron/commont/publish/PublishEnum.ts` | 18-24 |
| **PubItem 基类** | `project/aitoearn-electron/electron/main/plat/pub/PubItemBase.ts` | 1-12 |
| **发布服务（Video）** | `project/aitoearn-electron/electron/main/publish/video/service.ts` | 1-112 |
| **发布服务（PubRecord）** | `project/aitoearn-electron/electron/main/publish/service.ts` | 1-68 |
| **抖音平台实现** | `project/aitoearn-electron/electron/main/plat/platforms/douyin/index.ts` | — |
| **小红书平台实现** | `project/aitoearn-electron/electron/main/plat/platforms/xhs/index.ts` | — |

### Vela AI 现有代码（待改造点）

| 功能 | 路径 | 行号 |
|------|------|------|
| **SocialHandler** | `internal/handler/social.go` | 1-464 |
| **PlatformPublisher 接口** | `internal/service/social/publisher.go` | 32-44 |
| **Pinterest 实现** | `internal/service/social/pinterest.go` | 1-148 |
| **ShareHandler** | `internal/handler/share.go` | 1-224 |
| **SocialConnection 模型** | `internal/model/social.go` | 10-24 |
| **SocialPost 模型** | 同上 | 26-44 |
| **ContentJob 模型（可复用）** | 同上 | 46-59 |

---

## 附录：架构迁移对照图

```
AiToEarn (Electron/TS)                    Vela AI (Go) 建议
══════════════════════                    ══════════════════

pubRecord (发布任务)  ─────────────→  DistributionJob (新建)
  ├─ type: video/image-text              ├─ job_type: "video" | "image-text"
  ├─ status: PubStatus                    ├─ status: JobStatus
  └─ timingTime?: Date                    └─ scheduled_at: *time.Time
                                              │
videoModel × N (子记录)  ────────→  SocialPost × N (已存在但需扩展)
  ├─ accountId  (关联账号)                 ├─ ConnectionID (关联社交连接)
  ├─ type: PlatType (平台类型)              ├─ Platform (平台名)
  ├─ status: PubStatus                     ├─ Status (增加 PartSuccess/Audit)
  ├─ dataId  (平台返回的ID)                ├─ PinID / ContentID
  ├─ diffParams: JSON (差异化参数)          ├─ PlatformMeta: JSON (新增)
  ├─ failMsg                                ├─ ErrorMessage (已有)
  └─ proxyIp                                ├─ (通过 conn.ProxyConfig)

PlatController (分发器)  ─────────→  Distributor (新建)
  videoPublish(videoModels)               Distribute(ctx, job)
  Promise.all() 并行                       errgroup 并行
  进度 callback → IPC                      进度 channel → WebSocket

PlatformBase (抽象基类)  ───────→  PlatformPublisher (接口增强)
  videoPublish(params, callback)           Publish(ctx, token, req, opts...)
  imgTextPublish(params)                    (可增加 PublishImageText)
```

---

> **总结**：AiToEarn 的核心价值在于**双层记录模型 + 并行分发 + 进度回调 + 状态聚合**这套组合拳。Vela AI 已具备良好的 OAuth 基础和数据结构，只需引入 `DistributionJob` 作为顶层任务、用 `errgroup` 实现并行、扩展 `PlatformPublisher` 接口支持差异化参数，就能以最低成本获得同等的一键分发能力。Vela AI 的后端架构反而比 Electron 更灵活——天然支持异步队列、WebSocket 推送和定时任务，这些都是未来加分项。
