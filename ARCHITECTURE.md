# Vela Engine — 完整架构文档

> 最后更新: 2026-06-16
> 仓库: github.com/JingxuanC/vela-engine

## 一、系统定位

Vela Engine 是 Vela AI 电商操作系统的核心 AI 引擎。它从 vela-shopify (Shopify SaaS) 提取而来，移除了 Shopify OAuth 耦合，保留全部 AI 能力，作为独立 Go 服务运行。

```
vela-engine 定位：
  ┌────────────────────────────────────────────┐
  │  Medusa.js (电商基建)                        │
  │  Product / Order / Cart / Payment / Auth    │
  ├────────────────────────────────────────────┤
  │  Vela Engine (AI 运营层) ← 本文档范围        │
  │  LTV / 推荐 / 归因 / 评价 / 内容 / 客服       │
  ├────────────────────────────────────────────┤
  │  vela-storefront (前端)                      │
  │  AI 生成店铺 / @vela/sdk                    │
  └────────────────────────────────────────────┘
```

## 二、服务拓扑

| 组件 | 技术 | 端口 | 说明 |
|------|------|------|------|
| **Vela Engine** | Go 1.26 + chi | 8000 | 核心 AI 引擎，215 路由 |
| **PostgreSQL** | 16 | 5432 | 业务数据，30+ 张表 |
| **Redis** | 7 | 6379 | EventBus 流 + Cache + Session |
| **Asynq** | Go (内嵌) | — | Redis 任务队列（定时刷新/通知） |
| **可选: Qdrant** | qdrant/qdrant | 6333 | RAG 向量检索 |
| **可选: Ollama** | ollama/ollama | 11434 | 本地 Embedding 模型 |

## 三、数据基座架构

### 3.1 存储层

```
┌─────────────────────────────────────────────┐
│               PostgreSQL 16                  │
│                                              │
│  ┌──────────┐  ┌──────────┐  ┌───────────┐  │
│  │ 业务数据  │  │ 分析结果  │  │ 配置/审计  │  │
│  │          │  │          │  │           │  │
│  │ shops    │  │ customer │  │ shop_llm_ │  │
│  │ synced_* │  │ _insights│  │ configs   │  │
│  │ (5 表)   │  │ product_ │  │ audit_    │  │
│  │ returns  │  │ affinities│  │ logs      │  │
│  │ orders   │  │ review_* │  │ api_keys  │  │
│  │ products │  │ _attrib* │  │ billing   │  │
│  └──────────┘  └──────────┘  └───────────┘  │
│                                              │
│  全部 shop_id (UUID) 多租户隔离               │
│  GORM AutoMigrate 自动建表                    │
└─────────────────────────────────────────────┘
```

**数据表分类 (30+ 张)**：

| 分类 | 表 | 用途 |
|------|-----|------|
| 租户 | `shops` | 商户账号、Plan、Billing |
| 同步 | `synced_products`, `synced_orders`, `synced_customers`, `synced_reviews`, `synced_returns`, `synced_checkouts` | 平台数据镜像 |
| 推荐 | `product_affinities` | FBT/Trending 预计算结果 |
| 客户 | `customer_insights`, `customer_churn_risks`, `customer_360_views`, `customer_chat_profiles` | LTV/流失/Chat Profile |
| 归因 | `review_revenue_attributions`, `review_recovery_attributions`, `cart_recovery_codes`, `cart_recovery_sends` | 跨渠道归因 |
| 评价 | `reply_records`, `auto_reply_configs`, `auto_reply_logs`, `review_invitations`, `review_reply_codes` | 评价管理 |
| 营销 | `marketing_flows`, `marketing_flow_runs`, `cart_recovery_campaigns` | 营销自动化 |
| AI | `chat_intent_events`, `product_chat_insights`, `sales_agent_configs`, `shop_insights` | AI 交互数据 |
| 配置 | `shop_llm_configs`, `judgeme_settings`, `notification_prefs` | 商户级配置 |
| 审计 | `audit_logs`, `analytics_events`, `token_usages` | 审计和用量 |

### 3.2 Redis 架构

