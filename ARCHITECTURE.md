# Architecture — Vela Engine

## Overview

Vela Engine is a standalone Go HTTP service providing AI-powered commerce analytics. It follows a layered architecture with clean separation of concerns.

```
┌──────────────────────────────────────────────────┐
│                  HTTP Layer                       │
│  chi router + middleware (auth, CORS, ratelimit)  │
├──────────────────────────────────────────────────┤
│                  Handler Layer                    │
│  Parse request → validate → call service → respond│
├──────────────────────────────────────────────────┤
│                  Service Layer                    │
│  Business logic — pure Go, no HTTP concerns       │
├──────────────────────────────────────────────────┤
│                  Model Layer                      │
│  GORM models → PostgreSQL tables                 │
└──────────────────────────────────────────────────┘
```

## Directory Structure

```
vela-engine/
├── cmd/server/main.go          # Entry point
│
├── internal/
│   ├── handler/                 # HTTP handlers (30+ files)
│   │   ├── recommendations.go   # GET /api/recommendations/*
│   │   ├── customer_insights.go # GET /api/insights/*
│   │   ├── unified_attribution.go # GET /api/analytics/attribution/unified
│   │   └── ...
│   │
│   ├── service/                 # Business logic (50+ files)
│   │   ├── recommendation_engine.go  # FBT + Trending algorithm
│   │   ├── customer_intelligence.go  # LTV + churn prediction
│   │   ├── unified_attribution.go    # Cross-channel attribution
│   │   └── ...
│   │
│   ├── model/                   # Database models (GORM)
│   │   └── models.go            # Shop, Order, Product, Customer...
│   │
│   ├── middleware/               # HTTP middleware
│   │   ├── gateway.go           # API key auth + feature flags + quota
│   │   ├── cors.go              # CORS configuration
│   │   ├── ratelimit.go         # Per-endpoint rate limiting
│   │   └── audit.go             # Request audit logging
│   │
│   ├── platform/                # Infrastructure
│   │   ├── eventbus/            # In-process event bus
│   │   ├── taskqueue/           # Asynq (Redis-backed job queue)
│   │   └── ...
│   │
│   ├── database/                # Database utilities
│   │   └── db.go                # Connection pool + retry
│   │
│   └── config/                  # Configuration
│       └── config.go            # env-based config loading
│
└── pkg/
    └── httputil/                # Shared HTTP utilities
        └── response.go          # WriteOK / WriteError helpers
```

## Request Flow

```
Client
  │
  ▼
chi Router
  │
  ├── Middleware Chain
  │   ├── RequestID
  │   ├── RealIP
  │   ├── Gateway (API Key Auth)
  │   │   ├── Validate X-API-Key
  │   │   ├── Feature flag check (Redis)
  │   │   ├── Cross-tenant check (X-Shop-ID)
  │   │   └── Quota check (Redis)
  │   └── Logger
  │
  ▼
Handler
  │
  ├── Parse query params (shop_id, limit, product_id...)
  ├── Validate input
  │
  ▼
Service
  │
  ├── Query PostgreSQL via GORM
  ├── Compute (LTV formula, FBT pairs, attribution splits)
  │
  ▼
Response
  └── JSON via httputil.WriteOK
```

## Key Design Decisions

### 1. Shop as Tenant Boundary

All multi-tenant isolation is through `shop_id` (UUID). No Shopify-specific authentication. Handlers accept `shop_id` as a query parameter, and the gateway middleware validates it.

### 2. Service Layer is Pure Go

Service packages have zero HTTP dependency. They accept `context.Context`, query GORM, compute results, and return Go structs. This makes them testable without HTTP fixtures.

### 3. Equal Split Attribution

When an order is attributed by multiple channels, revenue is divided equally. This is the simplest fair model and avoids the complexity of time-based attribution (which requires consistent timestamp semantics across data sources).

### 4. Precomputed vs Real-time

- **Recommendations**: Precomputed periodically, stored in `product_affinities` table. API reads fast.
- **Customer Insights**: Precomputed on demand, cached in `customer_insights` table.
- **Attribution**: Real-time aggregation query over raw attribution tables.

### 5. Removed Shopify Coupling

The original codebase (vela-shopify) was already well-abstracted. Only 3 changes were needed:

| Original | Standalone |
|----------|-----------|
| Shopify OAuth/JWT gateway | X-API-Key header auth |
| `Shop.ShopDomain` + `AccessToken` | `Shop.Domain` only |
| Shopify-specific handlers (fulfillment, judgeme) | Removed |

## Database Schema

Core tables (auto-migrated on startup):

```
shops              — Tenant accounts (id, domain, plan, billing)
synced_orders      — Order data (shop_id, platform_id, total_price, line_items)
synced_products    — Product catalog (shop_id, platform_id, title, images, variants)
customer_insights  — Precomputed LTV/churn per customer
product_affinities — Precomputed recommendation pairs (FBT, trending)
analytics_events   — Raw analytics events for attribution
review_revenue_attributions — Revenue attributed to review conversions
cart_recovery_codes — Discount code usage for cart recovery attribution
```

## Adding a New Endpoint

1. **Define the handler** in `internal/handler/`
2. **Define the service** in `internal/service/`
3. **Define the model** in `internal/model/` (if new table)
4. **Register the route** in `cmd/server/main.go`
5. **Add the table to auto-migrate** in `models.go`

## Testing

```bash
# Run all service tests
go test ./internal/service/...

# Run specific package
go test ./internal/service/recommendation/...
go test ./internal/service/attribution/...
```
