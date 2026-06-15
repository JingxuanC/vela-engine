# Vela Engine

**AI-powered commerce operating system.** Standalone Go service — customer intelligence, product recommendations, unified attribution, AI sales agent, content factory, and more. For any e-commerce platform.

[![Go Version](https://img.shields.io/badge/Go-1.26%2B-blue)](https://go.dev)
[![License](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Routes](https://img.shields.io/badge/API%20routes-215-brightgreen)]()

## What is Vela Engine?

Vela Engine is the AI core of the Vela platform, extracted from the Shopify SaaS app into a standalone service. It connects to any PostgreSQL database and provides a complete AI-powered commerce suite:

| Module | Capability | Key API |
|--------|-----------|---------|
| Recommendation Engine | FBT + Trending (in-memory JSONB parsing) | /api/recommendations |
| Customer Intelligence | LTV prediction + churn risk + AI explanation | /api/customers/insights |
| Unified Attribution | Cross-channel revenue (Equal Split model) | /api/analytics/attribution |
| Sales Agent | SSE streaming chat, 8 context injectors | /api/chat/stream |
| Content Factory | AI-generated descriptions, blogs, social | /api/content |
| Review System | Auto-invite, auto-reply, revenue attribution | /api/review |
| Marketing Automation | RFM segments, flow engine, cart recovery | /api/marketing |
| Supply Chain | Stock prediction, return anomaly, trends | /api/supply |
| RAG | Vector search over products/reviews | integrated in chat |
| LLM Config | Multi-provider routing (Qwen/DeepSeek/GPT) | /api/llm |

No Shopify. No vendor lock-in. Just PostgreSQL + Redis.

## Quick Start

```bash
git clone https://github.com/JingxuanC/vela-engine.git
cd vela-engine

# Start PostgreSQL and Redis
docker run -d --name pg -p 5432:5432 -e POSTGRES_USER=vela -e POSTGRES_PASSWORD=vela -e POSTGRES_DB=vela postgres:16-alpine
docker run -d --name redis -p 6379:6379 redis:7-alpine

# Run
DATABASE_URL=postgres://vela:vela@localhost:5432/vela?sslmode=disable REDIS_URL=redis://localhost:6379/0 go run ./cmd/server

# With LLM keys
DASHSCOPE_API_KEY=your-key go run ./cmd/server
```

Health check:
```bash
curl http://localhost:8000/health
# {"postgres":"up","redis":"up","status":"ok"}
```

Authenticated API call:
```bash
curl -H "X-API-Key: $API_TOKEN" \
  "http://localhost:8000/api/recommendations/trending?shop_id=00000000-0000-0000-0000-000000000001"
```

## Architecture

```
cmd/server/main.go          -> server.New(cfg) -> 215 routes
├── internal/
│   ├── server/             Full server wiring (1461 lines)
│   ├── handler/            HTTP handlers (60+ files)
│   ├── service/            Business logic (50+ files)
│   ├── model/              GORM models (200+ structs, 30+ tables)
│   ├── middleware/         Auth, CORS, rate limiting, audit (8 total)
│   ├── platform/           EventBus (Redis Streams), Asynq, Sync, RAG, Insight
│   ├── database/           PG connection pool + auto-migration
│   └── config/             env-based configuration
└── pkg/httputil/           JSON response helpers
```

Data flow: External data -> Sync -> PostgreSQL -> EventBus.Publish -> Consumers -> AI -> API

**Full architecture: [ARCHITECTURE.md](ARCHITECTURE.md)** — 10 sections covering data infrastructure, AI capabilities, event flows, and deployment.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| DATABASE_URL | postgres://vela:vela@localhost:5432/vela | PostgreSQL |
| REDIS_URL | redis://localhost:6379/0 | Redis |
| PORT | 8000 | HTTP port |
| API_TOKEN | (empty=no auth) | API key |
| GO_ENV | development | Environment |
| DASHSCOPE_API_KEY | — | Alibaba DashScope (Qwen) |
| DEEPSEEK_API_KEY | — | DeepSeek |
| OPENAI_API_KEY | — | OpenAI |
| LLM_DEFAULT_PROVIDER | deepseek | Default LLM provider |
| LLM_DEFAULT_MODEL | deepseek-chat | Default LLM model |

## Development

```bash
go run ./cmd/server          # Run
air                          # Live reload
go test -race ./internal/service/...  # Test
CGO_ENABLED=0 go build -o vela-engine ./cmd/server  # Build
```

## Data Infrastructure

- **EventBus**: Redis Streams pub/sub — 10+ event types, 15+ consumers. Data changes trigger downstream AI processing.
- **Asynq**: Redis task queue — LTV refresh daily, FBT daily, Trending hourly, review invites with 5-day delay.
- **Sync Pipeline**: REST/GraphQL -> PostgreSQL -> EventBus -> AI analysis.
- **Snapshot Cache**: Aggregated data in Redis (1h TTL), 6+ PG queries -> 1 Redis read.

## Related Projects

| Project | Description |
|---------|-------------|
| [vela-storefront](https://github.com/JingxuanC/vela-storefront) | AI storefront + sdk |
| [vela-shopify](https://github.com/JingxuanC/vela-shopify) | Vela AI for Shopify |
| [vela-infra](https://github.com/JingxuanC/vela-infra) | Docker Compose configs |
| [vela-docs](https://github.com/JingxuanC/vela-docs) | Design documents |

## License

MIT