```
┌──────────────────────────────────────────────┐
│                  Redis 7                      │
│                                               │
│  ┌─────────────┐  ┌──────────┐  ┌─────────┐  │
│  │ EventBus    │  │ Cache    │  │ Session │  │
│  │ (Streams)   │  │ Service  │  │         │  │
│  │             │  │          │  │ chat_   │  │
│  │ product.*   │  │ llm:     │  │ session │  │
│  │ order.*     │  │ config:  │  │ :*      │  │
│  │ review.*    │  │ {shop}   │  │         │  │
│  │ return.*    │  │ TTL=5m   │  │ TTL=24h │  │
│  │ fulfill.*   │  │          │  │         │  │
│  │ cart_rec.*  │  │ snap:    │  │         │  │
│  │ content.*   │  │ shop:    │  │         │  │
│  │             │  │ {shop}   │  │         │  │
│  │ MAXLEN=10K  │  │ TTL=1h   │  │         │  │
│  └─────────────┘  └──────────┘  └─────────┘  │
│                                               │
│  ┌─────────────┐  ┌──────────┐               │
│  │ RateLimit   │  │ Asynq    │               │
│  │ (计数器)    │  │ (队列)   │               │
│  │             │  │          │               │
│  │ ratelimit:  │  │ asynq:   │               │
│  │ {shop}:     │  │ {task}   │               │
│  │ {endpoint}  │  │          │               │
│  └─────────────┘  └──────────┘               │
└──────────────────────────────────────────────┘
```

### 3.3 EventBus 事件流

EventBus 是系统的数据飞轮，基于 Redis Streams 实现。所有数据变更通过事件发布，下游消费者异步处理。

**事件 → 消费者矩阵**：

```
数据进入 (Webhook/Sync/Cron)
  │
  ▼
┌──────────────────────────────────────────────────────────┐
│                    EventBus (发布)                         │
│                                                          │
│  EventProductSynced    ──→ InsightEngine + RAG Indexer   │
│  EventOrderSynced      ──→ InsightEngine                  │
│  EventCustomerSynced   ──→ InsightEngine                  │
│  EventReviewSynced     ──→ InsightEngine + RAG + AutoReply│
│  EventReturnSynced     ──→ InsightEngine + ReturnAlert    │
│                           + ExchangeRecommender           │
│  EventCheckoutSynced   ──→ CartRecovery                   │
│  EventOrderFulfilled   ──→ FulfillmentNotifier            │
│                           + FulfillmentInsight            │
│                           + ReviewInviter                 │
│  EventContentGenerated ──→ ContentAttributor              │
│  EventOrderUpdated     ──→ InsightEngine                  │
│  chat.intent.*         ──→ VCI Consumer                   │
│                           (CustomerProfile + ProductInsight)│
│  EventInsightGenerated ──→ NotifyCenter (InApp Alert)     │
│  EventReviewReplied    ──→ VCI Consumer                   │
│  EventCartRecovery*    ──→ CartRecoveryAttributor         │
└──────────────────────────────────────────────────────────┘
```

**EventBus 实现细节**：
- 底层: Redis Streams (`XADD` / `XREADGROUP`)
- 每个事件类型对应独立 Stream (`vela:events:{type}`)
- `MAXLEN ≈ 10000` 自动截断，防止内存溢出
- 消费者组模式，支持多实例部署时负载均衡
- `StartConsumers()` 必须在所有 `Subscribe()` 之后调用

### 3.4 Asynq 任务队列

Asynq 是 Redis 支持的任务队列，用于定时异步任务。

