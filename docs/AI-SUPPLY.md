# Supply Chain — 供应链分析架构与数据流

> 文件: internal/handler/supply.go + internal/service/supply_insight.go
> 依赖: PostgreSQL + Google Trends

## 一、架构定位

Supply Chain 模块提供库存预测、退货异常检测和趋势匹配。

```
GET /api/supply/stock-prediction
GET /api/supply/return-anomalies
GET /api/supply/trend-match
  │
  ▼
SupplyHandler
  │
  ├─ StockPrediction:
  │   基于 synced_orders 历史销量
  │   + 季节性因子
  │   → 预测未来 7/14/30 天需求量
  │
  ├─ ReturnAnomalies:
  │   分析 synced_returns + return_items
  │   检测退货率异常 (>30%)
  │   + 原因分析 (尺寸/颜色/质量)
  │
  └─ TrendMatch:
      对比 Google Trends 数据
      识别上升趋势品类
      → 推荐进货建议
```

## 二、Stock Prediction

```
StockPrediction(shopID, productID, days):
  1. 加载历史销售数据
     SELECT date(order_created_at), COUNT(*), SUM(total_price)
     FROM synced_orders
     WHERE shop_id=? AND product_id=?
       AND order_created_at > now() - 90 days
     GROUP BY date

  2. 移动平均 + 季节性
     7天移动平均 → 基线
     + 周季节性因子 (工作日 vs 周末)
     + 月季节性因子 (月初 vs 月末)

  3. 预测输出
     predicted_units: [N+1, N+2, ..., N+days]
     confidence: 基于数据量的可信度
     seasonality_factor: 季节性影响系数
```

## 三、Return Anomaly

```
ReturnAnomalies(shopID):
  1. 计算总体退货率
     SELECT COUNT(*) FILTER (WHERE status='returned')
            / COUNT(*) * 100
     FROM synced_orders WHERE shop_id=?

  2. 按产品分析
     GROUP BY product_id
     return_rate > 30% → 标记异常

  3. 按原因分析
     GROUP BY return_reason
     主要退货原因: size_too_large, size_too_small,
                   wrong_color, quality_issue

  4. 输出
     anomaly_products[]: {product_id, return_rate, top_reason}
     overall_return_rate: float
     trend: improving/worsening
```

## 四、Trend Match

```
TrendMatch(shopID, category):
  1. Google Trends 数据
     trendsClient.FetchInterest(category, geo, timeframe)
     → 过去 90 天搜索热度

  2. 店内数据
     同品类产品销量趋势

  3. 匹配分析
     Google Trends 上升 + 店内缺货 → 推荐进货
     Google Trends 下降 + 店内高库存 → 建议清仓
```

## 五、Google Trends 集成

```
GoogleTrendsClient:
  FetchInterest(keyword, geo, timeframe):
    → HTTP GET trends.google.com/trends/api/explore
    → 获取 widget token
    → HTTP GET trends.google.com/trends/api/widgetdata
    → 解析 JSON (strip )]}' prefix)
    → 返回 timelineData[]
  
  Redis 缓存: TTL=6h
