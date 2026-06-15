package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/notify"
)

// LLMHealthMonitor checks LLM provider health on startup and periodically monitors balances.
type LLMHealthMonitor struct {
	router   *LLMRouter
	db       *gorm.DB
	notifier *notify.Center
}

// NewLLMHealthMonitor creates a new health monitor.
func NewLLMHealthMonitor(router *LLMRouter, db *gorm.DB, notifier *notify.Center) *LLMHealthMonitor {
	return &LLMHealthMonitor{router: router, db: db, notifier: notifier}
}

// StartupCheck runs health checks for all configured providers on server startup.
// Returns error if NO provider is healthy (but server still starts — error is logged as warning).
func (m *LLMHealthMonitor) StartupCheck(ctx context.Context) error {
	slog.Info("llm_health: running startup checks...")

	// Create temporary providers from platform keys (pool is lazily populated)
	providers := m.TempProviders()
	if len(providers) == 0 {
		return fmt.Errorf("llm_health: no LLM API keys configured — set DEEPSEEK_API_KEY, DASHSCOPE_API_KEY, or OPENAI_API_KEY")
	}

	var healthy int
	for _, p := range providers {
		hc, ok := p.(HealthChecker)
		if !ok {
			slog.Warn("llm_health: provider does not support health checks", "provider", p.ProviderName())
			continue
		}

		if err := hc.HealthCheck(ctx); err != nil {
			slog.Error("llm_health: provider UNHEALTHY", "provider", p.ProviderName(), "error", err)
			continue
		}
		healthy++
		slog.Info("llm_health: provider healthy", "provider", p.ProviderName())

		// Also check balance
		if info, err := hc.CheckBalance(ctx); err == nil && info != nil {
			slog.Info("llm_health: balance",
				"provider", info.Provider,
				"total_balance", fmt.Sprintf("%.2f %s", info.TotalBalance, info.Currency),
			)
			if info.IsLow {
				m.alertLowBalance(ctx, info)
			}
		}
	}

	if healthy == 0 {
		return fmt.Errorf("llm_health: all %d providers unhealthy — check API keys and network", len(providers))
	}

	slog.Info("llm_health: startup checks passed", "healthy", healthy, "total", len(providers))
	return nil
}

// TempProviders creates one-off providers from platform keys for health/balance checks.
func (m *LLMHealthMonitor) TempProviders() []LLMProvider {
	var out []LLMProvider
	for provider, key := range m.router.platformKeys {
		if key == "" {
			continue
		}
		switch provider {
		case "dashscope":
			out = append(out, NewDashScopeProvider(key, "", ""))
		case "deepseek":
			out = append(out, newDeepSeekProvider(key, ""))
		case "openai":
			out = append(out, NewDashScopeProvider(key, "https://api.openai.com/v1/chat/completions", "gpt-4o-mini"))
		}
	}
	return out
}

// StartPeriodicBalanceCheck runs balance checks every hour and alerts on low balance.
func (m *LLMHealthMonitor) StartPeriodicBalanceCheck(interval time.Duration) {
	go func() {
		// Wait 60s after startup before first periodic check
		time.Sleep(60 * time.Second)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for range ticker.C {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			m.checkAllBalances(ctx)
			cancel()
		}
	}()
	slog.Info("llm_health: periodic balance check started", "interval", interval)
}

func (m *LLMHealthMonitor) checkAllBalances(ctx context.Context) {
	for _, p := range m.TempProviders() {
		hc, ok := p.(HealthChecker)
		if !ok {
			continue
		}
		info, err := hc.CheckBalance(ctx)
		if err != nil {
			slog.Warn("llm_health: balance check failed", "provider", p.ProviderName(), "error", err)
			continue
		}
		if info == nil {
			continue
		}

		slog.Info("llm_health: balance", "provider", info.Provider, "balance", fmt.Sprintf("%.2f %s", info.TotalBalance, info.Currency))

		if info.IsLow {
			m.alertLowBalance(ctx, info)
		}
	}
}

func (m *LLMHealthMonitor) alertLowBalance(ctx context.Context, info *BalanceInfo) {
	slog.Warn("llm_health: LOW BALANCE", "provider", info.Provider, "balance", info.TotalBalance, "currency", info.Currency)

	if m.notifier == nil || m.db == nil {
		return
	}

	// Find all active shops and send alert
	var shops []model.Shop
	if err := m.db.WithContext(ctx).Where("uninstalled_at IS NULL").Limit(100).Find(&shops).Error; err != nil {
		slog.Error("llm_health: failed to find shops for alert", "error", err)
		return
	}

	for _, shop := range shops {
		body := fmt.Sprintf("⚠️ %s API balance is critically low (%.2f %s). AI features may stop working. Please top up or switch provider.",
			info.Provider, info.TotalBalance, info.Currency)
		if err := m.notifier.Send(ctx, &notify.Notification{
			ShopID:   shop.ID,
			Type:     "low_balance",
			Channel:  notify.ChannelInApp,
			Priority: notify.PriorityHigh,
			Title:    fmt.Sprintf("Low AI Balance: %s", info.Provider),
			Body:     body,
		}); err != nil {
			slog.Warn("llm_health: failed to send low balance alert", "shop_id", shop.ID, "error", err)
		}
	}
}