```
┌──────────────────────────────────────────────┐
│              Asynq Task Queue                 │
│                                              │
│  定时任务:                                    │
│  ┌──────────────────────────────────────┐    │
│  │ TypeCartRecoveryCheck (每5分钟)       │    │
│  │   → CartRecoveryExecutor             │    │
│  ├──────────────────────────────────────┤    │
│  │ TypeContentPullMetrics (每6小时)      │    │
│  │   → ContentMetricsPuller             │    │
│  ├──────────────────────────────────────┤    │
│  │ TypeEnrichProduct (异步)             │    │
│  │   → WebhookHandler.HandleEnrich      │    │
│  ├──────────────────────────────────────┤    │
│  │ TypeReviewInvitation (5天延迟)        │    │
│  │   → ReviewInviter.ExecuteTask        │    │
│  ├──────────────────────────────────────┤    │
│  │ TypeCustomerIntelligenceRefresh       │    │
│  │   (每天 2:00 AM)                      │    │
│  │   → CustomerIntelligenceEngine       │    │
│  │      .ComputeAll(shopID)             │    │
│  ├──────────────────────────────────────┤    │
│  │ TypeFBTRefresh (每天 3:00 AM)         │    │
│  │   → RecommendationEngine.BuildFBT    │    │
│  ├──────────────────────────────────────┤    │
│  │ TypeTrendingRefresh (每小时)          │    │
│  │   → RecommendationEngine             │    │
│  │      .BuildTrending                  │    │
│  └──────────────────────────────────────┘    │
└──────────────────────────────────────────────┘
```

### 3.5 数据同步 (Sync Pipeline)

```
                   ┌─────────────────┐
                   │  外部平台 API    │
                   │  (REST/GraphQL) │
                   └───────┬─────────┘
                           │ 定时轮询 / Webhook 触发
           ┌───────────────┼───────────────┐
           ▼               ▼               ▼
    ProductSyncer   OrderSyncer    CustomerSyncer
           │               │               │
           │ GORM Upsert   │               │
           ▼               ▼               ▼
    ┌──────────────────────────────────────────┐
    │           synced_* 表 (PostgreSQL)        │
    │  platform_id + shop_id 唯一约束           │
    │  JSONB 存储变体/选项/行项                  │
    └──────────────────────────────────────────┘
           │
           │ 写入成功后
           ▼
    EventBus.Publish(Event*Synced)
           │
           ▼
    下游消费者 (InsightEngine / RAG / etc.)
```

**同步策略**：
- **Order**: REST API 分页拉取 → `synced_orders` (JSONB `line_items`)
- **Product**: REST API 分页拉取 → `synced_products` (JSONB `variants`, `images`, `options`)
- **Customer**: REST API 拉取 → `synced_customers`
- **Review**: Judge.me API / 原生 API → `synced_reviews`
- **Return**: Webhook 触发 → `synced_returns`
- **Fulfillment**: Shopify GraphQL → `fulfillment_events`

---

## 四、AI 功能层架构

### 4.1 LLM 路由 (LLMRouter)

LLMRouter 是 AI 功能的入口，管理多供应商、多模型的客户端池。

```
                     ┌─────────────────────┐
                     │    LLMRouter         │
                     │                     │
  Handler ───────────┤ GetProvider(shopID) │
  (Chat/Content/     │                     │
   SEO/Review/       │ 1. Redis Cache      │
   Insights/         │    llm:config:{shop}│
   LTV Explain)      │    TTL=5min         │
                     │                     │
                     │ 2. DB (fallback)    │
                     │    shop_llm_configs │
                     │                     │
                     │ 3. Env Defaults     │
                     │    DASHSCOPE/       │
                     │    DEEPSEEK/        │
                     │    OPENAI           │
                     └─────────┬───────────┘
                               │
              ┌────────────────┼────────────────┐
              ▼                ▼                ▼
         DashScope         DeepSeek          OpenAI
         Provider          Provider          Provider
         (Qwen)            (Chat/R1)         (GPT-4o)
              │                │                │
              └────────────────┼────────────────┘
                               │
                     ┌─────────▼──────────┐
                     │  Provider Pool      │
                     │  key: provider:     │
                     │       apikey_hash   │
                     │  (懒加载 + 复用)     │
                     └────────────────────┘
```

**供应商模型按计划分级**：

| Plan | 可用模型 |
|------|---------|
| free | qwen-turbo, deepseek-chat |
| starter | qwen-plus, deepseek-chat, deepseek-reasoner |
| growth | qwen-plus, qwen-max, deepseek-chat, deepseek-reasoner |
| pro | qwen-max, deepseek-chat, deepseek-reasoner, gpt-4o-mini |
| enterprise | 全部模型 |

### 4.2 推荐引擎 (RecommendationEngine)

