# EventBus 事件总线 — 架构与数据流

> 文件: internal/platform/eventbus/
> 依赖: Redis 7 (Streams)

## 一、架构定位

EventBus 是 Vela Engine 的数据飞轮。所有数据变更通过事件发布，下游消费者异步处理，实现松耦合。

```
发布者                         EventBus                   消费者
───────                      ─────────                  ──────
ProductSyncer ─┐         Redis Streams              ┌─ InsightEngine
OrderSyncer   ─┤    ┌─────────────────────┐         ├─ RAG Indexer
Cron          ─┼───>│ EventBus.Publish()  │─────────┼─ AutoReply
Webhook       ─┤    │   XAdd(stream, msg) │         ├─ CartRecovery
SalesAgent    ─┘    │   MaxLen=10000      │         ├─ NotifyCenter
                    └──────────┬──────────┘         ├─ FulfillmentInsight
                               │                    ├─ ReviewInviter
                    ┌──────────▼──────────┐         └─ VCI Consumer
                    │   Consumer Group     │
                    │   XReadGroup()       │
                    │   → handler(event)   │
                    │   → XAck(msg)        │
                    └─────────────────────┘
```

## 二、事件类型矩阵

| 事件类型 | 发布者 | Stream 名 | 消费者 |
|---------|--------|-----------|--------|
| EventProductSynced | ProductSyncer | vela:events:product | InsightEngine, RAG |
| EventOrderSynced | OrderSyncer | vela:events:order | InsightEngine |
| EventOrderUpdated | Webhook/Sync | vela:events:order | InsightEngine |
| EventCustomerSynced | CustomerSyncer | vela:events:customer | InsightEngine |
| EventReviewSynced | JudgeMe/Webhook | vela:events:review | InsightEngine, RAG, AutoReply |
| EventReturnSynced | Webhook | vela:events:return | InsightEngine, ReturnAlert, Exchange |
| EventCheckoutSynced | Webhook | vela:events:checkout | CartRecovery |
| EventOrderFulfilled | FulfillmentTracker | vela:events:fulfillment | Notifier, Insight, ReviewInviter |
| EventContentGenerated | Content | vela:events:content | ContentAttributor |
| chat.intent.* | SalesAgent | vela:events:chat | VCI Consumer |
| EventInsightGenerated | InsightEngine | vela:events:insight | NotifyCenter |

## 三、Publish 数据流

```
1. 调用方创建 Event
   NewEvent(type, shopID, payload, source)
     → json.Marshal(payload)
     → Event{ID, Type, ShopID, Payload, Source, Timestamp}

2. EventBus.Publish(event)
     → json.Marshal(event)
     → redis.XAdd(Stream, {raw, event_id, event_type, shop_id})
     → 返回 nil (成功) 或 error (失败)
     → Stream 自动截断: MAXLEN≈10000

3. 调用方链路
   Syncer.Upsert() → DB write → bus.Publish(EventSynced)
   或 Handler → 业务逻辑 → bus.Publish(custom event)
```

## 四、Subscribe + Consume 数据流

```
1. 注册阶段 (New() 中)
   bus.Subscribe(EventProductSynced, handlerFunc)
     → handlers[EventType] = append(handlers, handlerFunc)
     → 返回 cleanup func (当前为 no-op)
   
   规则: 所有 Subscribe 必须在 StartConsumers 之前

2. 消费启动
   bus.StartConsumers(ctx)
     对于每个 EventType:
       for each handler:
         go streamConsumer.run(ctx)
           循环:
             XReadGroup(stream, group, consumer, Block=2s)
             对每条消息:
               遍历 handlers:
                 handler(ctx, &event)
             XAck(msg)
             
   Consumer Group: "eventbus:group:vela-core"
   每个 EventType 独立 goroutine

3. 关闭
   bus.Close()
     → close(stopCh) 通知 consumer 退出
     → XReadGroup Block=2s 超时后退出
```

## 五、Event 结构

```go
type Event struct {
    ID        uuid.UUID       // 事件唯一 ID
    Type      EventType       // "product.synced", "order.created" 等
    ShopID    uuid.UUID       // 租户隔离
    Payload   json.RawMessage // 业务负载 (JSON)
    Source    string          // "cron" / "webhook" / "manual"
    Timestamp time.Time       // 发布时间
}
```

## 六、线程安全

- handlers map: sync.RWMutex 保护
- Subscribe 需要写锁
- 消费循环需要读锁
- StartConsumers 后禁止 Subscribe

## 七、关键设计决策

| 决策 | 原因 |
|------|------|
| Redis Streams | 持久化 + Consumer Group 支持水平扩展 |
| MaxLen=10000 | 自动截断防止内存溢出 |
| 每个 EventType 独立 Stream | 避免不同事件的消费速度互相影响 |
| Consumer Group 固定名称 | 单实例模式，简化配置 |
| Subscribe 必须在 Start 前 | 避免运行中动态注册的竞态 |
