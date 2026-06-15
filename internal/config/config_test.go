package config

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadDefaults(t *testing.T) {
	// Unset all env vars that might affect the test
	for _, k := range []string{
		"ALIYUN_DASHSCOPE_API_KEY", "R2_ACCOUNT_ID", "DATABASE_URL",
		"REDIS_URL", "GO_ENV", "API_TOKEN", "SHOPIFY_API_SECRET",
		"SHIPENGINE_API_KEY", "RESEND_API_KEY",
	} {
		os.Unsetenv(k)
	}

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "development", cfg.GoEnv)
	assert.Equal(t, "debug", cfg.LogLevel)
	assert.Equal(t, "redis://localhost:6380", cfg.RedisURL)
	assert.Equal(t, "0.0.0.0", cfg.APIHost)
	assert.Equal(t, 8000, cfg.APIPort)
	assert.Equal(t, 5, cfg.MaxImageSizeMB)
	assert.Equal(t, 120, cfg.TaskTimeoutSec)
	assert.Equal(t, 24, cfg.CacheTTLHours)
	assert.Equal(t, 3, cfg.MaxRetries)
	assert.Equal(t, 500, cfg.MaxConcurrentSSE)
	assert.Equal(t, "https://dashscope.aliyuncs.com", cfg.AliyunDashscopeEndpoint)
	assert.Equal(t, "vela-images", cfg.R2BucketName)
	assert.Equal(t, "https://images.vela.dev", cfg.R2PublicURL)
}

func TestLoadProductionRequiresDatabaseURL(t *testing.T) {
	os.Setenv("GO_ENV", "production")
	os.Setenv("DATABASE_URL", "")
	os.Setenv("API_TOKEN", "test-token")
	defer func() {
		os.Unsetenv("GO_ENV")
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("API_TOKEN")
	}()

	_, err := Load()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DATABASE_URL")
}

func TestLoadProductionRequiresAPIToken(t *testing.T) {
	os.Setenv("GO_ENV", "production")
	os.Setenv("DATABASE_URL", "postgresql://localhost:5432/vtron")
	os.Setenv("REDIS_URL", "redis://localhost:6379")
	os.Setenv("API_TOKEN", "")
	defer func() {
		os.Unsetenv("GO_ENV")
		os.Unsetenv("DATABASE_URL")
		os.Unsetenv("REDIS_URL")
		os.Unsetenv("API_TOKEN")
	}()

	_, err := Load()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "API_TOKEN")
}

func TestLoadFromEnv(t *testing.T) {
	os.Setenv("ALIYUN_DASHSCOPE_API_KEY", "sk-test-key")
	os.Setenv("R2_ACCOUNT_ID", "test-account")
	os.Setenv("DATABASE_URL", "postgresql://user:pass@localhost/vtron")
	os.Setenv("REDIS_URL", "redis://custom:6379/5")
	os.Setenv("GO_ENV", "staging")
	os.Setenv("API_PORT", "9000")
	defer func() {
		for _, k := range []string{
			"ALIYUN_DASHSCOPE_API_KEY", "R2_ACCOUNT_ID", "DATABASE_URL",
			"REDIS_URL", "GO_ENV", "API_PORT",
		} {
			os.Unsetenv(k)
		}
	}()

	cfg, err := Load()
	require.NoError(t, err)

	assert.Equal(t, "sk-test-key", cfg.AliyunDashscopeAPIKey)
	assert.Equal(t, "test-account", cfg.R2AccountID)
	assert.Equal(t, 9000, cfg.APIPort)
}

func TestIsDevelopment(t *testing.T) {
	cfg := &Config{GoEnv: "development"}
	assert.True(t, cfg.IsDevelopment())
	assert.False(t, cfg.IsProduction())
}

func TestIsProduction(t *testing.T) {
	cfg := &Config{GoEnv: "production"}
	assert.True(t, cfg.IsProduction())
	assert.False(t, cfg.IsDevelopment())
}

func TestTaskTimeout(t *testing.T) {
	cfg := &Config{TaskTimeoutSec: 60}
	assert.Equal(t, 60.0, cfg.TaskTimeout().Seconds())
}

func TestCacheTTL(t *testing.T) {
	cfg := &Config{CacheTTLHours: 48}
	assert.Equal(t, 48.0, cfg.CacheTTL().Hours())
}

func TestCORSAllowedOriginsDevelopment(t *testing.T) {
	cfg := &Config{GoEnv: "development"}
	origins := cfg.CORSAllowedOrigins()
	assert.Contains(t, origins, "http://localhost:5173")
	assert.Contains(t, origins, "https://admin.shopify.com")
}

func TestCORSAllowedOriginsProduction(t *testing.T) {
	cfg := &Config{GoEnv: "production"}
	origins := cfg.CORSAllowedOrigins()
	// production without explicit CORSOrigins returns nil (no origins allowed)
	assert.Nil(t, origins)
}

func TestCORSAllowedOriginsCustom(t *testing.T) {
	cfg := &Config{GoEnv: "production", CORSOrigins: "https://example.com, https://app.example.com"}
	origins := cfg.CORSAllowedOrigins()
	assert.Equal(t, []string{"https://example.com", "https://app.example.com"}, origins)
}

// func TestRedisAddr(t *testing.T) {
// 	tests := []struct {
// 		name     string
// 		url      string
// 		expected string
// 	}{
// 		{"with db", "redis://localhost:6379/0", "localhost:6379"},
// 		{"with auth", "redis://user:pass@localhost:6379/0", "localhost:6379"},
// 		{"simple", "redis://localhost:6379", "localhost:6379"},
// 	}
// 	for _, tt := range tests {
// 		t.Run(tt.name, func(t *testing.T) {
// 			cfg := &Config{RedisURL: tt.url}
// 			assert.Equal(t, tt.expected, cfg.RedisAddr())
// 		})
// 	}
// }
