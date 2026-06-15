package handler

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/internal/service/billing"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// BillingHandler handles Shopify Billing API integration.
type BillingHandler struct {
	tokenTracker *service.TokenTracker

	db      *gorm.DB
	cache   *service.CacheService
	shopify *billing.ShopifyClient
}

// NewBillingHandler creates a new BillingHandler.
func NewBillingHandler(db *gorm.DB, cache *service.CacheService, shopify *billing.ShopifyClient, tokenTracker *service.TokenTracker) *BillingHandler {
	return &BillingHandler{db: db, cache: cache, shopify: shopify, tokenTracker: tokenTracker}
}

// PlanConfig defines a billing plan.
type PlanConfig struct {
	Name      string  `json:"name"`
	Price     float64 `json:"price"`
	Interval  string  `json:"interval"` // monthly, annual
	TrialDays int     `json:"trial_days"`
}

// Available plans
var billingPlans = map[string]PlanConfig{
	"starter": {Name: "Starter", Price: 29, Interval: "monthly", TrialDays: 14},
	"growth":  {Name: "Growth", Price: 79, Interval: "monthly", TrialDays: 14},
	"pro":     {Name: "Pro", Price: 149, Interval: "monthly", TrialDays: 14},
}

// SubscribeRequest is the request body for creating a subscription.
type SubscribeRequest struct {
	ShopID string `json:"shop_id"`
	Plan   string `json:"plan"` // starter, growth, pro
}

