package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
)

// CartRecoveryExecutor handles abandoned checkout detection, discount code
// generation, email template rendering, and recovery email dispatch.
type CartRecoveryExecutor struct {
	db              *gorm.DB
	discountService *service.ShopifyDiscountClient
	emailClient     *service.ResendClient
}

// NewCartRecoveryExecutor creates a new CartRecoveryExecutor.
func NewCartRecoveryExecutor(db *gorm.DB, discountService *service.ShopifyDiscountClient, emailClient *service.ResendClient) *CartRecoveryExecutor {
	return &CartRecoveryExecutor{
		db:              db,
		discountService: discountService,
		emailClient:     emailClient,
	}
}

// ProcessAbandonedCheckouts finds open checkouts past their delay window,
// matches them to active campaigns, generates discount codes, renders and
// sends recovery emails, and records the results.
func (e *CartRecoveryExecutor) ProcessAbandonedCheckouts(ctx context.Context, shopID uuid.UUID) error {
	// 1. Query all active campaigns for this shop
	var campaigns []model.CartRecoveryCampaign
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND is_active = true AND schedule_enabled = true", shopID).
		Find(&campaigns).Error; err != nil {
		return fmt.Errorf("cart_recovery: fetch campaigns: %w", err)
	}
	if len(campaigns) == 0 {
		return nil
	}

	// 2. For each campaign, find eligible checkouts
	for _, campaign := range campaigns {
		cutoff := time.Now().Add(-time.Duration(campaign.DelayMinutes) * time.Minute)

		var checkouts []model.SyncedCheckout
		err := e.db.WithContext(ctx).
			Where("shop_id = ? AND status = ? AND updated_at <= ? AND customer_email != ''",
				shopID, "open", cutoff).
			Order("updated_at ASC").
			Limit(50).
			Find(&checkouts).Error
		if err != nil {
			slog.Error("cart_recovery: failed to query checkouts",
				"campaign_id", campaign.ID, "error", err)
			continue
		}

		for _, checkout := range checkouts {
			// Check if already sent for this checkout
			var existingSend model.CartRecoverySend
			if err := e.db.WithContext(ctx).
				Where("checkout_id = ? AND campaign_id = ? AND shop_id = ?", checkout.ID, campaign.ID, shopID).
				First(&existingSend).Error; err == nil {
				// Already sent, skip
				slog.Debug("cart_recovery: already sent for checkout, skipping",
					"checkout_id", checkout.ID, "campaign_id", campaign.ID)
				continue
			}

			if err := e.processCheckout(ctx, shopID, &campaign, &checkout); err != nil {
				slog.Error("cart_recovery: failed to process checkout",
					"checkout_id", checkout.ID, "error", err)
				// Continue to next checkout on error
			}
		}
	}

	return nil
}

