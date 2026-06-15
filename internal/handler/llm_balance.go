package handler

import (
	"net/http"

	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// LLMBalanceHandler exposes LLM balance/health status.
type LLMBalanceHandler struct {
	health *service.LLMHealthMonitor
}

// NewLLMBalanceHandler creates a new handler.
func NewLLMBalanceHandler(health *service.LLMHealthMonitor) *LLMBalanceHandler {
	return &LLMBalanceHandler{health: health}
}

// GetBalance handles GET /api/llm/balance
func (h *LLMBalanceHandler) GetBalance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var results []*service.BalanceInfo
	for _, p := range h.health.TempProviders() {
		hc, ok := p.(service.HealthChecker)
		if !ok {
			continue
		}
		info, err := hc.CheckBalance(ctx)
		if err != nil {
			results = append(results, &service.BalanceInfo{
				Provider: p.ProviderName(),
				Currency: "unknown",
			})
			continue
		}
		if info != nil {
			results = append(results, info)
		}
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success":  true,
		"balances": results,
	})
}
