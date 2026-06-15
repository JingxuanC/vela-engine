// Package handler provides HTTP handlers for the Vela AI API.
// supply.go implements the Supply Insight endpoints.
package handler

import (
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/middleware"
	"github.com/JingxuanC/vela-engine/internal/platform/externaldata"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// SupplyHandler handles supply-chain insight API requests.
type SupplyHandler struct {
	db           *gorm.DB
	trendsClient *externaldata.GoogleTrendsClient
}

// NewSupplyHandler creates a new SupplyHandler.
func NewSupplyHandler(db *gorm.DB, trendsClient *externaldata.GoogleTrendsClient) *SupplyHandler {
	return &SupplyHandler{db: db, trendsClient: trendsClient}
}

// ── Stock Prediction ─────────────────────────────────────────────────────────

// StockPredictionResponse wraps the stock prediction list response.
type StockPredictionResponse struct {
	Success     bool                     `json:"success"`
	Predictions []service.StockPrediction `json:"predictions"`
	Count       int                      `json:"count"`
	Error       string                   `json:"error,omitempty"`
}

// StockPrediction handles GET /api/supply/stock-prediction
func (h *SupplyHandler) StockPrediction(w http.ResponseWriter, r *http.Request) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	predictions, err := service.ComputeStockPredictions(r.Context(), h.db, shopID)
	if err != nil {
		slog.Error("supply: stock prediction failed", "shop_id", shopIDStr, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("supply: stock prediction computed",
		"shop_id", shopIDStr,
		"products", len(predictions),
	)

	httputil.WriteOK(w, StockPredictionResponse{
		Success:     true,
		Predictions: predictions,
		Count:       len(predictions),
	})
}

// ── Return Anomalies ─────────────────────────────────────────────────────────

// ReturnAnomalyResponse wraps the return anomaly detection response.
type ReturnAnomalyResponse struct {
	Success   bool                   `json:"success"`
	Anomalies []service.ReturnAnomaly `json:"anomalies"`
	Count     int                    `json:"count"`
	Error     string                 `json:"error,omitempty"`
}

// ReturnAnomalies handles GET /api/supply/return-anomalies
func (h *SupplyHandler) ReturnAnomalies(w http.ResponseWriter, r *http.Request) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	anomalies, err := service.ComputeReturnAnomalies(r.Context(), h.db, shopID)
	if err != nil {
		slog.Error("supply: return anomaly detection failed", "shop_id", shopIDStr, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("supply: return anomalies computed",
		"shop_id", shopIDStr,
		"total", len(anomalies),
	)

	httputil.WriteOK(w, ReturnAnomalyResponse{
		Success:   true,
		Anomalies: anomalies,
		Count:     len(anomalies),
	})
}

// ── Trend Match ──────────────────────────────────────────────────────────────

// TrendMatchResponse wraps the trend match response.
type TrendMatchResponse struct {
	Success bool                      `json:"success"`
	Data    *service.TrendMatchResult `json:"data,omitempty"`
	Error   string                    `json:"error,omitempty"`
}

// TrendMatch handles GET /api/supply/trend-match
func (h *SupplyHandler) TrendMatch(w http.ResponseWriter, r *http.Request) {
	shopIDStr := middleware.ShopIDFromContext(r.Context())
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}
	shopID, err := uuid.Parse(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid shop_id")
		return
	}

	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	result, err := service.ComputeTrendMatch(r.Context(), h.db, h.trendsClient, shopID)
	if err != nil {
		slog.Error("supply: trend match failed", "shop_id", shopIDStr, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}

	slog.Info("supply: trend match computed",
		"shop_id", shopIDStr,
		"trending_keywords", len(result.TrendingKeywords),
		"missing_products", len(result.MissingProducts),
	)

	httputil.WriteOK(w, TrendMatchResponse{
		Success: true,
		Data:    result,
	})
}