```
┌──────────────────────────────────────────────────────────┐
│              RecommendationEngine                         │
│                                                          │
│  ┌─────────────────────┐  ┌─────────────────────────┐   │
│  │  FBT (Frequently    │  │  Trending                │   │
│  │  Bought Together)   │  │                          │   │
│  │                     │  │  PostgreSQL COUNT +       │   │
│  │  Go 内存 JSONB 解析  │  │  时间加权 (30天窗口)      │   │
│  │  synced_orders      │  │                          │   │
│  │  .line_items        │  │  synced_orders           │   │
│  │                     │  │  + product_affinities     │   │
│  │  1. 拉取90天订单     │  │                          │   │
│  │  2. 解析JSONB行项    │  │  按 product_id 分组      │   │
│  │  3. 构建商品对共现    │  │  加权公式:               │   │
│  │     map[(a,b)]count  │  │  1.0 (0-7天)            │   │
│  │  4. Upsert 到        │  │  0.7 (7-14天)           │   │
│  │     product_affinities│  │  0.3 (14-30天)          │   │
│  │                     │  │                          │   │
│  │  冷启动回退:          │  │  更新 product_affinities │   │
│  │  → 同品类商品         │  │                          │   │
│  └─────────┬───────────┘  └───────────┬──────────────┘   │
│            │                          │                  │
│            └──────────┬───────────────┘                  │
│                       ▼                                  │
│            product_affinities (PostgreSQL)               │
│            (shop_id, strategy, source→target, score)     │
│                       │                                  │
│                       ▼                                  │
│         GET /api/recommendations/fbt?product_id=         │
│         GET /api/recommendations/trending                │
│         GET /api/recommendations/stats                   │
└──────────────────────────────────────────────────────────┘
```

### 4.3 客户智能引擎 (CustomerIntelligenceEngine)

```
┌──────────────────────────────────────────────────────────┐
│           CustomerIntelligenceEngine                      │
│                                                          │
│  输入: synced_orders (Shopify 订单数据)                   │
│                                                          │
│  ┌──────────────────────────────────────────────────┐    │
│  │  LTV 预测 (Customer Lifetime Value)               │    │
│  │                                                  │    │
│  │  synced_orders                                   │    │
│  │    │                                             │    │
│  │    ▼ GROUP BY customer_email                     │    │
│  │  orderAggRow {                                   │    │
│  │    OrderCount, TotalSpent,                        │    │
│  │    FirstOrderAt, LastOrderAt                      │    │
│  │  }                                               │    │
│  │    │                                             │    │
│  │    ▼ 公式计算                                     │    │
│  │  PredictedLTV =                                   │    │
│  │    avgOrderValue × ordersPerYear × avgYearsActive │    │
│  │  + 衰减因子 (Recency × Frequency)                  │    │
│  │                                                  │    │
│  │  Confidence = f(DataPoints)                      │    │
│  │    < 2 orders: 25%  |  2-5: 50%  |  >5: 75%     │    │
│  └──────────────────────────────────────────────────┘    │
│                                                          │
│  ┌──────────────────────────────────────────────────┐    │
│  │  流失风险 (Churn Risk)                             │    │
│  │                                                  │    │
│  │  RiskScore (0-100) =                              │    │
│  │    RecencyScore (40%)  ← days since last order    │    │
│  │    + FrequencyDrop (30%) ← order rate decline     │    │
│  │    + ValueDrop (30%)     ← avg order value drop   │    │
│  │                                                  │    │
│  │  ActiveProbability =                              │    │
│  │    1 - (RiskScore / 100)                         │    │
│  │                                                  │    │
│  │  RiskLevel:                                       │    │
│  │    high (>70) / medium (40-70) / low (<40)       │    │
│  └──────────────────────────────────────────────────┘    │
│                                                          │
│  输出 → customer_insights 表 (Upsert by shop+email)      │
│                                                          │
│  API:                                                    │
│    GET /api/customers/insights/                           │
│    GET /api/customers/insights/{email}                    │
│    GET /api/customers/insights/{email}/explain            │
│         └─ LLMRouter → AI 解释 (25字总结 + 操作建议)      │
└──────────────────────────────────────────────────────────┘
```

### 4.4 统一归因 (UnifiedAttribution)

