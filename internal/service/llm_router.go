package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// LLMRouter manages per-shop LLM provider selection and client pooling.
// Providers are created lazily and reused across shops that share the same
// (provider, apiKey) pair. ShopLLMConfig is cached in Redis with a 5-min TTL
// to avoid DB queries on every request.
type LLMRouter struct {
	mu          sync.RWMutex
	db          *gorm.DB
	cache       *CacheService
	providers   map[string]LLMProvider // key: "provider:apiKeyHash"
	platformKeys map[string]string     // provider → platform API key (used when shop has no BYOK)
	metrics     LLMMetricsCollector
}

// NewLLMRouter creates a new LLMRouter.
// platformKeys maps provider names to their platform API keys.
// Keys are populated from: DASHSCOPE_API_KEY, DEEPSEEK_API_KEY, OPENAI_API_KEY env vars.
func NewLLMRouter(db *gorm.DB, cache *CacheService, platformKeys map[string]string) *LLMRouter {
	if platformKeys == nil {
		platformKeys = make(map[string]string)
	}
	// Fallback: DASHSCOPE_API_KEY → dashscope, etc.
	if _, ok := platformKeys["dashscope"]; !ok {
		if k := os.Getenv("DASHSCOPE_API_KEY"); k != "" {
			platformKeys["dashscope"] = k
		}
	}
	if _, ok := platformKeys["deepseek"]; !ok {
		if k := os.Getenv("DEEPSEEK_API_KEY"); k != "" {
			platformKeys["deepseek"] = k
		}
	}
	if _, ok := platformKeys["openai"]; !ok {
		if k := os.Getenv("OPENAI_API_KEY"); k != "" {
			platformKeys["openai"] = k
		}
	}

	return &LLMRouter{
		db:           db,
		cache:        cache,
		providers:    make(map[string]LLMProvider),
		platformKeys: platformKeys,
	}
}

// SetMetricsCollector sets an optional metrics collector for LLM observability.
func (r *LLMRouter) SetMetricsCollector(m LLMMetricsCollector) {
	r.metrics = m
}

// GetProvider returns the LLM provider and config for a shop.
// Resolution order:
//  1. ShopLLMConfig from Redis cache
//  2. ShopLLMConfig from DB (then cache it)
//  3. Global defaults (env vars LLM_DEFAULT_PROVIDER / LLM_DEFAULT_MODEL, or qwen-plus)
func (r *LLMRouter) GetProvider(ctx context.Context, shopID uuid.UUID) (LLMProvider, *model.ShopLLMConfig, error) {
	cfg, err := r.getConfig(ctx, shopID)
	if err != nil {
		return nil, nil, fmt.Errorf("llm_router: get config for shop %s: %w", shopID, err)
	}

	provider, err := r.getOrCreateProvider(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("llm_router: create provider: %w", err)
	}

	return provider, cfg, nil
}

// getConfig resolves the LLM config for a shop with caching.
func (r *LLMRouter) getConfig(ctx context.Context, shopID uuid.UUID) (*model.ShopLLMConfig, error) {
	// 1. Try Redis cache
	if r.cache != nil {
		cacheKey := fmt.Sprintf("llm:config:%s", shopID.String())
		if raw, err := r.cache.Get(ctx, cacheKey); err == nil && raw != "" {
			var cfg model.ShopLLMConfig
			if err := json.Unmarshal([]byte(raw), &cfg); err == nil {
				return &cfg, nil
			}
		}
	}

	// 2. Try DB
	var cfg model.ShopLLMConfig
	if r.db != nil {
		err := r.db.WithContext(ctx).
			Where("shop_id = ? AND is_active = ?", shopID, true).
			First(&cfg).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			slog.Warn("llm_router: db lookup failed", "shop_id", shopID, "error", err)
		}
		if err == nil {
			// Cache it for 5 minutes
			if r.cache != nil {
				cacheKey := fmt.Sprintf("llm:config:%s", shopID.String())
				if data, err := json.Marshal(cfg); err == nil {
					_ = r.cache.Set(ctx, cacheKey, string(data), 5*time.Minute)
				}
			}
			return &cfg, nil
		}
	}

	// 3. Global defaults
	return r.defaultConfig(), nil
}

// defaultConfig returns the platform-wide default LLM config.
func (r *LLMRouter) defaultConfig() *model.ShopLLMConfig {
	p := os.Getenv("LLM_DEFAULT_PROVIDER")
	if p == "" {
		p = "deepseek"
	}
	m := os.Getenv("LLM_DEFAULT_MODEL")
	if m == "" {
		m = "deepseek-chat"
	}
	return &model.ShopLLMConfig{
		Provider:    p,
		Model:       m,
		APIKey:      "",
		Temperature: 0.7,
		MaxTokens:   2048,
		IsActive:    true,
	}
}

