// Package config provides YAML-file + env-var configuration for the Vela AI API server.
// Priority: env vars > YAML config file > struct defaults.
package config

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
	"gopkg.in/yaml.v3"
)

// Config holds all application configuration.
type Config struct {
	// Alibaba Cloud DashScope (OutfitAnyone + Qwen LLM)
	AliyunDashscopeAPIKey   string `yaml:"dashscope_api_key" env:"ALIYUN_DASHSCOPE_API_KEY"`
	AliyunDashscopeEndpoint string `yaml:"dashscope_endpoint" env:"ALIYUN_DASHSCOPE_ENDPOINT"`

	// Cloudflare R2
	R2AccountID       string `yaml:"r2_account_id" env:"R2_ACCOUNT_ID"`
	R2AccessKeyID     string `yaml:"r2_access_key_id" env:"R2_ACCESS_KEY_ID"`
	R2SecretAccessKey string `yaml:"r2_secret_access_key" env:"R2_SECRET_ACCESS_KEY"`
	R2BucketName      string `yaml:"r2_bucket_name" env:"R2_BUCKET_NAME"`
	R2PublicURL       string `yaml:"r2_public_url" env:"R2_PUBLIC_URL"`

	// PostgreSQL
	DatabaseURL string `yaml:"database_url" env:"DATABASE_URL"`

	// Redis
	RedisURL string `yaml:"redis_url" env:"REDIS_URL"`

	// Asynq (task queue, same Redis instance, different DB)
	AsynqRedisDB int `yaml:"asynq_redis_db" env:"ASYNQ_REDIS_DB"`

	// Environment
	GoEnv    string `yaml:"go_env" env:"GO_ENV"`
	LogLevel string `yaml:"log_level" env:"LOG_LEVEL"`

	// API token for service-to-service auth
	APIToken string `yaml:"api_token" env:"API_TOKEN"`

	// Shopify (webhook verification)
	ShopifyAPISecret string `yaml:"shopify_api_secret" env:"SHOPIFY_API_SECRET"`

	// Shopify Billing
	ShopifyAPIKey     string `yaml:"shopify_api_key" env:"SHOPIFY_API_KEY"`
	ShopifyAPIVersion string `yaml:"shopify_api_version" env:"SHOPIFY_API_VERSION"`
	BillingReturnURL  string `yaml:"billing_return_url" env:"BILLING_RETURN_URL"`

	// ShipEngine (return labels)
	ShipEngineAPIKey string `yaml:"shipengine_api_key" env:"SHIPENGINE_API_KEY"`

	// Resend (email notifications)
	ResendAPIKey    string `yaml:"resend_api_key" env:"RESEND_API_KEY"`
	ResendFromEmail string `yaml:"resend_from_email" env:"RESEND_FROM_EMAIL"`
	ResendToEmail   string `yaml:"resend_to_email" env:"RESEND_TO_EMAIL"`

	// Server
	APIHost string `yaml:"api_host" env:"API_HOST"`
	APIPort int    `yaml:"api_port" env:"API_PORT"`

	// Limits
	MaxImageSizeMB   int `yaml:"max_image_size_mb" env:"MAX_IMAGE_SIZE_MB"`
	TaskTimeoutSec   int `yaml:"task_timeout_seconds" env:"TASK_TIMEOUT_SECONDS"`
	MaxRetries       int `yaml:"max_retries" env:"MAX_RETRIES"`
	CacheTTLHours    int `yaml:"cache_ttl_hours" env:"CACHE_TTL_HOURS"`
	MaxConcurrentSSE int `yaml:"max_concurrent_sse" env:"MAX_CONCURRENT_SSE"`

	// CORS origins (comma-separated)
	CORSOrigins string `yaml:"cors_origins" env:"CORS_ORIGINS"`

	// Pinterest OAuth
	PinterestClientID     string `yaml:"pinterest_client_id" env:"PINTEREST_CLIENT_ID"`
	PinterestClientSecret string `yaml:"pinterest_client_secret" env:"PINTEREST_CLIENT_SECRET"`
	PinterestRedirectURI  string `yaml:"pinterest_redirect_uri" env:"PINTEREST_REDIRECT_URI"`

	// Crypto (AES-256-GCM key, base64-encoded 32 bytes)
	CryptoKey string `yaml:"crypto_key" env:"CRYPTO_KEY"`

	// YouTube OAuth
	YouTubeClientID     string `yaml:"youtube_client_id" env:"YOUTUBE_CLIENT_ID"`
	YouTubeClientSecret string `yaml:"youtube_client_secret" env:"YOUTUBE_CLIENT_SECRET"`
	YouTubeRedirectURI  string `yaml:"youtube_redirect_uri" env:"YOUTUBE_REDIRECT_URI"`
	YouTubeAPIKey       string `yaml:"youtube_api_key" env:"YOUTUBE_API_KEY"`

	// Meta / Instagram OAuth
	MetaAppID       string `yaml:"meta_app_id" env:"META_APP_ID"`
	MetaAppSecret   string `yaml:"meta_app_secret" env:"META_APP_SECRET"`
	MetaRedirectURI string `yaml:"meta_redirect_uri" env:"META_REDIRECT_URI"`

	EnterpriseAPIRateLimit int `yaml:"enterprise_api_rate_limit" env:"ENTERPRISE_API_RATE_LIMIT"`
	AttributionWindowDays  int `yaml:"attribution_window_days" env:"ATTRIBUTION_WINDOW_DAYS"`

	// Day 4: External Data APIs
	GoogleAPIKey     string `yaml:"google_api_key" env:"GOOGLE_API_KEY"`
	ECCompassAPIKey  string `yaml:"eccompass_api_key" env:"EC_COMPASS_API_KEY"`
	ExternalDataMock bool   `yaml:"external_data_mock" env:"EXTERNAL_DATA_MOCK"`

	// Observability (Prometheus + Loki URLs for merchant dashboard)
	PrometheusURL string `yaml:"prometheus_url" env:"PROMETHEUS_URL"`
	LokiURL       string `yaml:"loki_url" env:"LOKI_URL"`

	// RAG Extension
	QdrantAddr  string  `yaml:"qdrant_url" env:"QDRANT_URL"`
	OllamaURL   string  `yaml:"ollama_url" env:"OLLAMA_URL"`
	EmbedModel  string  `yaml:"rag_embed_model" env:"RAG_EMBED_MODEL"`
	EmbedDims   int     `yaml:"rag_embed_dims" env:"RAG_EMBED_DIMS"`
	RAGTopK     int     `yaml:"rag_default_top_k" env:"RAG_DEFAULT_TOP_K"`
	RAGMinScore float64 `yaml:"rag_min_score" env:"RAG_MIN_SCORE"`
	RAGCacheTTL int     `yaml:"rag_cache_ttl_min" env:"RAG_CACHE_TTL_MIN"`
}