```
┌──────────────────────────────────────────────────────────┐
│              UnifiedAttributionService                    │
│                                                          │
│  数据源 (3 路归因数据):                                   │
│  ┌──────────────────┐ ┌──────────────────┐               │
│  │ Cart Recovery    │ │ Review Revenue   │               │
│  │ cart_recovery_   │ │ review_revenue_  │               │
│  │ codes            │ │ attributions     │               │
│  │ (discount code   │ │ (email + product │               │
│  │  match)          │ │  match)          │               │
│  └────────┬─────────┘ └────────┬─────────┘               │
│           │                    │                         │
│  ┌────────┴────────────────────┴─────────┐               │
│  │         Content Attribution           │               │
│  │  content_performances                 │               │
│  │  (UTM parameter match)                │               │
│  └────────┬──────────────────────────────┘               │
│           │                                              │
│           ▼                                              │
│  ┌────────────────────────────────────┐                  │
│  │   Equal Split Attribution          │                  │
│  │                                    │                  │
│  │  对于同一订单, 如果:               │                  │
│  │  - 渠道 A 声称贡献 $100            │                  │
│  │  - 渠道 B 声称贡献 $100            │                  │
│  │  实际: 每个渠道分 $50              │                  │
│  │                                    │                  │
│  │  overlap_matrix:                   │                  │
│  │  [{pair: "A+B", orders: N,         │                  │
│  │    revenue: $X}]                    │                  │
│  └────────────────────────────────────┘                  │
│                                                          │
│  API:                                                    │
│    GET /api/analytics/attribution/unified?days=30         │
│    → channels[], overlap_matrix[], overlap_rate%          │
└──────────────────────────────────────────────────────────┘
```

### 4.5 评价系统

```
┌──────────────────────────────────────────────────────────────┐
│                    评价系统架构                               │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  评价生成 (ReviewInviter)                             │   │
│  │                                                      │   │
│  │  订单 Fulfilled → EventOrderFulfilled                 │   │
│  │    → ReviewInviter.HandleEvent                       │   │
│  │    → 创建 ReviewInvitation (PG)                      │   │
│  │    → 排入 Asynq (5天延迟)                             │   │
│  │    → Resend 发送邀请邮件                              │   │
│  │    → Publish review.invitation_sent                  │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  评价归因 (ReviewInvitationAttributor)                │   │
│  │                                                      │   │
│  │  新评价到达 → EventReviewSynced                       │   │
│  │    → 匹配 email + product_id                         │   │
│  │    → 更新 ReviewInvitation.converted=true            │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  营收归因 (ReviewRevenueAttributor)                   │   │
│  │                                                      │   │
│  │  新订单 → EventOrderCreated                          │   │
│  │    → 匹配 email + product (30天窗口)                  │   │
│  │    → 写入 review_revenue_attributions                │   │
│  │    → Equal Split: 避免重复计算                        │   │
│  └──────────────────────────────────────────────────────┘   │
│                                                              │
│  ┌──────────────────────────────────────────────────────┐   │
│  │  自动回复 (AutoReply)                                 │   │
│  │                                                      │   │
│  │  EventReviewSynced                                    │   │
│  │    → AutoReplyService                                │   │
│  │    → ReviewAdapter (JudgeMe/Native)                  │   │
│  │    → LLM 生成回复 (品牌语气 + 评价内容)               │   │
│  │    → Publish review.replied                          │   │
│  └──────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────┘
```

### 4.6 Sales Agent (AI 客服)

