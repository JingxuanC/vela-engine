# Sync Pipeline 同步管道 — 架构与数据流

> 文件: internal/platform/sync/
> 依赖: 外部平台 REST API

## 一、架构定位

Sync Pipeline 负责从外部平台拉取数据、写入 PostgreSQL、通过 EventBus 触发下游 AI 分析。

```
外部平台 API (REST/GraphQL)
  │
  ├─ GET /admin/products.json?limit=250
  ├─ GET /admin/orders.json?limit=250
  └─ GET /admin/customers.json?limit=250
  │
  ▼
┌─────────────────────────────────────────────┐
│              Sync Pipeline                   │
│                                             │
│  ┌───────────────┐  ┌──────────────┐       │
│  │ ShopifyREST   │  │ Syncer       │       │
│  │ Client        │  │ Interface    │       │
│  │ (HTTP+分页)   │  │              │       │
│  │               │  │ SyncFull()   │       │
│  │ fetchPage()   │  │ SyncRecent() │       │
│  │ parseLink()   │  │              │       │
│  └───────┬───────┘  └──────┬───────┘       │
│          │                 │               │
│  ┌───────┴─────────────────┴───────┐       │
│  │         ProductSyncer          │       │
│  │  Upsert → synced_products      │       │
│  │  → EventBus.Publish            │       │
│  │  → Asynq EnrichProduct         │       │
│  ├─────────────────────────────────┤       │
│  │         OrderSyncer             │       │
│  │  Upsert → synced_orders        │       │
│  │  → EventBus.Publish            │       │
│  ├─────────────────────────────────┤       │
│  │         CustomerSyncer         │       │
│  │  Upsert → synced_customers     │       │
│  │  → EventBus.Publish            │       │
│  └─────────────────────────────────┘       │
└─────────────────────────────────────────────┘
  │
  ▼
PostgreSQL (synced_* 表) + EventBus (下游 AI)
```

## 二、同步策略

| 实体 | 数据源 | 存储表 | 唯一约束 | 分页 |
|------|--------|--------|---------|------|
| Product | REST API | synced_products | (shop_id, platform_id) | 250/页, Link header |
| Order | REST API | synced_orders | (shop_id, platform_id) | 250/页, Link header |
| Customer | REST API | synced_customers | (shop_id, platform_id) | 250/页, Link header |
| Review | JudgeMe API | synced_reviews | (shop_id, platform_id) | API 分页 |
| Return | Webhook | synced_returns | (shop_id, platform_id) | 单条事件 |

## 三、数据流详解

### 3.1 全量同步 (SyncFull)

```
Cron 触发 (或手动 API)
  → Syncer.SyncFull(ctx, shopID)
    → 1. 获取 API 凭证 (ShopDomain + AccessToken)
    → 2. fetchPage(url) → 分页循环
         ├─ HTTP GET with headers
         ├─ 解析 Link header 获取 next 页
         └─ 收集所有 items
    → 3. Upsert 到 PostgreSQL
         ├─ GORM Clauses.OnConflict{UpdateAll: true}
         └─ platform_id + shop_id 唯一约束
    → 4. Publish EventBus
         └─ EventProductSynced / EventOrderSynced / EventCustomerSynced
    → 5. 记录 LastSync 时间 (Redis)
```

### 3.2 增量同步 (SyncRecent)

```
仅拉取最近更新的数据 (since LastSync)
  → 减少 API 调用量
  → 适用于高频 Cron 任务
```

## 四、ShopifyREST Client

```go
type ShopifyRESTClient struct {
    client     *http.Client    // Timeout: 30s
    apiVersion string          // "2024-04"
}

func (c *ShopifyRESTClient) fetchPage(url, token string) ([]byte, string, error)
  → HTTP GET with X-Shopify-Access-Token
  → 返回 body + next page URL (from Link header)
```

## 五、Upsert 模式

```sql
INSERT INTO synced_products (shop_id, platform_id, title, ...)
VALUES (?, ?, ?, ...)
ON CONFLICT (shop_id, platform_id)
DO UPDATE SET title=EXCLUDED.title, updated_at=NOW()
```

GORM 实现:
```go
db.Clauses(clause.OnConflict{
    Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
    UpdateAll: true,
}).Create(&product)
```

## 六、Cron 调度

```
各 Syncer 通过 CronHandler 调度:
  POST /api/cron/sync-products   → ProductSyncer.SyncFull
  POST /api/cron/sync-orders     → OrderSyncer.SyncFull
  POST /api/cron/sync-customers  → CustomerSyncer.SyncFull
  POST /api/cron/sync-reviews    → JudgeMe Sync

去重: 每个 shop 每种 sync 1 小时内只执行一次 (Redis 记录)
```

## 七、关键设计决策

| 决策 | 原因 |
|------|------|
| platform_id + shop_id 唯一约束 | 避免重复同步 |
| JSONB 存 variants/images/line_items | 灵活 schema，无需额外表 |
| OnConflict UpdateAll | 幂等同步，重复执行无副作用 |
| Link header 分页 | REST API 标准分页方式 |
| EventBus publish after upsert | 数据变更立即触发 AI 分析 |