// defaultConfig returns defaults suitable for local development.
func defaultConfig() *Config {
	return &Config{
		AliyunDashscopeEndpoint: "https://dashscope.aliyuncs.com",
		R2BucketName:            "vela-images",
		R2PublicURL:             "https://images.vela.dev",
		RedisURL:                "redis://localhost:6380",
		AsynqRedisDB:            1,
		GoEnv:                   "development",
		LogLevel:                "debug",
		ShopifyAPIVersion:       "2025-01",
		BillingReturnURL:        "http://localhost:8000/api/billing/confirm",
		APIHost:                 "0.0.0.0",
		APIPort:                 8000,
		MaxImageSizeMB:          5,
		TaskTimeoutSec:          120,
		MaxRetries:              3,
		CacheTTLHours:           24,
		MaxConcurrentSSE:        500,
		EnterpriseAPIRateLimit:  300,
		AttributionWindowDays:   30,
		ExternalDataMock:        true,
		// Observability defaults
		PrometheusURL:           "http://localhost:9090",
		LokiURL:                 "http://localhost:3100",
		QdrantAddr:              "http://localhost:6333",
		OllamaURL:               "http://localhost:11434",
		EmbedModel:              "nomic-embed-text",
		EmbedDims:               768,
		RAGTopK:                 10,
		RAGMinScore:             0.5,
		RAGCacheTTL:             30,
		ResendFromEmail:         "contact@vela.dev",
		ResendToEmail:           "team@vela.dev",
	}
}

// configPath is set by --config flag, defaults to CONFIG_PATH env or config.yaml.
var configPath string

func init() {
	flag.StringVar(&configPath, "config", "", "path to YAML config file")
}

