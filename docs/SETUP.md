# Setup Guide

## Docker (Recommended)

```bash
# 1. Clone
git clone https://github.com/JingxuanC/vela-engine.git
cd vela-engine

# 2. Start PostgreSQL
docker run -d --name vela-pg \
  -e POSTGRES_USER=vela \
  -e POSTGRES_PASSWORD=*** \
  -e POSTGRES_DB=vela \
  -p 5432:5432 \
  postgres:16-alpine

# 3. Build & run
docker build -t vela-engine .
docker run -d --name vela-engine \
  -p 8000:8000 \
  -e DATABASE_URL="postgres://vela:***@host.docker.internal:5432/vela?sslmode=disable" \
  -e API_TOKEN="my-secret-token" \
  vela-engine

# 4. Verify
curl http://localhost:8000/health
# → {"status":"ok","service":"vela-engine"}
```

## Docker Compose

```yaml
# docker-compose.yml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: vela
      POSTGRES_PASSWORD: ***    POSTGRES_DB: vela
    ports:
      - "5432:5432"
    volumes:
      - pg_data:/var/lib/postgresql/data

  engine:
    build: .
    ports:
      - "8000:8000"
    environment:
      DATABASE_URL: postgres://vela:***@postgres:5432/vela?sslmode=disable
      API_TOKEN: my-secret-token
    depends_on:
      - postgres

volumes:
  pg_data:
```

```bash
docker compose up -d
```

## Local Development

```bash
# Prerequisites: Go 1.23+, PostgreSQL 14+

# Clone
git clone https://github.com/JingxuanC/vela-engine.git
cd vela-engine

# Run (auto-migrates schema)
DATABASE_URL="postgres://vela:***@localhost:5432/vela?sslmode=disable" go run ./cmd/server

# Build binary
go build -o vela-engine ./cmd/server

# Run tests
go test ./internal/service/...
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `DATABASE_URL` | Yes | `postgres://vela:***@localhost:5432/vela?sslmode=disable` | PostgreSQL connection |
| `PORT` | No | `8000` | HTTP listen port |
| `API_TOKEN` | No | (empty) | API key for route protection |

## First Request

After starting, make your first API call:

```bash
# Test with default dev shop ID
curl -H "X-API-Key: my-secret-token" \
  "http://localhost:8000/api/recommendations/trending?shop_id=00000000-0000-0000-0000-000000000001"
```

## Data Setup

The engine needs order and product data to produce useful results. You can:

1. **Import from Shopify** — use the sync module in [vela-shopify](https://github.com/JingxuanC/vela-shopify)
2. **Import from Medusa** — Medusa stores data in PostgreSQL, can be read directly
3. **Manual import** — Insert into `synced_orders` and `synced_products` tables

## Troubleshooting

### "Connection refused" on PostgreSQL

Check PostgreSQL is running and reachable:
```bash
docker ps | grep postgres
psql "$DATABASE_URL" -c "SELECT 1"
```

### Tables not created

Auto-migration runs on startup. Check logs for migration status. If tables exist but have wrong schema, drop and restart:
```bash
psql "$DATABASE_URL" -c "DROP TABLE IF EXISTS customer_insights, product_affinities CASCADE"
# Restart vela-engine
```
