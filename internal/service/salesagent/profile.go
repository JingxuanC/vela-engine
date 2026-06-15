package salesagent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// ── Profile Loading ──────────────────────────────────────────────────────────

// loadCustomerProfile loads the persistent customer profile from PostgreSQL.
// Returns nil if no profile exists yet (first-time visitor).
func (a *SalesAgent) loadCustomerProfile(ctx context.Context, shopID, customerID string) (*model.CustomerChatProfile, error) {
	if a.db == nil || customerID == "" {
		return nil, nil
	}
	var profile model.CustomerChatProfile
	err := a.db.WithContext(ctx).
		Where("shop_id = ? AND customer_id = ?", shopID, customerID).
		First(&profile).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil // no profile yet, not an error
		}
		return nil, err
	}
	return &profile, nil
}

// mergeProfileIntoSession injects persistent profile data into session Preferences.
// Called at the start of each conversation so the Agent has long-term memory.
func mergeProfileIntoSession(sess *ChatSession, profile *model.CustomerChatProfile) {
	if profile == nil {
		return
	}
	if sess.Preferences == nil {
		sess.Preferences = make(map[string]string)
	}

	// Only set from profile if session doesn't already have the value
	// (session-level preferences from current conversation take priority)
	if _, exists := sess.Preferences["size"]; !exists && profile.SizePreference != "" {
		sess.Preferences["size"] = profile.SizePreference
	}
	if _, exists := sess.Preferences["colors"]; !exists && profile.ColorPreferences != nil {
		var colors []string
		if err := json.Unmarshal(profile.ColorPreferences, &colors); err == nil && len(colors) > 0 {
			sess.Preferences["colors"] = strings.Join(colors, ",")
		}
	}
	if _, exists := sess.Preferences["budget"]; !exists && profile.BudgetRange != nil {
		var budget struct {
			Min float64 `json:"min"`
			Max float64 `json:"max"`
		}
		if err := json.Unmarshal(profile.BudgetRange, &budget); err == nil && budget.Max > 0 {
			sess.Preferences["budget"] = fmt.Sprintf("$%.0f-$%.0f", budget.Min, budget.Max)
		}
	}
	if _, exists := sess.Preferences["price_sensitive"]; !exists && profile.PriceSensitivity != "" && profile.PriceSensitivity != "U" {
		sess.Preferences["price_sensitive"] = profile.PriceSensitivity
	}
	if _, exists := sess.Preferences["high_intent_no_purchase"]; !exists && profile.IntentStrength == "H" && profile.PurchasesViaChat == 0 {
		sess.Preferences["high_intent_no_purchase"] = "true"
	}
	if _, exists := sess.Preferences["repeat_customer"]; !exists && profile.PurchasesViaChat > 0 {
		sess.Preferences["repeat_customer"] = "true"
	}
}

// ── Persona Context Builder ──────────────────────────────────────────────────

// buildPersonaContext creates a customer-specific context string for the system prompt.
// Injected to make the Agent aware of the customer's history and preferences.
func buildPersonaContext(profile *model.CustomerChatProfile, sess *ChatSession) string {
	// Start with session preferences (short-term memory from current conversation)
	prefs := sess.Preferences
	if prefs == nil {
		prefs = make(map[string]string)
	}

	var parts []string

	// Session-level context (from Preferences accumulated in this session)
	if size, ok := prefs["size"]; ok && size != "" {
		parts = append(parts, fmt.Sprintf("Customer's size: %s. Prioritize products available in this size.", size))
	}
	if colors, ok := prefs["colors"]; ok && colors != "" {
		parts = append(parts, fmt.Sprintf("Customer prefers these colors: %s.", colors))
	}
	if budget, ok := prefs["budget"]; ok && budget != "" {
		parts = append(parts, fmt.Sprintf("Customer's budget range: %s.", budget))
	}
	if prefs["price_sensitive"] == "true" || prefs["price_sensitive"] == "H" {
		parts = append(parts, "This customer is price-sensitive. Lead with value, only offer discount if absolutely necessary.")
	}

	// Long-term profile context
	if profile != nil {
		if profile.TotalChatSessions > 1 {
			parts = append(parts, fmt.Sprintf("This customer has visited %d times before. They are a returning visitor.", profile.TotalChatSessions))
		}
		if profile.PurchasesViaChat > 0 {
			parts = append(parts, fmt.Sprintf("They have purchased %d times via chat. Treat them as a valued repeat buyer.", profile.PurchasesViaChat))
		}
		if profile.IntentStrength == "H" && profile.PurchasesViaChat == 0 {
			parts = append(parts, "They showed strong purchase intent in a previous visit but didn't buy. Make extra effort to close this time.")
		}
		if profile.DiscountRequests > 0 {
			parts = append(parts, fmt.Sprintf("They have requested discounts %d times in the past. Be prepared to handle price objections.", profile.DiscountRequests))
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return "\n## CUSTOMER CONTEXT (use this to personalize your responses)\n" + strings.Join(parts, "\n")
}

// ── Profile Upsert (called by VCI consumer) ──────────────────────────────────

// UpsertCustomerProfile creates or updates a customer profile from accumulated signals.
// sessionID is used to deduplicate per-session counters.
func UpsertCustomerProfile(ctx context.Context, db *gorm.DB, shopID, customerID, customerEmail, eventType, sessionID string, metadata map[string]interface{}) error {
	if db == nil || customerID == "" {
		return nil
	}

	now := time.Now()
	var profile model.CustomerChatProfile
	err := db.WithContext(ctx).
		Where("shop_id = ? AND customer_id = ?", shopID, customerID).
		First(&profile).Error

	isNew := false
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return err
		}
		isNew = true
		profile = model.CustomerChatProfile{
			ShopID:        parseUUID(shopID),
			CustomerID:    customerID,
			CustomerEmail: customerEmail,
			FirstSeenAt:   &now,
		}
	}

	// Update timestamps (per-session counters handled in agent.go Chat())
	profile.LastSeenAt = &now
	if isNew {
		profile.TotalChatSessions = 1
	}

	// Update preferences based on event type
	switch eventType {
	case "size_preference":
		if size, ok := metadata["size"].(string); ok && size != "" {
			profile.SizePreference = size
		}
	case "color_preference":
		if color, ok := metadata["color"].(string); ok && color != "" {
			appendJSONArray(&profile.ColorPreferences, color)
		}
	case "purchase_signal":
		profile.IntentStrength = "H"
		// Note: PurchasesViaChat tracks INTENT signals, not confirmed purchases.
		// Confirmed purchases would come from order webhook correlation (Phase 3).
	case "price_objection":
		profile.PriceSensitivity = "H"
		profile.DiscountRequests++
	case "discount_request":
		profile.DiscountRequests++
	case "intent_high_no_purchase":
		profile.IntentStrength = "H"
	}

	if isNew {
		return db.WithContext(ctx).Create(&profile).Error
	}
	return db.WithContext(ctx).Save(&profile).Error
}