// SubscribeResponse is the response for a subscription creation.
type SubscribeResponse struct {
	Success         bool   `json:"success"`
	ConfirmationURL string `json:"confirmation_url,omitempty"`
	SubscriptionID  string `json:"subscription_id,omitempty"`
	Plan            string `json:"plan,omitempty"`
	TrialEndsAt     string `json:"trial_ends_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

// CurrentSubscriptionResponse shows active subscription info.
type CurrentSubscriptionResponse struct {
	Success        bool    `json:"success"`
	Plan           string  `json:"plan"`
	SubscriptionID string  `json:"subscription_id"`
	BillingStatus  string  `json:"billing_status"`
	Price          float64 `json:"price"`
	TrialEndsAt    string  `json:"trial_ends_at,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// --- Handlers ---

// resolveShop looks up a shop by UUID or domain string.
// Returns the shop and a parsed UUID (may be zero-value if looked up by domain).
func (h *BillingHandler) resolveShop(shopIDStr string) (*model.Shop, error) {
	// Try UUID first
	shopID, err := uuid.Parse(shopIDStr)
	if err == nil {
		var shop model.Shop
		if err := h.db.First(&shop, "id = ?", shopID).Error; err != nil {
			return nil, fmt.Errorf("shop not found by id: %w", err)
		}
		return &shop, nil
	}
	// Fallback: look up by domain (e.g. "vtron-dev-store.myshopify.com")
	var shop model.Shop
	if err := h.db.First(&shop, "shop_domain = ?", shopIDStr).Error; err != nil {
		return nil, fmt.Errorf("shop not found by domain: %w", err)
	}
	return &shop, nil
}

// Subscribe handles POST /api/billing/subscribe
func (h *BillingHandler) Subscribe(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req SubscribeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	plan, ok := billingPlans[req.Plan]
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("Invalid plan %q. Available: starter, growth, pro", req.Plan))
		return
	}

	shop, err := h.resolveShop(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Shop not found: "+err.Error())
		return
	}

	// Calculate trial end date
	trialEndsAt := time.Now().UTC().AddDate(0, 0, plan.TrialDays)

	// Call Shopify GraphQL appSubscriptionCreate
	result, err := h.shopify.CreateSubscription(
		shop.Domain,
		"",
		plan.Name,
		fmt.Sprintf("%.2f", plan.Price),
		shop.ID.String(),
		plan.TrialDays,
	)
	if err != nil {
		slog.Error("billing: shopify create subscription failed", "shop_id", shop.ID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create subscription with Shopify: "+err.Error())
		return
	}

	subscriptionID := result.SubscriptionID

	// Update shop with plan info
	updates := map[string]interface{}{
		"plan":            req.Plan,
		"subscription_id": subscriptionID,
		"trial_ends_at":   &trialEndsAt,
		"billing_status":  "pending", // pending until the user confirms via confirmationUrl
	}
	if err := h.db.WithContext(r.Context()).Model(&shop).Updates(updates).Error; err != nil {
		slog.Error("billing: failed to update shop subscription", "shop_id", shop.ID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to create subscription")
		return
	}

	slog.Info("billing: subscription created",
		"shop_id", shop.ID,
		"plan", req.Plan,
		"shopify_subscription_id", subscriptionID,
		"trial_ends_at", trialEndsAt,
	)

	httputil.WriteCreated(w, SubscribeResponse{
		Success:         true,
		ConfirmationURL: result.ConfirmationURL,
		SubscriptionID:  subscriptionID,
		Plan:            req.Plan,
		TrialEndsAt:     trialEndsAt.Format(time.RFC3339),
	})
}

// Confirm handles billing confirmation callbacks from Shopify.
// Shopify redirects the user to the return URL after they accept/decline.
// In production, Shopify sends GET with query params; internally we also accept POST.
func (h *BillingHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req struct {
		ShopID         string `json:"shop_id"`
		SubscriptionID string `json:"subscription_id"`
		Status         string `json:"status"` // accepted, declined
	}

	// Support both GET (Shopify redirect callback) and POST (internal)
	if r.Method == http.MethodGet {
		req.ShopID = r.URL.Query().Get("shop")
		req.SubscriptionID = r.URL.Query().Get("subscription_id")
		req.Status = r.URL.Query().Get("status")
		if req.Status == "" {
			req.Status = "accepted" // Shopify redirect implies acceptance
		}
	} else {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
			return
		}
	}

	shop, err := h.resolveShop(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Shop not found: "+err.Error())
		return
	}

	if req.Status == "accepted" {
		// Activate subscription — clear trial or transition to paid
		now := time.Now().UTC()
		trialEndsAt := now.AddDate(0, 1, 0) // 1 month trial from confirmation
		if err := h.db.WithContext(r.Context()).Model(&shop).Updates(map[string]interface{}{
			"billing_status": "active",
			"trial_ends_at":  &trialEndsAt,
		}).Error; err != nil {
			slog.Error("billing: failed to confirm subscription", "shop_id", shop.ID, "error", err)
			httputil.WriteError(w, http.StatusInternalServerError, "Failed to confirm subscription")
			return
		}
		slog.Info("billing: subscription confirmed", "shop_id", shop.ID, "plan", shop.Plan)
	} else {
		// Declined — keep free plan
		if h.shopify != nil {
			if err := h.shopify.CancelSubscription(shop.Domain, "", shop.SubscriptionID); err != nil {
				slog.Warn("billing: failed to cancel subscription on Shopify", "shop_id", shop.ID, "error", err)
			}
		}
		if err := h.db.WithContext(r.Context()).Model(&shop).Updates(map[string]interface{}{
			"plan":            "free",
			"billing_status":  "cancelled",
			"subscription_id": "",
		}).Error; err != nil {
			slog.Error("billing: failed to decline subscription", "shop_id", shop.ID, "error", err)
		}
		slog.Info("billing: subscription declined", "shop_id", shop.ID)
	}

	// Browser redirect (from Shopify billing approval): send back to app plans page
	if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
		appURL := fmt.Sprintf("https://admin.shopify.com/store/%s/apps/vela/app/plans?confirmed=true", shop.Domain)
		http.Redirect(w, r, appURL, http.StatusFound)
		return
	}

	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"status":  req.Status,
	})
}

// Current handles GET /api/billing/current
func (h *BillingHandler) Current(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	shopIDStr := r.URL.Query().Get("shop_id")
	if shopIDStr == "" {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id is required")
		return
	}

	shop, err := h.resolveShop(shopIDStr)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Shop not found: "+err.Error())
		return
	}

	price := 0.0
	if plan, ok := billingPlans[shop.Plan]; ok {
		price = plan.Price
	}

	resp := CurrentSubscriptionResponse{
		Success:        true,
		Plan:           shop.Plan,
		SubscriptionID: shop.SubscriptionID,
		BillingStatus:  shop.BillingStatus,
		Price:          price,
	}
	if shop.TrialEndsAt != nil {
		resp.TrialEndsAt = shop.TrialEndsAt.Format(time.RFC3339)
	}

	httputil.WriteOK(w, resp)
}