```
┌──────────────────────────────────────────────────────────┐
│                   SalesAgent                              │
│                                                          │
│  Storefront Widget                                       │
│    │                                                     │
│    ▼ POST /api/chat/stream (SSE)                         │
│  ┌──────────────────────────────────────────────────┐    │
│  │  SalesAgent.StreamChat(ctx, sessionID, message)   │    │
│  │                                                  │    │
│  │  上下文注入 (ContextInjector):                     │    │
│  │  ├─ SnapshotFetcher   → Redis 缓存聚合数据         │    │
│  │  ├─ RAGFetcher        → 向量检索产品/评价         │    │
│  │  ├─ ReturnFetcher     → 退货历史                  │    │
│  │  ├─ OrderStatusFetcher→ 订单状态                  │    │
│  │  ├─ ProductDataFetcher→ 产品详情                  │    │
│  │  ├─ TrendDataFetcher  → Google Trends 数据        │    │
│  │  ├─ TryOnHistoryFetcher→ 试穿记录                 │    │
│  │  └─ InventoryVelocityFetcher→ 库存速度            │    │
│  │                                                  │    │
│  │  LLM 调用 (LLMRouter → Qwen/DeepSeek/GPT)         │    │
│  │  + 工具调用 (Tools):                               │    │
│  │    search_products, get_order_status,              │    │
│  │    check_inventory, recommend_products             │    │
│  │                                                  │    │
│  │  事件发布:                                         │    │
│  │  └─ chat.intent.* → VCI Consumer                  │    │
│  └──────────────────────────────────────────────────┘    │
│                                                          │
│  数据飞轮 (VCI Consumer):                                │
│    chat.intent.*                                         │
│      → ChatIntentEvent → PostgreSQL                      │
│      → CustomerChatProfile (Upsert)                      │
│      → ProductChatInsight (Upsert)                       │
│      → Redis Re-engagement Tags (30天 TTL)               │
└──────────────────────────────────────────────────────────┘
```

### 4.7 内容工厂 + RAG

```
┌──────────────────────────────────────────────────────────┐
│              内容工厂 & RAG                               │
│                                                          │
│  ┌──────────────────────────────────────────────────┐    │
│  │  内容生成 (ContentHandler)                        │    │
│  │                                                  │    │
│  │  POST /api/content/generate                       │    │
│  │    → LLM 生成产品描述/博客/社媒                    │    │
│  │    → ContentJob → PostgreSQL                      │    │
│  │    → Publish content.generated                    │    │
│  │      → ContentAttributor (UTM 匹配订单)           │    │
│  └──────────────────────────────────────────────────┘    │
│                                                          │
│  ┌──────────────────────────────────────────────────┐    │
│  │  RAG (检索增强生成)                               │    │
│  │                                                  │    │
│  │  Indexer (EventBus Consumer):                    │    │
│  │    EventProductSynced → Chunk → Embed → Qdrant   │    │
│  │    EventReviewSynced  → Chunk → Embed → Qdrant   │    │
│  │    EventReturnSynced  → Chunk → Embed → Qdrant   │    │
│  │                                                  │    │
│  │  Retriever:                                      │    │
│  │    查询 → Embed → Qdrant (ANN) → Top-K           │    │
│  │    → Rerank → 返回相关文档片段                    │    │
│  │                                                  │    │
│  │  降级策略: Qdrant 不可用 → SQL-only               │    │
│  └──────────────────────────────────────────────────┘    │
└──────────────────────────────────────────────────────────┘
```

### 4.8 Insight 引擎 + 内容归因

```
┌──────────────────────────────────────────────────────────┐
│              InsightEngine                                │
│                                                          │
│  EventBus Consumer:                                      │
│    OnEvent(EventProductSynced/OrderSynced/ReviewSynced/  │
│            ReturnSynced/OrderUpdated/ProductUpdated)     │
│      │                                                   │
│      ▼                                                   │
│  分析类型:                                                │
│  ├─ ProductTrend     → 销量趋势                           │
│  ├─ ReturnAnomaly    → 退货异常 (阈值 >30%)               │
│  ├─ InventoryAlert   → 库存预警                           │
│  ├─ PriceAnalysis    → 定价分析                           │
│  └─ CustomerSegment  → 客户分层                           │
│      │                                                   │
│      ▼                                                   │
│  shop_insights (PostgreSQL)                              │
│  + NotifyCenter (InApp/Email 通知)                       │
│                                                          │
│  Snapshot Store:                                         │
│    定时聚合 → Redis cache (1h TTL)                        │
│    6+ PG 查询 → 1 Redis read                             │
└──────────────────────────────────────────────────────────┘
```

---

## 五、请求流完整路径