// ── Product Insight Upsert ───────────────────────────────────────────────────

// UpsertProductInsight atomically increments product-level chat analytics.
// Uses UPDATE counter = counter + 1 to avoid race conditions on concurrent events.
func UpsertProductInsight(ctx context.Context, db *gorm.DB, shopID, productID, eventType string) error {
	if db == nil || productID == "" {
		return nil
	}

	periodStart := time.Now().Format("2006-01") // monthly bucket
	shopUUID := parseUUID(shopID)

	// Map event type to column
	col := ""
	switch eventType {
	case "purchase_signal":
		col = "times_recommended"
	case "price_objection":
		col = "times_objected"
	case "size_preference", "color_preference":
		col = "times_questioned"
	}
	if col == "" {
		return nil
	}

	// Try atomic update first
	result := db.WithContext(ctx).Model(&model.ProductChatInsight{}).
		Where("shop_id = ? AND product_id = ? AND period_start = ?", shopUUID, productID, periodStart).
		UpdateColumn(col, gorm.Expr(col+" + 1"))

	if result.RowsAffected == 0 {
		// Create new record if none exists — look up product title
		insight := model.ProductChatInsight{
			ShopID:      shopUUID,
			ProductID:   productID,
			PeriodStart: periodStart,
		}
		// Populate product title from synced_products
		var product struct{ Title string }
		if err := db.WithContext(ctx).Table("synced_products").
			Where("platform_id = ? AND shop_id = ?", productID, shopUUID).
			Select("title").First(&product).Error; err == nil {
			insight.ProductTitle = product.Title
		}
		// Set the initial count for the relevant column
		switch eventType {
		case "purchase_signal":
			insight.TimesRecommended = 1
		case "price_objection":
			insight.TimesObjected = 1
		case "size_preference", "color_preference":
			insight.TimesQuestioned = 1
		}
		if err := db.WithContext(ctx).Create(&insight).Error; err != nil {
			// If duplicate key (race), retry with update
			if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
				return db.WithContext(ctx).Model(&model.ProductChatInsight{}).
					Where("shop_id = ? AND product_id = ? AND period_start = ?", shopUUID, productID, periodStart).
					UpdateColumn(col, gorm.Expr(col+" + 1")).Error
			}
			return err
		}
	}

	return nil
}

func appendJSONArray(field *datatypes.JSON, item string) {
	if field == nil {
		return
	}
	var arr []string
	if len(*field) > 0 {
		json.Unmarshal(*field, &arr)
	}
	for _, existing := range arr {
		if existing == item {
			return // already present
		}
	}
	arr = append(arr, item)
	data, err := json.Marshal(arr)
	if err == nil {
		*field = data
	}
}

func appendObjection(field *datatypes.JSON, reason string) {
	if field == nil {
		return
	}
	type objection struct {
		Reason string `json:"reason"`
		Count  int    `json:"count"`
	}
	var arr []objection
	if len(*field) > 0 {
		json.Unmarshal(*field, &arr)
	}
	for i, o := range arr {
		if o.Reason == reason {
			arr[i].Count++
			data, _ := json.Marshal(arr)
			*field = data
			return
		}
	}
	arr = append(arr, objection{Reason: reason, Count: 1})
	data, _ := json.Marshal(arr)
	*field = data
}

func parseUUID(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil
	}
	return id
}
