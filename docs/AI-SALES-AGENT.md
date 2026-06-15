# Sales Agent — AI 客服架构与数据流

> 文件: internal/service/salesagent/
> 依赖: LLMRouter + ContextInjector + Redis Session

## 一、架构定位

Sales Agent 是面向消费者的 AI 购物助手。支持 SSE 流式对话，通过 8 个 Context Injector 注入实时商店数据。

```
Storefront Widget (Floating Chat)
  │
  ▼ POST /api/chat/stream (SSE)
  │
SalesAgent.StreamChat(ctx, sessionID, message)
  │
  ├─ 1. Session 管理 (Redis, 24h TTL)
  │     key: chat:session:{sessionID}
  │     history: []ChatMessage
  │
  ├─ 2. Context Injector (8 个数据源)
  │     ├─ SnapshotFetcher      → Redis 缓存聚合数据
  │     ├─ RAGFetcher           → Qdrant 向量检索
  │     ├─ ReturnFetcher        → 退货历史
  │     ├─ OrderStatusFetcher   → 订单状态
  │     ├─ ProductDataFetcher   → 产品详情
  │     ├─ TrendDataFetcher     → Google Trends
  │     ├─ TryOnHistoryFetcher  → 试穿记录
  │     └─ InventoryVelocityFetcher → 库存速度
  │
  ├─ 3. LLM Call (LLMRouter → Qwen/DeepSeek/GPT)
  │     System: 品牌语气 + 产品目录
  │     Context: 注入的实时数据
  │     History: 对话历史 (最近 10 轮)
  │     Tools: search_products, check_order, get_inventory, etc.
  │
  └─ 4. Event 发布
       chat.intent.* → VCI Consumer
```

## 二、数据飞轮 (VCI Consumer)

```
chat.intent.* 事件
  │
  ├─ ChatIntentEvent → PostgreSQL
  │   意图信号: intent_high, price_objection,
  │             size_preference, color_preference,
  │             discount_request, purchase_signal
  │
  ├─ CustomerChatProfile (Upsert)
  │   长期客户画像: SizePreference, ColorPreferences,
  │                 StylePreferences, BudgetRange,
  │                 PriceSensitivity, IntentStrength
  │
  ├─ ProductChatInsight (Upsert)
  │   产品级分析: TimesRecommended, TimesQuestioned,
  │              TimesViewed, TimesPurchased,
  │              TopObjections, ComparedWith
  │
  └─ Redis Re-engagement Tags (30 天 TTL)
      key: reengagement:{shopID}:{customerID}
      value: productID
```

## 三、Tools (函数调用)

```
Sales Agent 可用工具:
  search_products(query, filters)    → 产品搜索
  get_product_detail(productID)      → 产品详情
  check_order_status(email, orderID) → 订单查询
  check_inventory(productID)         → 库存查询
  recommend_products(preferences)    → AI 推荐
  check_return_policy()              → 退货政策
  estimate_shipping(zipCode)         → 物流估算
```

## 四、Session 管理

```
Redis Key: chat:session:{sessionID}
TTL: 24 小时

Value: JSON {
  messages: [...],       // 对话历史 (最近 10 轮)
  customerID: "...",     // 匿名或已登录
  shopID: "...",
  preferences: {},       // 本次会话偏好
  metadata: {}           // 来源页面、设备等
}
```

## 五、关键设计决策

| 决策 | 原因 |
|------|------|
| SSE 流式 | 实时打字效果，提升体验 |
| Context Injection | 8 数据源 → 1 Redis Read (Snapshot) |
| 数据飞轮 | Chat 交互 → 客户画像 → 精准推荐 |
| Session Redis | 无状态服务，水平扩展友好 |
