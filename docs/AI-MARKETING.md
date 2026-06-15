# Marketing Automation — 营销自动化架构与数据流

> 文件: internal/service/marketing_flow.go + rfm.go + handler/marketing.go
> 依赖: PostgreSQL + EventBus + Resend

## 一、架构定位

Marketing Automation 基于 RFM 客户分层和事件触发器，自动执行营销流程。

```
┌──────────────────────────────────────────────────────┐
│              Marketing Automation                     │
│                                                      │
│  数据输入:                                            │
│  ┌──────────────┐  ┌──────────────┐                 │
│  │ RFM Engine   │  │ EventBus     │                 │
│  │ (Recency/    │  │ (Order/      │                 │
│  │  Frequency/  │  │  Cart/       │                 │
│  │  Monetary)   │  │  Customer)   │                 │
│  └──────┬───────┘  └──────┬───────┘                 │
│         │                 │                          │
│         └────────┬────────┘                          │
│                  ▼                                    │
│  ┌─────────────────────────────────────┐            │
│  │  MarketingFlowEngine                │            │
│  │                                     │            │
│  │  Triggers:                          │            │
│  │  ├─ order_placed                    │            │
│  │  ├─ order_fulfilled                 │            │
│  │  ├─ customer_dormant (90天无购买)    │            │
│  │  ├─ cart_abandoned                  │            │
│  │  └─ review_received                 │            │
│  │                                     │            │
│  │  Templates (5):                     │            │
│  │  ├─ welcome (新客户欢迎)             │            │
│  │  ├─ dormant_reactivate (流失唤醒)    │            │
│  │  ├─ post_purchase (购后关怀)         │            │
│  │  ├─ cart_recovery (弃单挽回)         │            │
│  │  └─ review_request (评价邀请)        │            │
│  └─────────────────┬───────────────────┘            │
│                    │                                  │
│                    ▼                                  │
│  ┌─────────────────────────────────────┐            │
│  │  MarketingFlowRun → PostgreSQL      │            │
│  │  (trigger_type, template, status,    │            │
│  │   customer_email, sent_at)           │            │
│  │                                     │            │
│  │  → Resend 发送邮件                   │            │
│  └─────────────────────────────────────┘            │
└──────────────────────────────────────────────────────┘
```

## 二、RFM Engine

```
RFMEngine.ComputeAll(shopID):
  1. 加载 synced_orders (过去 365 天)
  
  2. 计算 RFM 分数:
     Recency:   1-5 (越近越高)
     Frequency: 1-5 (越多越高)
     Monetary:  1-5 (越高越高)
  
  3. 分层:
     Champions:      R5F5M5
     Loyal:          R4-5 F4-5 M4-5
     At Risk:        R1-2 F3-5 M3-5
     Dormant:        R1 F1-2 M1-2
  
  4. Upsert → customer_segments (PostgreSQL)
```

## 三、触发器

```
MarketingFlowEngine 订阅事件:

  EventOrderCreated:
    → 触发 order_placed
    → 运行 welcome + post_purchase flow
  
  EventCheckoutCreated:
    → 启动 60min 定时器
    → 如果无对应 EventOrderCreated → 触发 cart_abandoned
  
  定时扫描 (每天):
    ScanDormantCustomers:
      → 查找 90 天无购买的客户
      → 触发 customer_dormant
      → 运行 dormant_reactivate flow
```

## 四、去重

```
每次触发前检查 MarketingFlowRun:
  WHERE shop_id=? AND customer_email=? AND trigger_type=?
    AND created_at > now() - 24h
  
  24 小时内同一触发类型不重复执行
```

## 五、关键表

| 表 | 用途 |
|----|------|
| marketing_flows | 营销流程定义 |
| marketing_flow_runs | 流程执行记录 |
| customer_segments | RFM 分层结果 |
| cart_recovery_campaigns | 弃单挽回活动 |
| cart_recovery_codes | 折扣码 |
| cart_recovery_sends | 发送记录 |