```
Client
  │
  ▼
Caddy (可选, 反向代理 + HTTPS)
  │
  ▼
chi Router
  │
  ├─ Middleware Chain (顺序执行):
  │   ├─ RequestID          → 请求追踪
  │   ├─ RealIP             → 真实 IP
  │   ├─ Observability      → Prometheus 指标
  │   ├─ Logging            → 结构化日志
  │   ├─ CORS               → 跨域
  │   ├─ Recoverer          → Panic 恢复
  │   ├─ Gateway            → API Key 认证 + Feature Flag + Quota
  │   ├─ RateLimit          → Redis 滑动窗口限流
  │   ├─ Audit              → 审计日志 (PostgreSQL)
  │   ├─ InternalAuth       → 内部服务认证
  │   └─ BodySizeLimit      → 10MB 请求体限制
  │
  ▼
Handler
  │
  ├─ Parse params (shop_id, limit, product_id, days...)
  ├─ Validate input
  │
  ▼
Service
  │
  ├─ GORM query (PostgreSQL)
  ├─ Redis cache (if applicable)
  ├─ LLMRouter → Provider → AI inference
  ├─ Compute result
  │
  ▼
httputil.WriteJSON(w, status, result)
```

**路由分组 (215 路由)**：

| 前缀 | 路由数 | 功能 |
|------|--------|------|
| `/api/recommendations/*` | 3 | FBT + Trending + Stats |
| `/api/customers/insights/*` | 4 | LTV + Churn + Explain |
| `/api/customers/segments/*` | 2 | RFM 客户分层 |
| `/api/analytics/attribution/*` | 1 | 统一归因 |
| `/api/review/*` | 12 | 评价管理+自动回复+分析 |
| `/api/chat/*` | 6 | Sales Agent (SSE) |
| `/api/content/*` | 4 | 内容生成+分析 |
| `/api/tryon/*` | 5 | 虚拟试穿 |
| `/api/returns/*` | 11 | 退货管理 |
| `/api/exchange/*` | 2 | 换货推荐 |
| `/api/cart-recovery/*` | 9 | 弃单挽回 |
| `/api/marketing/*` | 4 | 营销自动化 |
| `/api/llm/*` | 4 | LLM 配置+余额 |
| `/api/social/*` | 7 | 社交媒体发布 |
| `/api/supply/*` | 3 | 供应链分析 |
| `/api/fulfillment/*` | 3 | 物流追踪 |
| `/api/inbox/*` | 4 | 客服对话 |
| `/api/cron/*` | 7 | 定时任务触发 |
| `/api/billing/*` | 5 | 计费 |
| `/api/shop/*` | 10 | 商户管理 |
| `/api/notifications/*` | 5 | 通知偏好 |
| `/api/style/*` | 7 | 样式配置 |
| `/api/seo/*` | 1 | SEO 扫描 |
| `/api/description/*` | 1 | 描述生成 |
| `/api/size/*` | 2 | 尺码推荐 |
| `/api/image/*` | 1 | 图片处理 |
| `/api/integrations/judgeme/*` | 5 | Judge.me 集成 |
| 公开路由 | 6 | Health / Ready / Metrics / Contact / Share / Webhooks |

---

## 六、中间件详解

### Gateway (API 认证)

```
X-API-Key header / Bearer token
  │
  ▼
cfg.APIToken != "" && apiKey != cfg.APIToken ?
  ├─ Yes → 401 Unauthorized
  └─ No  → 继续
            │
            ▼
          Feature Flag 检查 (Redis)
            ├─ feature:{name} = 0/false/disabled → 返回 disabled
            └─ 通过
                │
                ▼
              Cross-Tenant 检查
                X-Shop-ID header vs shop_id query param
                不一致 → 403 Forbidden
                │
                ▼
              Quota 检查 (Redis)
                quota:{shop}:{feature}:{date}
                超限 → 429 Too Many Requests
```

### Rate Limit

```
Redis 滑动窗口:
  key: ratelimit:{shop_id}:{endpoint}:{minute}
  默认: 100 req/min per shop per endpoint
  开发模式 (GO_ENV=development): 跳过
```

---

## 七、部署架构

