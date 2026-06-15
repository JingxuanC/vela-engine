# 客户智能引擎 — LTV + 流失预测架构与数据流

> 文件: internal/service/customer_intelligence.go
> 依赖: PostgreSQL (synced_orders) + LLMRouter (AI 解释)

## 一、架构定位

CustomerIntelligenceEngine 基于历史订单数据预测客户生命周期价值(LTV)和流失风险。

```
synced_orders
  │
  ▼
CustomerIntelligenceEngine
  │
  ├─ ComputeAll(shopID) → 批量计算
  │   ├─ 聚合阶段 (1 SQL + Go 内聚合)
  │   ├─ LTV 公式计算
  │   └─ 流失风险评估
  │
  ├─ Upsert → customer_insights
  │
  └─ API
      GET  /api/customers/insights/
      GET  /api/customers/insights/{email}
      GET  /api/customers/insights/{email}/explain
           └─ LLMRouter → AI 解释
```

## 二、数据流

### 2.1 批量计算 (ComputeAll)

```
ComputeAll(ctx, shopID):
  1. 加载所有订单
     SELECT customer_email, total_price, order_created_at
     FROM synced_orders
     WHERE shop_id=? AND order_created_at > now() - 365 days

  2. Go 内聚合 (GROUP BY customer_email)
     orderAggRow {
         CustomerEmail, CustomerName,
         OrderCount, TotalSpent,
         FirstOrderAt, LastOrderAt
     }

  3. 加载单笔订单 (趋势计算)
     SELECT customer_email, total_price, order_created_at
     ORDER BY order_created_at DESC
     最近 3 笔 vs 早期 3 笔 → AvgRecentValue, AvgEarlyValue

  4. 计算 LTV + 流失指标 (见第三节)

  5. Upsert → customer_insights
     ON CONFLICT (shop_id, customer_email) UPDATE
```

### 2.2 定时刷新

```
Asynq TypeCustomerIntelRefresh (每天 2:00 AM)
  → 遍历所有 active shops
  → ComputeAll(ctx, shopID)
  → 日志: "customer intelligence enqueued for N shops"
```

## 三、LTV 公式

```
LTV Prediction = AvgOrderValue × OrdersPerYear × AvgYearsActive

其中:
  AvgOrderValue  = TotalSpent / OrderCount
  OrdersPerYear  = OrderCount / ActiveYears
  ActiveYears    = (LastOrderAt - FirstOrderAt) / 365 + 0.5
                   最少 0.5 年 (新客户)
  DiscountFactor = exp(-0.1 × MonthsSinceLastOrder)
                   衰减因子 (最近购买越久，价值越低)

Confidence:
  DataPoints < 2  → 25%
  2-5 orders      → 50%
  >5 orders       → 75%
```

## 四、流失风险公式

```
ChurnRiskScore (0-100) =
  RecencyScore (40%)  =
    100 × min(DaysSinceLastOrder / 180, 1.0)
    (超过 180 天未购买 = 满分)

  + FrequencyDrop (30%) =
    100 × (1 - RecentOrdersPerMonth / EarlyOrdersPerMonth)
    最近购买频率下降

  + ValueDrop (30%) =
    100 × (1 - AvgRecentValue / AvgEarlyValue)
    近期客单价下降

ActiveProbability = 1 - ChurnRiskScore / 100

RiskLevel:
  score > 70 → high
  40-70     → medium
  <40       → low
```

## 五、customer_insights 表

```sql
CREATE TABLE customer_insights (
    id                   UUID PRIMARY KEY,
    shop_id              UUID NOT NULL,
    customer_email       VARCHAR(255) NOT NULL,
    customer_name        VARCHAR(255),
    total_orders         INT,
    total_spent          FLOAT,
    avg_order_value      FLOAT,
    avg_order_interval_days FLOAT,
    predicted_ltv        FLOAT,
    churn_risk_score     FLOAT,
    churn_risk_level      VARCHAR(20),        -- high/medium/low
    active_probability    FLOAT,
    avg_recent_value     FLOAT,
    avg_early_value      FLOAT,
    confidence           FLOAT,               -- 0.25/0.50/0.75
    data_points          INT,
    first_order_at       TIMESTAMPTZ,
    last_order_at        TIMESTAMPTZ,
    computed_at          TIMESTAMPTZ,
    UNIQUE(shop_id, customer_email)
);
```

## 六、AI 解释 (ExplainInsight)

```
GET /api/customers/insights/{email}/explain?shop_id=

  → CustomerIntelligenceEngine.GetOneInsight(email)
  → 构建 Prompt (25 字内中文总结 + 1 条操作建议)
  → llmRouter.GetProvider(shopID)
  → LLM ChatCompletion
  → 返回 {"explanation": "..."}
```

## 七、关键设计决策

| 决策 | 原因 |
|------|------|
| Go 内聚合而非纯 SQL | 灵活，LTV 公式可快速迭代 |
| 衰减因子 | 避免历史订单过度影响 LTV |
| 三级置信度 | 透明展示预测可靠性 |
| LLM 解释独立 API | 轻量查询 + 可选 AI 增强 |
| 每日定时刷新 | 平衡实时性和计算成本 |
