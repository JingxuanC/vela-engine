# Database 数据模型与迁移 — 架构文档

> 文件: internal/database/ + internal/model/
> 依赖: PostgreSQL 16 + GORM

## 一、架构定位

PostgreSQL 是 Vela Engine 的单一事实来源。所有业务数据、AI 分析结果、配置和审计日志存于此。

```
┌──────────────────────────────────────────────────────┐
│                 PostgreSQL 16                        │
│                                                     │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────┐ │
│  │  同步数据     │  │  AI 分析结果  │  │  配置/审计 │ │
│  │  (6 表)      │  │  (12 表)     │  │  (8 表)   │ │
│  │              │  │              │  │           │ │
│  │ synced_      │  │ customer_    │  │ shops     │ │
│  │ products     │  │ insights     │  │ shop_llm_ │ │
│  │ orders       │  │ product_     │  │ configs   │ │
│  │ customers    │  │ affinities   │  │ audit_logs│ │
│  │ reviews      │  │ review_*_    │  │ api_keys  │ │
│  │ returns      │  │ attributions │  │ ...       │ │
│  │ checkouts    │  │ chat_intent_ │  │           │ │
│  │              │  │ events       │  │           │ │
│  └──────────────┘  └──────────────┘  └───────────┘ │
│                                                     │
│  全部 shop_id (UUID) 多租户隔离                      │
│  GORM AutoMigrate 自动建表                           │
└──────────────────────────────────────────────────────┘
```

## 二、连接池

```go
func Connect(databaseURL string) (*gorm.DB, error)
  → gorm.Open(postgres.Open(dsn), config)
  → 重试 3 次 (指数退避: 1s, 2s)
  → 连接池:
      MaxIdleConns:    10
      MaxOpenConns:    50
      ConnMaxLifetime: 30 min
      ConnMaxIdleTime:  5 min
```

## 三、AutoMigrate 数据流

```
server.New() 启动时:
  database.Connect(dsn) → *gorm.DB
  database.AutoMigrate(db)
    → 遍历 40+ 模型
    → db.AutoMigrate(&model.Xxx{})
    → 每个表独立迁移，失败不阻断
    → 日志: "migration succeeded" / "migration failed"
  database.Seed(db)
    → 仅当 shops 表为空时插入种子数据
    → 3 个测试店铺 + 示例数据
```

## 四、模型规范

```go
type ModelName struct {
    ID        uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
    ShopID    uuid.UUID  `gorm:"index;not null"`
    // 业务字段...
    CreatedAt time.Time  `gorm:"autoCreateTime"`
    UpdatedAt time.Time  `gorm:"autoUpdateTime"`
}
```

| 规范 | 说明 |
|------|------|
| UUID 主键 | gen_random_uuid()，非自增 |
| ShopID | 所有表必带，多租户隔离 |
| JSONB | datatypes.JSON，变体/选项/行项 |
| 软删除 | UninstalledAt 标记（非物理删除） |
| 时间戳 | autoCreateTime / autoUpdateTime |

## 五、核心表分类

### 同步数据 (6 表)
| 表 | 数据源 | 唯一约束 |
|----|--------|---------|
| synced_products | REST API | shop_id + platform_id |
| synced_orders | REST API | shop_id + platform_id |
| synced_customers | REST API | shop_id + platform_id |
| synced_reviews | JudgeMe API | shop_id + platform_id |
| synced_returns | Webhook | shop_id + platform_id |
| synced_checkouts | Webhook | shop_id + platform_id |

### AI 分析结果 (12 表)
| 表 | 用途 |
|----|------|
| product_affinities | FBT/Trending 预计算结果 |
| customer_insights | LTV + 流失预测 |
| review_revenue_attributions | 评价营收归因 |
| review_recovery_attributions | 评价挽回归因 |
| chat_intent_events | Sales Agent 意图信号 |
| product_chat_insights | 产品级 Chat 分析 |
| customer_chat_profiles | 客户 Chat 画像 |
| shop_insights | AI 洞察（退货异常等） |
| analytics_events | 原始分析事件 |
| attribution_models | 归因模型 |
| predictive_scores | 预测分数 |
| content_performances | 内容表现 |

### 业务数据 (8+ 表)
returns, exchange_orders, cart_recovery_campaigns, marketing_flows, review_invitations, reply_records, content_pieces, fulfillment_events...

### 配置/审计 (8 表)
shops, shop_llm_configs, api_keys, audit_logs, notification_prefs, token_usages, sales_agent_configs, billing...

## 六、ResolveShopID

```go
func ResolveShopID(db *gorm.DB, shopIDStr string) (uuid.UUID, error)
  1. uuid.Parse(shopIDStr)          // 尝试 UUID 格式
  2. db.Where("shop_domain = ?")     // 回退域名查询
```

## 七、GDPR 清理

```
商家卸载时:
  CleanupAll(shopID)
    → 删除 28 张表中所有 shop_id 匹配的行
    → 标记 shops.uninstalled_at
    → 不移除 Shop 记录（保留用于审计）
```

| 决策 | 原因 |
|------|------|
| UUID 主键 | 跨平台兼容，无序列冲突 |
| OnConflict UpdateAll | 幂等同步 |
| JSONB 存储变体 | 灵活性强，无需关联表 |
| 独立迁移 | 单表失败不影响其他表 |
| 软删除 | GDPR 合规 + 审计保留 |