// Load reads configuration: YAML file → env vars → validate.
// Priority: env vars override YAML values, which override defaults.
func Load() (*Config, error) {
	flag.Parse()

	cfg := defaultConfig()

	// 1. Load YAML config file
	yamlPath := configPath
	if yamlPath == "" {
		yamlPath = os.Getenv("CONFIG_PATH")
	}
	if yamlPath == "" {
		// Try environment-specific config first, then default
		goEnv := os.Getenv("GO_ENV")
		if goEnv == "" {
			goEnv = "development"
		}
		candidates := []string{
			filepath.Join("configs", "config."+goEnv+".yaml"),
			filepath.Join("configs", "config.yaml"),
			"config." + goEnv + ".yaml",
			"config.yaml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				yamlPath = c
				break
			}
		}
	}

	if yamlPath != "" {
		data, err := os.ReadFile(yamlPath)
		if err != nil {
			if configPath != "" { // only fail if explicitly specified
				return nil, fmt.Errorf("config: read %s: %w", yamlPath, err)
			}
			slog.Warn("config: could not read config file, using defaults", "path", yamlPath, "error", err)
		} else {
			if err := yaml.Unmarshal(data, cfg); err != nil {
				return nil, fmt.Errorf("config: parse %s: %w", yamlPath, err)
			}
			slog.Info("config: loaded from file", "path", yamlPath)
		}
	} else {
		slog.Info("config: no config file found, using defaults + env vars")
	}

	// 2. Override with environment variables (highest priority)
	if err := env.Parse(cfg); err != nil {
		return nil, fmt.Errorf("config: parse env: %w", err)
	}

	// 3. Validate production requirements
	if cfg.GoEnv == "production" {
		if cfg.DatabaseURL == "" {
			return nil, errors.New("DATABASE_URL is required in production environment")
		}
		if cfg.RedisURL == "" {
			return nil, errors.New("REDIS_URL is required in production environment")
		}
		if cfg.APIToken == "" {
			return nil, errors.New("API_TOKEN is required in production environment")
		}
		if cfg.CryptoKey == "" {
			slog.Warn("CRYPTO_KEY not set - social OAuth tokens will not be encrypted")
		}
	}

	return cfg, nil
}

// IsDevelopment returns true when running in a development environment.
func (c *Config) IsDevelopment() bool {
	return c.GoEnv == "development"
}

// IsProduction returns true when running in a production environment.
func (c *Config) IsProduction() bool {
	return c.GoEnv == "production"
}

// TaskTimeout returns the task timeout as a time.Duration.
func (c *Config) TaskTimeout() time.Duration {
	return time.Duration(c.TaskTimeoutSec) * time.Second
}

// CacheTTL returns the cache TTL as a time.Duration.
func (c *Config) CacheTTL() time.Duration {
	return time.Duration(c.CacheTTLHours) * time.Hour
}

// CORSAllowedOrigins returns the list of allowed CORS origins for the current environment.
func (c *Config) CORSAllowedOrigins() []string {
	if c.CORSOrigins != "" {
		return splitAndTrim(c.CORSOrigins, ",")
	}
	if c.IsDevelopment() {
		return []string{
			"http://localhost:8000",
			"http://localhost:9293",
			"http://localhost:5173",
			"http://localhost:3000",
			"https://admin.shopify.com",
			"https://*.myshopify.com",
		}
	}
	return nil
}

// ShopifyGraphQLURL returns the Shopify Admin GraphQL endpoint for a given shop domain.
func (c *Config) ShopifyGraphQLURL(shopDomain string) string {
	return fmt.Sprintf("https://%s/admin/api/%s/graphql.json", shopDomain, c.ShopifyAPIVersion)
}

// AsynqRedisAddr returns the host:port address for Asynq's Redis connection.
func (c *Config) AsynqRedisAddr() string {
	return extractAddr(c.RedisURL)
}

// extractAddr extracts host:port from a redis:// URL.
func extractAddr(redisURL string) string {
	s := redisURL
	if hasPrefix(s, "redis://") {
		s = s[8:]
	} else if hasPrefix(s, "rediss://") {
		s = s[9:]
	}
	if idx := indexOf(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	if idx := indexOf(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	return s
}

func splitAndTrim(s, sep string) []string {
	var result []string
	for _, part := range splitStr(s, sep) {
		t := trimSpace(part)
		if t != "" {
			result = append(result, t)
		}
	}
	return result
}

func splitStr(s, sep string) []string {
	if s == "" {
		return nil
	}
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if hasPrefix(s[i:], sep) {
			parts = append(parts, s[start:i])
			i += len(sep) - 1
			start = i + 1
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func indexOf(s, substr string) int {
	for i := 0; i < len(s); i++ {
		if hasPrefix(s[i:], substr) {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
