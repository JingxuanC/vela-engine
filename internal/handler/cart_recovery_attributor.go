package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// CartRecoveryAttributor matches discount codes used during order creation
// back to cart recovery campaigns, marking codes as used and checkouts as recovered.
type CartRecoveryAttributor struct {
	db       *gorm.DB
	eventBus eventbus.EventBus
}

// NewCartRecoveryAttributor creates a new CartRecoveryAttributor.
func NewCartRecoveryAttributor(db *gorm.DB, bus eventbus.EventBus) *CartRecoveryAttributor {
	return &CartRecoveryAttributor{db: db, eventBus: bus}
}

// AttributeOrder processes discount codes from an order webhook, matches them
// to CartRecoveryCode records, marks them as used, and updates the associated
// SyncedCheckout status to "recovered". Returns the list of matched campaign IDs.
func (a *CartRecoveryAttributor) AttributeOrder(
	ctx context.Context,
	shopID uuid.UUID,
	platformID string,
	totalPrice float64,
	discountCodes []shopifyDiscountCode,
) ([]uuid.UUID, error) {
	if len(discountCodes) == 0 {
		return nil, nil
	}

	var matchedCampaigns []uuid.UUID
	now := time.Now()

	for _, dc := range discountCodes {
		code := strings.TrimSpace(dc.Code)
		if code == "" {
			continue
		}

		prefix := extractCodePrefix(code)
		if prefix == "" {
			slog.Debug("cart_recovery_attrib: no RECOVER prefix found", "code", code)
			continue
		}

		// Query CartRecoveryCode by code AND shop_id
		var recoveryCode model.CartRecoveryCode
		err := a.db.WithContext(ctx).
			Where("code = ? AND shop_id = ?", code, shopID).
			First(&recoveryCode).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				slog.Warn("cart_recovery_attrib: query recovery code failed",
					"code", code, "shop_id", shopID, "error", err)
			}
			continue
		}

		// Atomically mark code as used below — skip earlier is_used check
		// (the WHERE is_used = false in the UPDATE handles race conditions)

		// Mark code as used atomically — only if is_used is still false
		result := a.db.WithContext(ctx).Model(&model.CartRecoveryCode{}).
			Where("id = ? AND is_used = ? AND shop_id = ?", recoveryCode.ID, false, shopID).
			Updates(map[string]interface{}{
				"is_used":     true,
				"used_at":     now,
				"order_id":    platformID,
				"order_total": totalPrice,
			})
		if result.Error != nil {
			slog.Error("cart_recovery_attrib: failed to mark code used",
				"code", code, "error", result.Error)
			continue
		}
		if result.RowsAffected == 0 {
			// Already marked as used by another goroutine — skip
			slog.Debug("cart_recovery_attrib: code already used by concurrent order", "code", code)
			continue
		}

		// Update associated SyncedCheckout status to "recovered"
		if recoveryCode.CheckoutID != uuid.Nil {
			if err := a.db.WithContext(ctx).Model(&model.SyncedCheckout{}).
				Where("id = ? AND shop_id = ?", recoveryCode.CheckoutID, shopID).
				Updates(map[string]interface{}{
					"status":       "recovered",
					"recovered_at": now,
					"order_id":     platformID,
				}).Error; err != nil {
				slog.Warn("cart_recovery_attrib: failed to update checkout status",
					"checkout_id", recoveryCode.CheckoutID, "error", err)
			}
		}

		matchedCampaigns = append(matchedCampaigns, recoveryCode.CampaignID)

		slog.Info("cart_recovery_attrib: order recovered",
			"order_id", platformID,
			"code", code,
			"campaign_id", recoveryCode.CampaignID,
			"total_price", totalPrice,
		)
	}

	// Publish cart_recovery.order_recovered event for each matched campaign
	if a.eventBus != nil && len(matchedCampaigns) > 0 {
		for _, campaignID := range matchedCampaigns {
			ev, err := eventbus.NewEvent(eventbus.EventOrderRecovered, shopID,
				map[string]interface{}{
					"order_id":    platformID,
					"campaign_id": campaignID.String(),
					"shop_id":     shopID.String(),
					"total_price": totalPrice,
					"timestamp":   now.UTC().Format(time.RFC3339),
				}, "webhook/orders/create")
			if err != nil {
				slog.Warn("cart_recovery_attrib: failed to create event", "error", err)
				continue
			}
			pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := a.eventBus.Publish(pubCtx, ev); err != nil {
				slog.Error("cart_recovery_attrib: failed to publish order_recovered event",
					"order_id", platformID, "campaign_id", campaignID, "error", err)
			}
			cancel()
		}
	}

	return matchedCampaigns, nil
}

// extractCodePrefix splits a discount code like "RECOVER-A1B2-X7K9" and
// returns the prefix "RECOVER-A1B2".
func extractCodePrefix(code string) string {
	// Must start with "RECOVER-"
	if !strings.HasPrefix(strings.ToUpper(code), "RECOVER-") {
		return ""
	}
	// Split by "-" and take first two segments
	parts := strings.Split(code, "-")
	if len(parts) < 3 {
		// Not enough segments; fall back to first two if available
		if len(parts) == 2 {
			return strings.ToUpper(code)
		}
		return ""
	}
	// Return "RECOVER-A1B2" from "RECOVER-A1B2-X7K9"
	return fmt.Sprintf("%s-%s", parts[0], parts[1])
}
