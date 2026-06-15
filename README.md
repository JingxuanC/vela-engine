# Vela Engine

**AI-powered commerce analytics engine.** Standalone Go service providing customer intelligence, product recommendations, and unified attribution for e-commerce platforms.

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-blue)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)

## What is Vela Engine?

Vela Engine is the AI analytics core of the Vela platform. It provides three main capabilities:

| Module | What it does | API |
|--------|-------------|-----|
| **Recommendation Engine** | Frequently Bought Together + Trending products | `/api/recommendations/*` |
| **Customer Intelligence** | LTV prediction + churn risk analysis | `/api/insights/customer*` |
| **Unified Attribution** | Cross-channel revenue attribution with overlap detection | `/api/analytics/attribution/unified` |

It runs independently — no Shopify, no third-party commerce platform required. Just PostgreSQL.

## Quick Start

### Prerequisites

- Go 1.23+
- PostgreSQL 14+

### Run

```bash
# Clone
git clone https://github.com/JingxuanC/vela-engine.git
cd vela-engine

# Start PostgreSQL (Docker)
docker run -d --name pg -p 5432:5432 -e POSTGRES_USER=vela -e POSTGRES_PASSWORD=vela -e POSTGRES_DB=vela postgres:16-alpine

# Run
DATABASE_URL="postgres://vela:***@localhost:5432/vela?sslmode=disable" go run ./cmd/server

# Health check
curl http://localhost:8000/health
# → {"status":"ok","service":"vela-engine"}
```

### Docker

```bash
docker build -t vela-engine .
docker run -p 8000:8000 \
  -e DATABASE_URL="postgres://vela:***@host.docker.internal:5432/vela?sslmode=disable" \
  vela-engine
```

## API Reference

### Recommendations

```
GET /api/recommendations/fbt?shop_id=<uuid>&product_id=<int>&limit=4
GET /api/recommendations/trending?shop_id=<uuid>&limit=8
GET /api/recommendations/stats?shop_id=<uuid>
```

### Customer Intelligence

```
GET /api/insights/customers?shop_id=<uuid>&sort=ltv_desc&limit=20
GET /api/insights/customer?shop_id=<uuid>&email=<email>
```

### Attribution

```
GET /api/analytics/attribution/unified?shop_id=<uuid>&days=30
```

### Authentication

All `/api/` routes require authentication. Pass your API token via header:

```bash
curl -H "X-API-Key: your-token" http://localhost:8000/api/recommendations/trending?shop_id=00000000-0000-0000-0000-000000000001
```

## Architecture

```
cmd/server/main.go          ← Entry point (chi router + PG)
├── internal/
│   ├── handler/            ← HTTP handlers (30+ files)
│   ├── service/            ← Business logic (50+ files)
│   │   ├── recommendation_engine.go
│   │   ├── customer_intelligence.go
│   │   └── unified_attribution.go
│   ├── model/              ← Database models (200+ structs)
│   ├── middleware/         ← Auth, CORS, rate limiting
│   ├── platform/           ← EventBus, task queue, sync
│   └── database/           ← PG connection + migration
└── pkg/
    └── httputil/           ← JSON response helpers
```

See [ARCHITECTURE.md](ARCHITECTURE.md) for full details.

## Development

```bash
# Run tests
go test ./internal/service/...

# Auto-migrate schema
go run ./cmd/server  # runs AutoMigrate on startup

# Build
go build -o vela-engine ./cmd/server
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://vela:***@localhost:5432/vela?sslmode=disable` | PostgreSQL connection string |
| `PORT` | `8000` | HTTP server port |
| `API_TOKEN` | (empty = no auth) | API key for `/api/` route protection |

## Related Projects

- [vela-storefront](https://github.com/JingxuanC/vela-storefront) — AI-generated storefront + @vela/sdk
- [vela-shopify](https://github.com/JingxuanC/vela-shopify) — Vela AI for Shopify merchants
- [vela-infra](https://github.com/JingxuanC/vela-infra) — Docker Compose + deployment configs
- [vela-docs](https://github.com/JingxuanC/vela-docs) — Design documents

## License

MIT
