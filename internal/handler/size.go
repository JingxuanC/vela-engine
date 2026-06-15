package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service/size"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
	"gorm.io/gorm"
)

// SizeHandler handles size recommendation endpoints.
type SizeHandler struct {
	engine   *size.SizeRecommendationEngine
	db       *gorm.DB
	injector *insight.ContextInjector
	eventBus eventbus.EventBus
}

// NewSizeHandler creates a new SizeHandler.
func NewSizeHandler(engine *size.SizeRecommendationEngine, db *gorm.DB, inj *insight.ContextInjector) *SizeHandler {
	return &SizeHandler{engine: engine, db: db, injector: inj}
}

// SetEventBus sets the optional event bus for AI event publishing.
func (h *SizeHandler) SetEventBus(bus eventbus.EventBus) { h.eventBus = bus }

// SizeRecommendRequest holds body measurements for a recommendation.
type SizeRecommendRequest struct {
	ShopID       string             `json:"shop_id"`
	ProductID    string             `json:"product_id"`
	Measurements map[string]float64 `json:"measurements"`
}

// SizeRecommendResponse holds the recommendation result.
type SizeRecommendResponse struct {
	Success         bool    `json:"success"`
	RecommendedSize string  `json:"recommended_size"`
	Confidence      float64 `json:"confidence"`
	AlternativeSize string  `json:"alternative_size"`
	FitNotes        string  `json:"fit_notes"`
	Error           string  `json:"error,omitempty"`
}

// Recommend handles POST /api/size/recommend.
func (h *SizeHandler) Recommend(w http.ResponseWriter, r *http.Request) {
	var req SizeRecommendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	shopID, _ := uuid.Parse(req.ShopID)
	result := h.engine.RecommendSize(req.ShopID, req.ProductID, req.Measurements)
	if result.Error != "" {
		PublishAIEvent(r.Context(), h.eventBus, shopID, "size.recommend", req, nil, false)
		httputil.WriteJSON(w, http.StatusOK, SizeRecommendResponse{
			Success: false,
			Error:   result.Error,
		})
		return
	}

	fitNotes := result.FitNotes

	// Phase 1.5: Inject size return data into fit notes
	if h.injector != nil && h.db != nil && req.ShopID != "" {
		if data := h.injector.InjectOne(r.Context(), h.db, req.ShopID, "return_insight"); data != "" {
			if fitNotes != "" {
				fitNotes += " | "
			}
			fitNotes += data
		}
	}

	PublishAIEvent(r.Context(), h.eventBus, shopID, "size.recommend", req, result, true)

	httputil.WriteOK(w, SizeRecommendResponse{
		Success:         true,
		RecommendedSize: result.RecommendedSize,
		Confidence:      result.Confidence,
		AlternativeSize: result.AlternativeSize,
		FitNotes:        fitNotes,
	})
}

// LearnFromReturnsRequest holds return data for learning.
type LearnFromReturnsRequest struct {
	ShopID     string              `json:"shop_id"`
	ReturnData []size.ReturnRecord `json:"return_data"`
}

// LearnFromReturnsResponse holds the learning result.
type LearnFromReturnsResponse struct {
	Success     bool                   `json:"success"`
	Adjustments map[string]interface{} `json:"adjustments"`
	Samples     int                    `json:"samples"`
	Message     string                 `json:"message"`
}

// LearnFromReturns handles POST /api/size/learn-from-returns.
func (h *SizeHandler) LearnFromReturns(w http.ResponseWriter, r *http.Request) {
	var req LearnFromReturnsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request")
		return
	}

	shopID, _ := uuid.Parse(req.ShopID)
	result := h.engine.LearnFromReturns(req.ShopID, req.ReturnData)

	adjUntyped, adjOK := result["adjustments"].(map[string]interface{})
	if !adjOK {
		httputil.WriteError(w, http.StatusInternalServerError, "Invalid adjustments data type")
		return
	}

	samplesFloat, samplesOK := result["samples"].(int)
	if !samplesOK {
		// Try float64 (json.Unmarshal uses float64 for numbers)
		if f, ok := result["samples"].(float64); ok {
			samplesFloat = int(f)
		} else {
			samplesFloat = 0
		}
	}

	var msg string
	if len(adjUntyped) > 0 {
		msg = fmt.Sprintf("Analysed %d return records. Adjusted %d size thresholds.", samplesFloat, len(adjUntyped))
	} else {
		msg = fmt.Sprintf("Analysed %d return records. No significant return patterns found; no adjustments needed.", samplesFloat)
	}

	PublishAIEvent(r.Context(), h.eventBus, shopID, "size.learn", req, result, true)

	httputil.WriteOK(w, LearnFromReturnsResponse{
		Success:     true,
		Adjustments: adjUntyped,
		Samples:     samplesFloat,
		Message:     msg,
	})
}
