# Redis Cache — 缓存与限流架构

> 文件: internal/service/cache.go
> 依赖: Redis 7

## 一、架构定位

Redis 在 Vela Engine 中承担四重角色：EventBus 流、应用缓存、Session 存储、Rate Limit。

```
┌────────────────────────────────────────────────────┐
│                   Redis 7                           │
│                                                     │
│  DB 0: 业务缓存 + EventBus                          │
│  ├─ llm:config:{shopID}     (TTL=5min)             │
│  ├─ snap:shop:{shopID}      (TTL=1h)               │
│  ├─ cron:last_sync:{type}:{shopID}                  │
│  ├─ feature:{name}          (feature flag)          │
│  ├─ ratelimit:{shop}:{endpoint}:{minute}            │
│  ├─ quota:{shop}:{feature}:{date}                  │
│  ├─ reengagement:{shop}:{cust} (TTL=30d)           │
│  ├─ customer_sessions:{shop}:{cust}                 │
│  └─ vela:events:*          (Streams, MaxLen=10K)    │
│                                                     │
│  DB 1: Session 存储                                  │
│  ├─ chat:session:{sessionID} (TTL=24h)             │
│  └─ shop:session:{shopID}    (TTL=24h)             │
│                                                     │
│  DB 2: Asynq 任务队列                                │
│  ├─ asynq:default/critical/scheduled/retry/dead     │
│  └─ asynq:servers/completed                         │
└────────────────────────────────────────────────────┘
```

## 二、CacheService

```go
type CacheService struct {
    client *redis.Client
}

主要方法:
  Get(ctx, key)       → string (with TTL awareness)
  Set(ctx, key, val, ttl)
  Delete(ctx, key)
  Incr(ctx, key)      → 计数器自增
  CheckQuota(ctx, key, date, limit) → (allowed bool, count int, err)
  Ping(ctx)           → 健康检查
```

## 三、缓存策略

### 3.1 LLM 配置缓存
```
getConfig(shopID):
  key = "llm:config:{shopID}"
  TTL = 5 分钟
  
  命中 → 直接返回 ShopLLMConfig
  未命中 → 查 PostgreSQL → 写入缓存 → 返回
```

### 3.2 Snapshot 缓存
```
SnapshotStore:
  key = "snap:shop:{shopID}"
  TTL = 1 小时
  
  数据: {
    total_orders, total_revenue, total_customers,
    avg_order_value, return_rate,
    top_products[], recent_orders[]
  }
  
  优势: 6+ PG 查询 → 1 Redis 读取
```

### 3.3 同步去重
```
key = "cron:last_sync:{type}:{shopID}"
value = timestamp
TTL = 1 小时

SyncOrders 前检查: 如果 1 小时内已同步 → 跳过
```

## 四、Rate Limit

```
key = "ratelimit:{shop}:{endpoint}:{YYYYMMDDHHMM}"
限流: 滑动窗口计数器

DefaultRateLimits():
  /api/recommendations/* → 100 req/min
  /api/chat/*            → 30 req/min
  /api/content/*         → 20 req/min
  默认                   → 100 req/min

开发模式 (GO_ENV=development): 跳过限流
```

## 五、Quota 管理

```
key = "quota:{shop}:{feature}:{date}"
每日配额计数器

Gateway middleware:
  feature = extractFeatureFromPath("/api/recommendations/trending")
           → "recommendations"
  limit = DefaultQuotaLimits[feature]  // 或 100
  
  CheckQuota(key, date, limit):
    INC quota:{shop}:{feature}:{date}
    if count > limit → 429 Too Many Requests
```

## 六、连接池

```go
NewCacheService(ctx, redisURL):
  redis.ParseURL(redisURL)
  redis.NewClient(opts)
    PoolSize:     20
    MinIdleConns: 5
    MaxRetries:   3
    DialTimeout:  5s
    ReadTimeout:  3s
    WriteTimeout: 3s
```
