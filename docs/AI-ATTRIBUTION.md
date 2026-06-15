# 统一归因 — 跨渠道营收归因架构与数据流

> 文件: internal/service/unified_attribution.go
> 依赖: PostgreSQL (3 路归因数据)

## 一、架构定位

UnifiedAttributionService 将 Cart Recovery、Content、Review 三路独立的归因数据合并为统一视图，用 Equal Split 消除重复计算。

```
三路归因数据源:
┌──────────────────┐  ┌────────────────┐  ┌──────────────────┐
│ Cart Recovery    │  │ Content        │  │ Review Revenue    │
│ cart_recovery_   │  │ analytics_     │  │ review_revenue_   │
│ codes            │  │ events         │  │ attributions      │
│ (折扣码匹配)      │  │ (UTM 参数匹配)  │  │ (email+product)   │
└────────┬─────────┘  └────────┬───────┘  └────────┬─────────┘
         │                     │                    │
         └─────────────────────┼────────────────────┘
                               │
                               ▼
                    UnifiedAttributionService
                               │
                    ┌──────────▼──────────┐
                    │  UNION ALL 三源数据   │
                    │  + Channel Count     │
                    │  + Equal Split       │
                    └──────────┬──────────┘
                               │
                               ▼
                    GET /api/analytics/attribution/unified
                    {
                      channels[], overlap_matrix[],
                      overlap_rate, total_revenue
                    }
```

## 二、数据流

### 2.1 归因数据产生

```
Cart Recovery 归因:
  顾客使用折扣码下单
  → Webhook orders/create
  → 匹配 cart_recovery_codes.code = order.discount_code
  → 标记 is_used=true, used_order_id, used_at

Content 归因:
  AI 内容生成 → ContentJob
  → 社交平台发布 → UTM 参数嵌入
  → 顾客通过 UTM 链接购买
  → Webhook orders/create → 解析 landingPage URL
  → 写入 analytics_events (event_type=purchase, channel=content)

Review 归因:
  评价邀请发送 (Resend email)
  → 顾客收到邮件 → 留评
  → 30 天内该顾客下单
  → 匹配 email + product_id
  → 写入 review_revenue_attributions
```

### 2.2 统一查询 (GetUnified)

```
GetUnified(ctx, shopID, days=30):
  1. UNION ALL 三路数据
     raw AS (
       SELECT 'cart_recovery' AS channel, order_id, shop_id, revenue
       FROM cart_recovery_codes WHERE is_used=true AND used_at > now()-interval
       UNION ALL
       SELECT 'content', order_id, shop_id, order_value
       FROM analytics_events WHERE event_type='purchase' AND channel='content'
       UNION ALL
       SELECT 'review', order_id, shop_id, revenue
       FROM review_revenue_attributions WHERE attributed_at > now()-interval
     )

  2. 计算每个 order 被几个渠道归因
     with_counts AS (
       SELECT *, COUNT(*) OVER (PARTITION BY order_id) AS channel_count
       FROM raw
     )

  3. Equal Split
     attributed_revenue = revenue / channel_count

  4. 聚合输出
     per_channel: SUM(attributed_revenue), COUNT(order_id)
     overlap_matrix: pairwise+threeway overlap
     overlap_rate: shared_revenue / total_attributed_revenue
```

## 三、Equal Split 逻辑

```
订单 #1001: $200
  Cart Recovery 归因: ✓
  Content 归因:      ✓
  → channel_count = 2
  → Cart Recovery:  $200 / 2 = $100
  → Content:        $200 / 2 = $100
  → total_attributed = $200 (非 $400)

订单 #1002: $150
  Review 归因: ✓
  → channel_count = 1
  → Review: $150 / 1 = $150
```

## 四、API 响应

```json
{
  "total_actual_revenue": 45000,
  "total_attributed_revenue": 38000,
  "overlap_rate": 15.5,
  "channels": [
    {"channel": "cart_recovery", "attributed_orders": 45, "attributed_revenue": 16000, "shared_revenue": 2000},
    {"channel": "content",       "attributed_orders": 120, "attributed_revenue": 19000, "shared_revenue": 3000},
    {"channel": "review",        "attributed_orders": 30, "attributed_revenue": 10000, "shared_revenue": 2000}
  ],
  "overlap_matrix": [
    {"pair": "cart_recovery + content", "orders": 12, "revenue": 1500},
    {"pair": "cart_recovery + review",  "orders": 5,  "revenue": 500},
    {"pair": "content + review",        "orders": 8,  "revenue": 500},
    {"pair": "all three",               "orders": 3,  "revenue": 200}
  ]
}
```

## 五、关键设计决策

| 决策 | 原因 |
|------|------|
| Equal Split (非 Last-touch) | 最简单公平模型，避免时间语义不一致 |
| UNION ALL 三路数据 | 单次查询，无需中间表 |
| overlap_matrix | 透明展示多渠道归因重叠 |
| 30 天默认窗口 | 与营销归因行业标准一致 |
