# LLM Router — AI 多供应商路由架构与数据流

> 文件: internal/service/llm_router.go
> 依赖: PostgreSQL + Redis (配置缓存)

## 一、架构定位

LLMRouter 是 Vela Engine 中所有 AI 功能的入口。管理多个 LLM 供应商的客户端池，提供统一的 `GetProvider(shopID)` 接口。

```
Handler (Chat/Content/SEO/Review/Insights/LTV Explain)
  │
  ▼
llmRouter.GetProvider(ctx, shopID)
  │
  ├─ 1. Redis 缓存 (5min TTL)
  │      key: llm:config:{shopID}
  │
  ├─ 2. PostgreSQL (cache miss)
  │      SELECT * FROM shop_llm_configs WHERE shop_id=? AND is_active=true
  │
  └─ 3. 环境变量默认值 (fallback)
         LLM_DEFAULT_PROVIDER + LLM_DEFAULT_MODEL
  │
  ▼
Provider Pool
  ├─ DashScopeProvider (Qwen-Turbo/Plus/Max)
  ├─ DeepSeekProvider  (Chat/Reasoner)
  └─ OpenAICompatible   (GPT-4o/GPT-4o-mini)
  │
  ▼
LLM API 调用
```

## 二、供应商解析 (三级回退)

```
GetProvider(ctx, shopID)
  │
  ├─ Level 1: Redis
  │   cache.Get("llm:config:{shopID}")
  │   命中 → 返回 ShopLLMConfig
  │   未命中 → Level 2
  │
  ├─ Level 2: PostgreSQL
  │   db.Where("shop_id=? AND is_active=true").First(&cfg)
  │   找到 → cache.Set(5min TTL) → 返回
  │   未找到 → Level 3
  │
  └─ Level 3: 全局默认
       Provider: LLM_DEFAULT_PROVIDER (deepseek)
       Model:    LLM_DEFAULT_MODEL (deepseek-chat)
```

## 三、Provider 池化

```go
type LLMRouter struct {
    mu           sync.RWMutex
    db           *gorm.DB
    cache        *CacheService
    providers    map[string]LLMProvider   // "provider:apiKeyHash"
    platformKeys map[string]string        // env→provider mapping
}

getOrCreateProvider(cfg):
  apiKey = cfg.APIKey 或 platformKeys[cfg.Provider]
  poolKey = fmt.Sprintf("%s:%x", provider, sha256(apiKey)[:8])
  
  读锁: 检查 providers[poolKey] 是否存在 → 命中返回
  写锁: 创建新 provider → 存入池 → 返回
```

池化策略: 相同 (provider, apiKey) 的组合复用同一客户端。

## 四、API Key 管理

```
优先级:
  1. shop_llm_configs.api_key (商户自有 Key)
  2. 环境变量 (平台 Key)
     DASHSCOPE_API_KEY → dashscope
     DEEPSEEK_API_KEY  → deepseek
     OPENAI_API_KEY    → openai
  3. 无可用 Key → 返回 error
```

## 五、Plan 模型分级

| Plan | 可用模型 |
|------|---------|
| free | qwen-turbo, deepseek-chat |
| starter | qwen-plus, deepseek-chat, deepseek-reasoner |
| growth | qwen-plus, qwen-max, deepseek-chat, deepseek-reasoner |
| pro | qwen-max, deepseek-chat, deepseek-reasoner, gpt-4o-mini |
| enterprise | 全部模型 (*) |

## 六、LLM Provider 接口

```go
type LLMProvider interface {
    ChatCompletion(ctx, *ChatCompletionRequest) (*ChatCompletionResponse, error)
    ChatCompletionStream(ctx, *ChatCompletionRequest) (<-chan StreamChunk, error)
    ProviderName() string
    AvailableModels() []string
}

type ChatCompletionRequest struct {
    Model       string
    Messages    []ChatMessage
    Temperature float64
    MaxTokens   int
    Stream      bool
}
```

## 七、使用场景

| 功能 | 调用方 | 模型偏好 |
|------|--------|---------|
| AI 客服 (Sales Agent) | SalesAgent | 任何 (流式) |
| 商品描述生成 | ContentHandler | 任何 |
| 评价总结 | ReviewHandler | 任何 |
| LTV 解释 | CustomerInsights | 任何 (轻量) |
| SEO 优化 | SEOHandler | 任何 |
| 营销文案 | MarketingHandler | 任何 |
| LLM 健康检查 | LLMHealthMonitor | 任何 (轻量) |