```
┌──────────────────────────────────────────────────────────┐
│  Docker Compose (vela-infra)                             │
│                                                          │
│  ┌────────────────┐  ┌────────────────┐                  │
│  │  vela-engine   │  │  PostgreSQL    │                  │
│  │  (Go binary)   │  │  16            │                  │
│  │  port: 8000    │  │  port: 5432    │                  │
│  └───────┬────────┘  └───────┬────────┘                  │
│          │                   │                            │
│  ┌───────┴───────────────────┴────────┐                  │
│  │              Redis 7               │                  │
│  │  port: 6379                        │                  │
│  │  (EventBus + Cache + Asynq)        │                  │
│  └────────────────────────────────────┘                  │
│                                                          │
│  可选:                                                   │
│  ┌────────────┐  ┌────────────┐                          │
│  │  Qdrant    │  │  Ollama    │                          │
│  │  (RAG)     │  │  (Embed)   │                          │
│  └────────────┘  └────────────┘                          │
└──────────────────────────────────────────────────────────┘
```

**环境变量**：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `DATABASE_URL` | `postgres://vela:***@localhost:5432/vela` | PG 连接 |
| `REDIS_URL` | `redis://localhost:6379/0` | Redis 连接 |
| `PORT` | `8000` | HTTP 端口 |
| `API_TOKEN` | (空=无认证) | API 密钥 |
| `GO_ENV` | `development` | 环境 (development/production) |
| `DASHSCOPE_API_KEY` | — | 阿里云 DashScope |
| `DEEPSEEK_API_KEY` | — | DeepSeek |
| `OPENAI_API_KEY` | — | OpenAI |
| `LLM_DEFAULT_PROVIDER` | `deepseek` | 默认 LLM |
| `LLM_DEFAULT_MODEL` | `deepseek-chat` | 默认模型 |

---

## 八、可观测性

```
  /health   → {"postgres":"up/down","redis":"up/down","status":"ok"}
  /ready    → 200 (DB+Redis ready) / 503 (not ready)
  /metrics  → Prometheus 格式 (98 个指标)

  指标分类:
  ├─ HTTP:  requests_total, errors_total, active_requests, duration_ms
  ├─ DB:    db_queries_total, db_duration_ms, db_connections
  ├─ Redis: redis_commands_total, redis_duration_ms
  ├─ LLM:   llm_calls_total, llm_tokens_total, llm_duration_ms
  ├─ Sync:  sync_products_total, sync_orders_total, sync_duration_ms
  ├─ Cron:  cron_jobs_total, cron_duration_ms
  └─ Go:    goroutines, memory, gc (标准 runtime 指标)
```

---

## 九、添加新模块

```
1. 定义 Model    → internal/model/xxx.go
2. 定义 Service  → internal/service/xxx.go
3. 定义 Handler  → internal/handler/xxx.go
4. 注册路由       → internal/server/server.go (buildRouter)
5. 注册 AutoMigrate → internal/database/db.go (AutoMigrate)
6. 接入 EventBus  → server.go (Subscribe + Publish)
7. 添加测试       → internal/service/xxx_test.go
```

---

## 十、关键设计决策

| # | 决策 | 理由 |
|---|------|------|
| 1 | **Shop 多租户隔离** | 所有表通过 `shop_id` UUID 隔离，无 Shopify 依赖 |
| 2 | **Service 层纯 Go** | 零 HTTP 依赖，可直接单元测试 |
| 3 | **Equal Split 归因** | 最简单公平模型，避免时间窗口语义不一致 |
| 4 | **FBT 内存 JSONB 解析** | `synced_orders.line_items` 存为 JSONB，Go 内解析无需额外表 |
| 5 | **推荐预计算** | `product_affinities` 表缓存结果，API 读取 < 10ms |
| 6 | **LLM 供应商池化** | 按 `provider:apiKeyHash` 懒加载复用，减少连接开销 |
| 7 | **EventBus 解耦** | 数据写入 → EventBus.Publish → 下游异步消费，松耦合 |
| 8 | **ShopDomain + Domain 双列** | ShopDomain 用于 Shopify 兼容，Domain 用于 standalone |
| 9 | **LLM 路由 3 级回退** | Redis → PostgreSQL → 环境变量默认值 |
| 10 | **RAG 降级** | Qdrant 不可用时自动降级为 SQL-only，不阻断服务 |
