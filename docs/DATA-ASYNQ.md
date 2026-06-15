# Asynq 任务队列 — 架构与数据流

> 文件: internal/platform/taskqueue/
> 依赖: Redis 7 (asynq 队列)

## 一、架构定位

Asynq 是基于 Redis 的任务队列，用于定时异步作业。内嵌在 Go 进程中运行。

```
┌────────────────────────────────────────────────────┐
│                  Asynq 任务队列                      │
│                                                    │
│  ┌──────────────────┐   ┌──────────────────────┐  │
│  │   AsynqClient    │   │   AsynqServer         │  │
│  │   (Enqueue)      │   │   (Worker)            │  │
│  │                  │   │                      │  │
│  │   Enqueue(task)  │   │   mux.HandleFunc()    │  │
│  │   → Redis Queue  │   │   → handler(task)     │  │
│  └────────┬─────────┘   └──────────┬───────────┘  │
│           │                        │              │
│           ▼                        ▼              │
│     ┌──────────────────────────────────────┐      │
│     │           Redis (asynq DB)            │      │
│     │  队列: asynq:{default,low,critical}   │      │
│     │  状态: asynq:completed, asynq:failed  │      │
│     └──────────────────────────────────────┘      │
└────────────────────────────────────────────────────┘
```

## 二、任务类型

| 任务类型 | 调度频率 | 处理函数 | 说明 |
|---------|---------|---------|------|
| TypeCartRecoveryCheck | 每 5 分钟 | CartRecoveryExecutor | 扫描弃单并发送恢复邮件 |
| TypeContentPullMetrics | 每 6 小时 | ContentMetricsPuller | 拉取 Pinterest 内容指标 |
| TypeEnrichProduct | 按需 | Webhook.EnrichProduct | AI 补全商品信息 |
| TypeReviewInvitation | 5 天延迟 | ReviewInviter.ExecuteTask | 发送评价邀请 |
| TypeCustomerIntelRefresh | 每天 2:00 AM | CustomerIntelEngine.ComputeAll | 计算 LTV + 流失 |
| TypeFBTRefresh | 每天 3:00 AM | RecommendationEngine.BuildFBT | 构建 FBT 共现对 |
| TypeTrendingRefresh | 每小时 | RecommendationEngine.BuildTrending | 计算热门商品 |

## 三、数据流

### 3.1 定时任务 (Scheduled)

```
server.New()
  │
  ├─ 启动 goroutine (消费者刷新)
  │   计算 initialDelay (next 2:00am)
  │   time.Sleep(initialDelay)
  │   ticker := NewTicker(24h)
  │   for range ticker.C:
  │     遍历所有 active shops:
  │       taskClient.Enqueue(TypeCustomerIntelRefresh, payload{shopID})
  │
  └─ 启动 goroutine (FBT 刷新)
      计算 initialDelay (next 3:00am)
      ... 同上 ...

Asynq Worker:
  mux.Handle(TypeCustomerIntelRefresh, handler)
    → shopID = uuid.Parse(payload.ShopID)
    → srv.customerIntelEng.ComputeAll(ctx, shopID)
```

### 3.2 延迟任务 (Delayed)

```
Order Fulfilled
  → EventOrderFulfilled
  → ReviewInviter.HandleEvent
  → 创建 ReviewInvitation (PostgreSQL)
  → taskClient.Enqueue(
      TypeReviewInvitation,
      payload{ReviewInvitationID: uuid},
      asynq.ProcessIn(5 * 24 * time.Hour)  // 5 天延迟
    )

5 天后:
  Worker 收到任务
  → ReviewInviter.ExecuteTask
  → 发送评价邀请邮件 (Resend)
```

## 四、Task 结构

```go
type Task struct {
    Type    TaskType        // "cart_recovery:check" 等
    Payload json.RawMessage // 业务负载 (JSON)
}

type ReviewInvitationPayload struct {
    ReviewInvitationID string `json:"review_invitation_id"`
}

type CustomerIntelligenceRefreshPayload struct {
    ShopID string `json:"shop_id"`
}
```

## 五、Redis 队列结构

```
Redis DB (默认 asynq DB):
  asynq:default        → 默认队列 (List)
  asynq:critical       → 关键队列 (List)
  asynq:scheduled      → 延迟任务 (ZSET, score=执行时间)
  asynq:retry          → 重试任务 (ZSET)
  asynq:dead           → 死信队列 (ZSET)
  asynq:completed      → 已完成 (保留 N 条用于检查)
  asynq:servers        → Worker 心跳 (ZSET)
```

## 六、容错

- **重试**: 默认最多 25 次，指数退避
- **死信**: 超过重试次数的任务进入 dead queue
- **超时**: 任务默认 30 分钟超时
- **Worker 心跳**: 心跳丢失 → 任务重新分配