// Cancel handles POST /api/billing/cancel
func (h *BillingHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req struct {
		ShopID string `json:"shop_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	shop, err := h.resolveShop(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Shop not found: "+err.Error())
		return
	}
	shopID := shop.ID

	// Cancel on Shopify first
	if shop.SubscriptionID != "" && h.shopify != nil {
		if err := h.shopify.CancelSubscription(shop.Domain, "", shop.SubscriptionID); err != nil {
			slog.Warn("billing: failed to cancel subscription on Shopify", "shop_id", shop.ID, "error", err)
		}
	}

	if err := h.db.WithContext(r.Context()).Model(&model.Shop{}).Where("id = ?", shopID).Updates(map[string]interface{}{
		"plan":            "free",
		"billing_status":  "cancelled",
		"subscription_id": "",
	}).Error; err != nil {
		slog.Error("billing: failed to cancel subscription", "shop_id", shop.ID, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to cancel subscription")
		return
	}

	slog.Info("billing: subscription cancelled", "shop_id", shop.ID)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"message": "Subscription cancelled successfully",
	})
}

// ChangePlan handles POST /api/billing/change-plan
func (h *BillingHandler) ChangePlan(w http.ResponseWriter, r *http.Request) {
	slog.Warn("billing: ChangePlan only updates local DB, Shopify subscription unchanged - use Subscribe for new plans")
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	var req struct {
		ShopID  string `json:"shop_id"`
		NewPlan string `json:"new_plan"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if _, ok := billingPlans[req.NewPlan]; !ok {
		httputil.WriteError(w, http.StatusBadRequest, fmt.Sprintf("Invalid plan %q. Available: starter, growth, pro", req.NewPlan))
		return
	}

	shop, err := h.resolveShop(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Shop not found: "+err.Error())
		return
	}
	shopID := shop.ID

	if err := h.db.WithContext(r.Context()).Model(&model.Shop{}).Where("id = ?", shopID).Update("plan", req.NewPlan).Error; err != nil {
		slog.Error("billing: failed to change plan", "shop_id", shop.ID, "new_plan", req.NewPlan, "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to change plan")
		return
	}

	slog.Info("billing: plan changed", "shop_id", shop.ID, "new_plan", req.NewPlan)
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"plan":    req.NewPlan,
		"message": fmt.Sprintf("Plan changed to %s successfully", req.NewPlan),
	})
}

// RecordUsage handles POST /api/billing/record-usage — records token consumption
func (h *BillingHandler) RecordUsage(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}
	var req struct {
		ShopID    string `json:"shop_id"`
		Operation string `json:"operation"`
		Count     int    `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ShopID == "" || req.Operation == "" || req.Count <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, "shop_id, operation, and count are required")
		return
	}
	shop, err := h.resolveShop(req.ShopID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "shop not found")
		return
	}
	// Record to database
	usage := model.TokenUsage{
		ShopID: shop.ID, Feature: req.Operation, Operation: req.Operation,
		Count: req.Count, Status: "success", RecordedAt: time.Now().UTC(),
	}
	if createErr := h.db.WithContext(r.Context()).Create(&usage).Error; createErr != nil {
		slog.Error("billing: failed to record usage", "error", createErr)
		httputil.WriteError(w, http.StatusInternalServerError, "failed to record usage")
		return
	}
	// Update Redis counter
	if h.cache != nil {
		ym := time.Now().UTC().Format("200601")
		h.cache.Client().IncrBy(r.Context(), fmt.Sprintf("token_usage:%s:%s:%s", shop.ID, req.Operation, ym), int64(req.Count))
		h.cache.Client().IncrBy(r.Context(), fmt.Sprintf("token_usage:%s:total:%s", shop.ID, ym), int64(req.Count))
	}
	httputil.WriteOK(w, map[string]interface{}{"success": true, "recorded": req.Count, "shop_id": shop.ID})
}
