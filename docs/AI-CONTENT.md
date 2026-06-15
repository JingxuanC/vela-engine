# Content Factory — AI 内容生成与归因架构

> 文件: internal/handler/content.go + content_analytics.go + content_attribution.go
> 依赖: LLMRouter + EventBus

## 一、架构定位

Content Factory 使用 LLM 生成产品描述、博客文章、社交媒体内容，并通过 UTM 参数追踪内容转化效果。

```
POST /api/content/generate
  {type: "product_description", product_id: "...", platform: "shopify"}
  │
  ▼
ContentHandler.Generate
  │
  ├─ 加载产品信息 (synced_products)
  ├─ LLMRouter → LLM 生成
  │   prompt: "为以下产品生成 SEO 优化的描述..."
  │
  ├─ ContentJob → PostgreSQL
  │   shop_id, type, product_id, content, platform, status
  │
  ├─ Publish content.generated
  │
  ▼
ContentAttributor.OnContentGenerated
  │
  ├─ 嵌入 UTM 参数: utm_source=vela&utm_medium=ai&utm_campaign=content_{id}
  ├─ 发布到社交平台 (Pinterest, Meta, YouTube)
  │
  ▼ (顾客通过 UTM 链接访问 → 下单)
EventOrderCreated
  │
  ▼
ContentAttributor 匹配 UTM 参数
  │
  ├─ 解析 Shopify order landingPage URL
  ├─ 提取 UTM 参数
  ├─ 匹配 ContentJob
  └─ 写入 content_performances
```

## 二、内容类型

| 类型 | 用途 | 输出平台 |
|------|------|---------|
| product_description | 产品描述 | Shopify / 独立站 |
| blog_post | 博客文章 | 独立站 / Medium |
| social_post | 社交媒体帖子 | Pinterest / Meta / YouTube |
| email_campaign | 邮件营销内容 | Resend |
| ad_copy | 广告文案 | Meta / Google Ads |

## 三、社交发布 (Social)

```
Content → SocialHandler.Publish
  │
  ├─ Pinterest: Create Pin (image + description + link)
  ├─ Meta:      Create Post (text + image + link)
  └─ YouTube:   Create Video/Short (video + description + title)
  │
  ▼
SocialPost → PostgreSQL
  追踪: impressions, clicks, engagements
  定时拉取指标 (ContentMetricsPuller, 每 6 小时)
```

## 四、内容归因数据流

```
ContentAttributor (EventBus Consumer):
  OnContentGenerated:
    → 不直接写入归因数据
    → 仅记录内容元数据

  OnOrderCreated (Webhook):
    → 获取订单的 landingPage URL
    → 解析 UTM 参数:
        utm_source=vela
        utm_medium=ai
        utm_campaign=content_{jobID}
    → 匹配 ContentJob
    → 写入 analytics_events (channel=content)

  → UnifiedAttribution 在查询时 UNION ALL 消费
```

## 五、关键表

| 表 | 用途 |
|----|------|
| content_jobs | 内容生成任务 |
| content_pieces | 生成的内容 |
| content_performances | 内容表现 (归因) |
| content_platform_metrics | 平台指标 |
| social_connections | 社交平台连接 |
| social_posts | 已发布帖子 |
