# Middleware — 中间件链架构

> 文件: internal/middleware/
> 依赖: PostgreSQL + Redis

## 一、执行顺序

```
Request
  │
  ▼
chi Router
  │
  ├─ 1. RequestID       → X-Request-ID header
  ├─ 2. RealIP          → X-Forwarded-For / X-Real-IP
  ├─ 3. Observability   → Prometheus counter (requests_total)
  ├─ 4. Logging         → slog (method, path, status, duration)
  ├─ 5. CORS            → Access-Control-* headers
  ├─ 6. Recoverer       → panic → 500
  ├─ 7. Gateway         → API Key / Feature Flag / Quota
  ├─ 8. RateLimit       → Redis sliding window (100 req/min)
  ├─ 9. Audit           → PostgreSQL audit_logs
  ├─ 10. InternalAuth   → X-Internal-Secret (dev mode skip)
  └─ 11. BodySizeLimit  → 10MB max
  │
  ▼
Handler
```

## 二、Gateway 详解

```
Gateway(cfg, cache, shopIDResolver):
  
  1. 公开路径直通:
     !strings.HasPrefix("/api/") || "/api/contact"
     → next.ServeHTTP
  
  2. API Key 认证:
     apiKey = X-API-Key header 或 Bearer token
     if cfg.APIToken != "" && apiKey != cfg.APIToken:
       → 401 Unauthorized
  
  3. Feature Flag 检查 (Redis):
     feature = extractFeatureFromPath("/api/recommendations/trending")
              → "recommendations"
     flagKey = "feature:recommendations"
     if value == "0"/"false"/"disabled":
       → 200 {"disabled": true}
  
  4. Cross-Tenant 检查:
     X-Shop-ID header vs shop_id query param
     不一致 → 403 Cross-tenant access denied
  
  5. ShopID 注入:
     effectiveShopID = header || query
     ctx = context.WithValue(ctx, "shop_id", shopID)
  
  6. Quota 检查 (Redis):
     quotaKey = "{shopID}:{feature}"
     CheckQuota(key, date, limit)
     超限 → 429 Too Many Requests
     Retry-After: 86400
```

## 三、RateLimit

```
RateLimit(cache, limits):
  key = "ratelimit:{shop}:{endpoint}:{minute}"
  sliding window: INCR + EXPIRE 60s
  超限 → 429 Too Many Requests
  开发模式: 跳过
```

## 四、Audit

```
Audit(db):
  /api/* 路径自动记录:
    shop_id, method, path, status_code,
    duration_ms, remote_addr, user_agent
  
  写入 PostgreSQL audit_logs
  db 为 nil 时静默跳过
```

## 五、InternalAuth

```
InternalAuth:
  公开路径: /, /health, /ready, /metrics, /api/contact, /share/*
    → 直通
  
  开发模式 (GO_ENV=development):
    → 直通
  
  生产模式:
    X-Internal-Secret header == INTERNAL_SECRET env
    不匹配 → 401 unauthorized
```