// processCheckout handles a single abandoned checkout: generates a discount
// code, renders and sends the recovery email, records the send event, and
// finally marks the checkout as abandoned ONLY after everything succeeds.
//
// Important: abandoned status is set LAST so that if Shopify API or Resend
// fails, the checkout remains "open" and will be retried on the next scan.
func (e *CartRecoveryExecutor) processCheckout(ctx context.Context, shopID uuid.UUID, campaign *model.CartRecoveryCampaign, checkout *model.SyncedCheckout) error {
	now := time.Now()

	// 1. Generate discount code (if campaign has a discount percentage)
	var discountCode *model.CartRecoveryCode
	if campaign.DiscountPercent > 0 {
		code := campaign.CodePrefix + "-" + randomSuffix()

		// Look up shop domain and access token for Shopify API calls
		var shop model.Shop
		if err := e.db.WithContext(ctx).
			Where("id = ?", shopID).
			Select("shop_domain, access_token").
			First(&shop).Error; err != nil {
			return fmt.Errorf("cart_recovery: lookup shop: %w", err)
		}

		priceRuleID, err := e.discountService.CreatePriceRule(shop.Domain, "", service.PriceRuleInput{
			Title:        fmt.Sprintf("Cart Recovery - %s", campaign.Name),
			TargetType:   "line_item",
			TargetSelect: "all",
			Allocation:   "across",
			ValueType:    "percentage",
			Value:        -campaign.DiscountPercent,
			UsageLimit:   1,
			StartsAt:     now.Format(time.RFC3339),
			EndsAt:       now.Add(30 * 24 * time.Hour).Format(time.RFC3339),
		})
		if err != nil {
			return fmt.Errorf("cart_recovery: create price rule: %w", err)
		}

		shopifyCodeID, err := e.discountService.CreateDiscountCode(shop.Domain, "", priceRuleID, code)
		if err != nil {
			return fmt.Errorf("cart_recovery: create discount code: %w", err)
		}

		discountCode = &model.CartRecoveryCode{
			CampaignID:      campaign.ID,
			ShopID:          shopID,
			CheckoutID:      checkout.ID,
			Code:            code,
			ShopifyCodeID:   fmt.Sprintf("%d", shopifyCodeID),
			DiscountPercent: campaign.DiscountPercent,
			ExpiresAt:       now.Add(30 * 24 * time.Hour),
		}
		if err := e.db.WithContext(ctx).Create(discountCode).Error; err != nil {
			return fmt.Errorf("cart_recovery: save discount code: %w", err)
		}
	}

	// 2. Render and send recovery email
	discountCodeStr := ""
	discountPercentStr := ""
	if discountCode != nil {
		discountCodeStr = discountCode.Code
		discountPercentStr = fmt.Sprintf("%.0f", campaign.DiscountPercent)
	}

	// Look up shop name for email template
	shopName := ""
	var shop model.Shop
	if err := e.db.WithContext(ctx).
		Where("id = ?", shopID).
		Select("shop_domain").
		First(&shop).Error; err == nil {
		shopName = shop.Domain
	}

	vars := map[string]string{
		"customer_name":    checkout.CustomerName,
		"discount_code":    discountCodeStr,
		"discount_percent": discountPercentStr,
		"cart_items":       formatLineItems(checkout.LineItems),
		"shop_name":        shopName,
	}
	htmlBody := renderEmailTemplate(campaign.EmailBody, vars)

	messageID, err := e.emailClient.SendToCustomer(checkout.CustomerEmail, campaign.EmailSubject, htmlBody)
	if err != nil {
		slog.Error("cart_recovery: failed to send email",
			"checkout_id", checkout.ID, "error", err)
		// Record failed send for idempotency, then return error so checkout stays "open"
		failed := model.CartRecoverySend{
			CampaignID:      campaign.ID,
			ShopID:          shopID,
			CheckoutID:      checkout.ID,
			CustomerEmail:   checkout.CustomerEmail,
			ResendMessageID: "",
			Status:          "failed",
		}
		if discountCode != nil {
			failed.DiscountCodeID = &discountCode.ID
		}
		e.db.WithContext(ctx).Create(&failed)
		return fmt.Errorf("cart_recovery: send email: %w", err)
	}

	// 3. Record the successful send
	send := model.CartRecoverySend{
		CampaignID:      campaign.ID,
		ShopID:          shopID,
		CheckoutID:      checkout.ID,
		CustomerEmail:   checkout.CustomerEmail,
		ResendMessageID: messageID,
		Status:          "sent",
	}
	if discountCode != nil {
		send.DiscountCodeID = &discountCode.ID
	}
	if err := e.db.WithContext(ctx).Create(&send).Error; err != nil {
		slog.Error("cart_recovery: failed to save send record", "error", err)
	}

	// 4. Update campaign last executed timestamp
	e.db.WithContext(ctx).Model(campaign).Update("last_executed_at", now)

	// 5. Atomically mark as abandoned — LAST, after everything succeeded.
	// This ensures checkout stays "open" for retry if any step above fails.
	result := e.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
		Where("id = ? AND status = ?", checkout.ID, "open").
		Updates(map[string]interface{}{
			"status":       "abandoned",
			"abandoned_at": now,
			"campaign_id":  campaign.ID,
		})
	if result.Error != nil || result.RowsAffected == 0 {
		slog.Warn("cart_recovery: mark abandoned skipped",
			"checkout_id", checkout.ID, "rows_affected", result.RowsAffected)
	}

	return nil
}

// renderEmailTemplate replaces placeholders like {{customer_name}} in the
// email body template with the provided variable values.
func renderEmailTemplate(htmlTemplate string, vars map[string]string) string {
	result := htmlTemplate
	for k, v := range vars {
		result = strings.ReplaceAll(result, "{{"+k+"}}", v)
	}
	return result
}

// formatLineItems converts the JSON line items from a checkout into a simple
// text representation for email rendering.
func formatLineItems(raw datatypes.JSON) string {
	var items []struct {
		Title string `json:"title"`
		Price string `json:"price"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return ""
	}
	var parts []string
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("• %s — $%s", item.Title, item.Price))
	}
	return strings.Join(parts, "\n")
}

// randomSuffix generates a 4-character uppercase hex suffix for discount codes.
func randomSuffix() string {
	b := make([]byte, 2)
	rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}