// getOrCreateProvider returns an existing provider from the pool or creates a new one.
func (r *LLMRouter) getOrCreateProvider(cfg *model.ShopLLMConfig) (LLMProvider, error) {
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = r.platformKeys[cfg.Provider]
	}
	if apiKey == "" {
		return nil, fmt.Errorf("llm_router: no API key for provider %q (set %s_API_KEY env or configure per-shop)", cfg.Provider, strings.ToUpper(cfg.Provider))
	}

	poolKey := r.poolKey(cfg.Provider, apiKey)

	// Fast path: read lock
	r.mu.RLock()
	if p, ok := r.providers[poolKey]; ok {
		r.mu.RUnlock()
		return p, nil
	}
	r.mu.RUnlock()

	// Slow path: create provider under write lock
	r.mu.Lock()
	defer r.mu.Unlock()

	// Double-check after acquiring write lock
	if p, ok := r.providers[poolKey]; ok {
		return p, nil
	}

	var provider LLMProvider
	switch cfg.Provider {
	case "dashscope":
		provider = NewDashScopeProvider(apiKey, "", cfg.Model)
	case "deepseek":
		provider = newDeepSeekProvider(apiKey, cfg.Model)
	default:
		// Unknown provider: treat as OpenAI-compatible, use DashScopeProvider with custom endpoint
		slog.Warn("llm_router: unknown provider, treating as openai-compatible", "provider", cfg.Provider)
		endpoint := os.Getenv("LLM_ENDPOINT")
		if endpoint == "" {
			endpoint = "https://api.openai.com/v1/chat/completions"
		}
		provider = NewDashScopeProvider(apiKey, endpoint, cfg.Model)
	}

	if r.metrics != nil {
		switch p := provider.(type) {
		case *DashScopeProvider:
			p.SetMetricsCollector(r.metrics)
		case *deepSeekProvider:
			p.DashScopeProvider.SetMetricsCollector(r.metrics)
		}
	}

	r.providers[poolKey] = provider
	return provider, nil
}

// poolKey returns a deterministic key for the provider pool.
func (r *LLMRouter) poolKey(provider, apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%s:%x", provider, hash[:8])
}

// AllProviders returns all unique providers currently in the pool.
func (r *LLMRouter) AllProviders() []LLMProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	seen := make(map[string]bool)
	var out []LLMProvider
	for _, p := range r.providers {
		name := p.ProviderName()
		if !seen[name] {
			seen[name] = true
			out = append(out, p)
		}
	}
	return out
}

// InvalidateCache clears the cached config for a shop (call after config update).
func (r *LLMRouter) InvalidateCache(ctx context.Context, shopID uuid.UUID) {
	if r.cache != nil {
		cacheKey := fmt.Sprintf("llm:config:%s", shopID.String())
		_ = r.cache.Delete(ctx, cacheKey)
	}
}

// ListModels returns available models for a given provider, filtered by plan.
func (r *LLMRouter) ListModels(provider, plan string) []ModelInfo {
	allowlist, ok := planModelAllowlist[plan]
	if !ok {
		slog.Warn("llm_router: unknown plan, falling back to free", "plan", plan)
		allowlist = planModelAllowlist["free"]
	}

	var allModels []ModelInfo
	switch provider {
	case "dashscope":
		allModels = []ModelInfo{
			{ID: "qwen-turbo", Name: "Qwen Turbo", Description: "Fast, cost-effective"},
			{ID: "qwen-plus", Name: "Qwen Plus", Description: "Balanced performance"},
			{ID: "qwen-max", Name: "Qwen Max", Description: "Highest quality"},
		}
	case "deepseek":
		allModels = []ModelInfo{
			{ID: "deepseek-chat", Name: "DeepSeek Chat", Description: "Fast general-purpose"},
			{ID: "deepseek-reasoner", Name: "DeepSeek Reasoner", Description: "Deep reasoning (R1)"},
		}
	case "openai":
		allModels = []ModelInfo{
			{ID: "gpt-4o-mini", Name: "GPT-4o Mini", Description: "Fast, cost-effective"},
			{ID: "gpt-4o", Name: "GPT-4o", Description: "Top-tier quality"},
		}
	default:
		allModels = []ModelInfo{}
	}

	// Filter by plan allowlist
	if allowlist[0] == "*" {
		return allModels
	}
	allowedSet := make(map[string]bool, len(allowlist))
	for _, m := range allowlist {
		allowedSet[m] = true
	}

	var filtered []ModelInfo
	for _, m := range allModels {
		locked := !allowedSet[m.ID]
		m.Locked = locked
		filtered = append(filtered, m)
	}
	return filtered
}

// ModelInfo describes an available LLM model.
type ModelInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Locked      bool   `json:"locked"`
}

// PlanModelAllowlist returns the model allowlist for a plan, and whether the plan exists.
func PlanModelAllowlist(plan string) (*ModelAllowlist, bool) {
	models, ok := planModelAllowlist[plan]
	if !ok {
		return nil, false
	}
	return &ModelAllowlist{models: models}, true
}

// ModelAllowlist wraps a plan's allowed model list.
type ModelAllowlist struct {
	models []string
}

// Allows returns true if the given model is in the allowlist.
// A wildcard "*" entry means all models are allowed.
func (a *ModelAllowlist) Allows(model string) bool {
	if len(a.models) == 1 && a.models[0] == "*" {
		return true
	}
	for _, m := range a.models {
		if m == model {
			return true
		}
	}
	return false
}

// planModelAllowlist maps billing plans to allowed model IDs.
// "*" means all models are available.
var planModelAllowlist = map[string][]string{
	"free":       {"qwen-turbo", "deepseek-chat"},
	"starter":    {"qwen-plus", "deepseek-chat", "deepseek-reasoner"},
	"growth":     {"qwen-plus", "qwen-max", "deepseek-chat", "deepseek-reasoner"},
	"pro":        {"qwen-max", "deepseek-chat", "deepseek-reasoner", "gpt-4o-mini"},
	"enterprise": {"*"},
}
