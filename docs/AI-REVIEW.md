# 评价系统 — 邀请+自动回复+归因架构与数据流

> 文件: internal/service/review/ + review_inviter.go + review_invitation_attributor.go + review_revenue_attributor.go
> 依赖: PostgreSQL + EventBus + Asynq + Resend + LLMRouter

## 一、架构全景

```
订单 Fulfilled
  │
  ▼
EventOrderFulfilled
  │
  ├──────────────────────────────────────────┐
  │                                          │
  ▼                                          ▼
ReviewInviter                            FulfillmentNotifier
  │
  ├─ 创建 ReviewInvitation (PG)
  ├─ Asynq 排队 (5 天延迟)
  │
  ▼ (5 天后)
Resend 发送邀请邮件
  → Publish review.invitation_sent
  │
  ▼
顾客留评
  │
  ▼
EventReviewSynced
  │
  ├──────────────────────────────────────┐
  │                                      │
  ▼                                      ▼
ReviewInvitationAttributor           AutoReplyService
  │                                      │
  ├─ 匹配 email+product                  ├─ ReviewAdapter (JudgeMe/Native)
  ├─ 标记 converted=true                 ├─ LLM 生成回复
  └─ 归因完成                             └─ Publish review.replied
  │
  ▼
新订单 (30 天内)
  │
  ▼
ReviewRevenueAttributor
  ├─ 匹配 email+product (30 天窗口)
  ├─ 写入 review_revenue_attributions
  └─ → UnifiedAttribution 消费
```

## 二、ReviewInviter — 评价邀请

```
HandleEvent(EventOrderFulfilled):
  1. 创建 ReviewInvitation
     shop_id, customer_email, customer_name,
     order_id, product_id, product_title,
     status=pending, scheduled_at=now+5days
  
  2. Asynq 排队
     taskClient.Enqueue(TypeReviewInvitation, payload{
       ReviewInvitationID: uuid
     }, ProcessIn(5 * 24 * time.Hour))
  
  3. 5 天后
     ExecuteTask(ctx, reviewInvitationID):
       → 加载 ReviewInvitation
       → 检查状态 (未过期、未取消)
       → Resend 发送邮件
       → 更新 status=sent
       → Publish review.invitation_sent
```

## 三、ReviewInvitationAttributor — 评价归因

```
HandleEvent(EventReviewSynced):
  1. 解析评价数据 (email, product_id)
  2. 查找 ReviewInvitation
     WHERE customer_email=? AND product_id=?
       AND status='sent' AND converted=false
       AND scheduled_at > now() - 30 days
  3. 标记 converted=true, converted_at=now
  4. Publish review.invitation_converted
```

## 四、ReviewRevenueAttributor — 营收归因

```
HandleEvent(EventOrderCreated):
  1. 查找该顾客的评价邀请
     WHERE customer_email=? AND converted=true
       AND converted_at > now() - 30 days
  
  2. 匹配 product_id (订单中的商品)
  
  3. 写入 review_revenue_attributions
     shop_id, order_id, revenue,
     review_invitation_id, attributed_at
  
  4. → UnifiedAttribution 在查询时 UNION ALL 消费
```

## 五、AutoReply — 自动回复

```
HandleEvent(EventReviewSynced):
  1. 加载 AutoReplyConfig (品牌语气、回复模板)
  2. 加载评价内容
  3. ReviewAdapter → 获取平台适配器
  4. LLM 生成回复
     prompt: "你是{品牌}的客服。评价内容:{...}。
             品牌语气:{friendly/professional/luxury}。
             请生成回复："
  5. 回复到平台 (JudgeMe API)
  6. 写入 reply_records
  7. Publish review.replied
```

## 六、关键表结构

| 表 | 用途 |
|----|------|
| review_invitations | 评价邀请记录 |
| review_revenue_attributions | 营收归因 |
| reply_records | 自动回复记录 |
| auto_reply_configs | 自动回复配置 |
| auto_reply_logs | 自动回复日志 |
