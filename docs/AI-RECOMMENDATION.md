# 推荐引擎 — FBT + Trending 架构与数据流

> 文件: internal/service/recommendation_engine.go
> 依赖: PostgreSQL (synced_orders, product_affinities)

## 一、架构定位

推荐引擎提供两种推荐策略：Frequently Bought Together (FBT) 和 Trending。

```
synced_orders (JSONB line_items)
  │
  ├─────────────────────────────────────┐
  │                                     │
  ▼                                     ▼
FBT Engine                          Trending Engine
(内存 JSONB 解析)                    (PostgreSQL 聚合)
  │                                     │
  │ 90天订单                            │ 30天窗口
  │ 解析 line_items                     │ 时间加权
  │ 构建商品对共现 map                   │ GROUP BY product_id
  │                                     │
  └─────────────┬───────────────────────┘
                │
                ▼
         product_affinities
         (shop_id, strategy, source_product_id,
          target_product_id, score, product_info)
                │
                ▼
      GET /api/recommendations/fbt?product_id=
      GET /api/recommendations/trending
      GET /api/recommendations/stats
```

## 二、FBT (Frequently Bought Together)

### 2.1 算法

```
BuildFBT(ctx, shopID):
  1. 加载 90 天订单
     SELECT * FROM synced_orders
     WHERE shop_id=? AND order_created_at > now() - 90 days

  2. 解析 JSONB line_items
     for each order:
       items = json.Unmarshal(order.LineItems)
       
  3. 构建商品对共现
     for each pair (i, j) in items:
       pair = productPair{min(i, j), max(i, j)}
       pairCount[pair]++

  4. 计算分数
     totalOrders = len(orders)
     for pair, count in pairCount:
       score = count / totalOrders  // 共现比例

  5. Upsert 到 product_affinities
     strategy = "fbt"
     source = pair.A, target = pair.B
```

### 2.2 冷启动回退

```
QueryFBT(ctx, shopID, productID, limit):
  查找 product_affinities WHERE strategy='fbt' AND source_product_id=?

  if results < 3:
    回退: 同品类商品 (synced_products WHERE product_type 相同)

  if still < 3:
    回退: Trending 热门商品
```

## 三、Trending (热门)

### 3.1 算法

```
BuildTrending(ctx, shopID):
  1. 加载 30 天订单
     SELECT product_id, order_created_at
     FROM synced_orders
     WHERE shop_id=? AND order_created_at > now() - 30 days

  2. 时间加权
     for each order:
       daysAgo = (now - order_created_at).Days
       if daysAgo <= 7:   weight = 1.0
       elif daysAgo <= 14: weight = 0.7
       else:               weight = 0.3
       productScore[product_id] += weight

  3. 排序 + Upsert
     strategy = "trending"
     更新 product_affinities
```

### 3.2 查询

```
QueryTrending(ctx, shopID, limit=8):
  SELECT * FROM product_affinities
  WHERE strategy='trending'
  ORDER BY score DESC
  LIMIT 8
```

## 四、product_affinities 表结构

```sql
CREATE TABLE product_affinities (
    id              UUID PRIMARY KEY,
    shop_id         UUID NOT NULL,
    strategy         VARCHAR(20) NOT NULL,     -- 'fbt' / 'trending'
    source_product_id VARCHAR(50) NOT NULL,     -- 源商品 (FBT) 或通用 (Trending)
    target_product_id VARCHAR(50) NOT NULL,     -- 推荐目标商品
    score           FLOAT NOT NULL,             -- 0.0 - 1.0
    product_title   VARCHAR(500),               -- 冗余存储避免 JOIN
    product_image   VARCHAR(1000),              -- 冗余存储避免 JOIN
    product_price   VARCHAR(50),                -- 冗余存储避免 JOIN
    created_at      TIMESTAMPTZ,
    updated_at      TIMESTAMPTZ,
    UNIQUE(shop_id, strategy, source_product_id, target_product_id)
);
```

## 五、异步刷新

```
Asynq 定时任务:
  TypeFBTRefresh (每天 3:00 AM) → BuildFBT
  TypeTrendingRefresh (每小时)   → BuildTrending
  TypeCustomerIntelRefresh (每天 2:00 AM) → Cleanup 旧数据
```

## 六、关键设计决策

| 决策 | 原因 |
|------|------|
| 内存 JSONB 解析 FBT | 避免创建 order_items 关联表 |
| 预计算 + 缓存表 | API 读取 <10ms |
| 冗余 product_info | 避免 JOIN synced_products |
| 时间加权 Trending | 最近购买的商品更重要 |
| 冷启动多级回退 | 新店铺无数据时保证有结果 |
