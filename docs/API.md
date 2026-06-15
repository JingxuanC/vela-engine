# API Reference

Base URL: `http://localhost:8000`

All `/api/*` routes require `X-API-Key` header (configured via `API_TOKEN` env).

## Health

```
GET /health
```

Response: `{"status":"ok","service":"vela-engine"}`

---

## Recommendations

### Frequently Bought Together

```
GET /api/recommendations/fbt?shop_id=<uuid>&product_id=<int>&limit=4
```

| Param | Type | Default | Description |
|-------|------|---------|-------------|
| shop_id | UUID | required | Tenant ID |
| product_id | int64 | required | Source product |
| limit | int | 4 | Max results (max 20) |

Cold-start fallback: same-category products → trending products.

### Trending Products

```
GET /api/recommendations/trending?shop_id=<uuid>&limit=8
```

### Recommendation Stats

```
GET /api/recommendations/stats?shop_id=<uuid>
```

Response:
```json
{
  "success": true,
  "fbt_pair_count": 23,
  "trending_count": 8,
  "fbt_impressions": 0,
  "fbt_ctr": 0,
  "trending_impressions": 0,
  "trending_ctr": 0,
  "top_products": [
    {"product_id": "123", "title": "Summer Dress", "rec_revenue": 0}
  ]
}
```

---

## Customer Intelligence

### List Customers

```
GET /api/insights/customers?shop_id=<uuid>&sort=ltv_desc&limit=20&offset=0
```

| Param | Default | Description |
|-------|---------|-------------|
| sort | ltv_desc | ltv_desc / risk_asc / spent_desc / recent |
| limit | 20 | Page size |
| offset | 0 | Pagination offset |

### Single Customer Insight

```
GET /api/insights/customer?shop_id=<uuid>&email=<email>
```

Response:
```json
{
  "insight": {
    "customer_email": "vip@example.com",
    "customer_name": "VIP Customer",
    "predicted_ltv": 2400.00,
    "total_spent": 1200.00,
    "order_count": 8,
    "avg_order_value": 150.00,
    "avg_order_interval_days": 14.5,
    "active_probability": 0.72,
    "churn_risk_score": 18.5,
    "churn_risk_level": "low",
    "confidence": "high",
    "segment": "VIP",
    "data_points": 8
  }
}
```

---

## Unified Attribution

```
GET /api/analytics/attribution/unified?shop_id=<uuid>&days=30
```

Response:
```json
{
  "success": true,
  "total_actual_revenue": 45000,
  "total_attributed_revenue": 38000,
  "overlap_rate": 15.5,
  "channels": [
    {
      "channel": "cart_recovery",
      "attributed_orders": 45,
      "attributed_revenue": 16000,
      "shared_revenue": 2000
    },
    {
      "channel": "content",
      "attributed_orders": 120,
      "attributed_revenue": 19000,
      "shared_revenue": 3000
    }
  ],
  "overlap_matrix": [
    {"pair": "cart_recovery + content", "orders": 12, "revenue": 1500},
    {"pair": "cart_recovery + review", "orders": 5, "revenue": 500},
    {"pair": "content + review", "orders": 8, "revenue": 500},
    {"pair": "all three", "orders": 3, "revenue": 200}
  ]
}
```

**Equal Split model**: Revenue is divided equally among all attribution channels for the same order.

**overlap_rate**: Percentage of attributed revenue that is shared across multiple channels.
