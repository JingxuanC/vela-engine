package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	shopifySvc "github.com/JingxuanC/vela-engine/internal/service/shopify"
)

// ContentAttributor matches Shopify orders to ContentPiece records via UTM
// parameters parsed from the order's landingPage URL (queried via Shopify
// Admin GraphQL API).
type ContentAttributor struct {
	db          *gorm.DB
	cfg         *config.Config
	eventBus    eventbus.EventBus
	adminClient *shopifySvc.AdminClient
}

// NewContentAttributor creates a new ContentAttributor.
func NewContentAttributor(db *gorm.DB, cfg *config.Config, bus eventbus.EventBus, adminClient *shopifySvc.AdminClient) *ContentAttributor {
	return &ContentAttributor{db: db, cfg: cfg, eventBus: bus, adminClient: adminClient}
}

// AttributionResult summarizes the outcome of an order attribution attempt.
type AttributionResult struct {
	Attributed    bool      `json:"attributed"`
	ContentPieceID uuid.UUID `json:"content_piece_id,omitempty"`
	ShortID       string    `json:"short_id,omitempty"`
	Revenue       float64   `json:"revenue,omitempty"`
}

// AttributeOrder queries Shopify's Admin API for the order's landingPage URL,
// parses UTM parameters, and updates ContentPerformance if the traffic came
// from Vela AI content.
func (a *ContentAttributor) AttributeOrder(
	ctx context.Context,
	shopID uuid.UUID,
	orderPlatformID string,
	orderTotal float64,
) (*AttributionResult, error) {
	// 1. Look up shop to get domain + access token
	var shop model.Shop
	if err := a.db.WithContext(ctx).Where("id = ?", shopID).First(&shop).Error; err != nil {
		slog.Warn("content_attr: shop not found", "shop_id", shopID, "error", err)
		return nil, fmt.Errorf("shop not found: %w", err)
	}

	if "" == "" {
		slog.Warn("content_attr: shop has no access token", "shop_id", shopID, "domain", shop.Domain)
		return nil, fmt.Errorf("shop has no access token")
	}

	// 2. Query Shopify Admin GraphQL API for landingPage URL
	orderGID := fmt.Sprintf("gid://shopify/Order/%s", orderPlatformID)
	landingURL, err := a.adminClient.FetchOrderLandingPage(ctx, shop.Domain, "", orderGID)
	if err != nil {
		slog.Warn("content_attr: failed to fetch landing page",
			"order_id", orderPlatformID, "shop_domain", shop.Domain, "error", err)
		return nil, fmt.Errorf("fetch landing page: %w", err)
	}

	if landingURL == "" {
		slog.Debug("content_attr: no landing page URL for order", "order_id", orderPlatformID)
		return nil, nil
	}

	// 3. Parse UTM parameters
	shortID := parseContentShortID(landingURL)
	if shortID == "" {
		return nil, nil // not from Vela content
	}

	// 4. Look up ContentPiece by short_id + shop_id
	var piece model.ContentPiece
	if err := a.db.WithContext(ctx).
		Where("short_id = ? AND shop_id = ?", shortID, shopID).
		First(&piece).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Debug("content_attr: content piece not found for short_id",
				"short_id", shortID, "shop_id", shopID)
		} else {
			slog.Warn("content_attr: query content piece failed",
				"short_id", shortID, "shop_id", shopID, "error", err)
		}
		return nil, err
	}

	// 5. Compute attributed units from order line items
	// (We don't have line items here — the webhook already has the total.
	//  For now we attribute 1 unit per order. Full line-item attribution
	//  can be added later by passing line items through.)
	units := 1

	// 6. Atomic Upsert ContentPerformance (race-free via ON CONFLICT)
	now := time.Now()
	perf := model.ContentPerformance{
		ContentPieceID:    piece.ID,
		ShopID:            shopID,
		ContentType:       piece.ContentType,
		AttributedOrders:  1,
		AttributedRevenue: orderTotal,
		AttributedUnits:   units,
		LastAttributionAt: &now,
	}

	err = a.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "shop_id"}, {Name: "content_piece_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"attributed_orders":   gorm.Expr("content_performances.attributed_orders + 1"),
			"attributed_revenue":  gorm.Expr("content_performances.attributed_revenue + ?", orderTotal),
			"attributed_units":    gorm.Expr("content_performances.attributed_units + ?", units),
			"last_attribution_at": now,
		}),
	}).Create(&perf).Error
	if err != nil {
		slog.Error("content_attr: failed to upsert performance",
			"content_piece_id", piece.ID, "error", err)
		return nil, fmt.Errorf("upsert performance: %w", err)
	}

	// 7. Create AnalyticsEvent for the attribution
	analyticsEvent := model.AnalyticsEvent{
		ShopID:     shopID,
		CustomerID: "", // order customer email not available at this level
		EventType:  "purchase",
		Channel:    "content",
		Source:     "vela_ai",
		Medium:     piece.ContentType,
		URL:        landingURL,
		OrderID:    orderPlatformID,
		OrderValue: orderTotal,
	}
	if err := a.db.WithContext(ctx).Create(&analyticsEvent).Error; err != nil {
		slog.Warn("content_attr: failed to create analytics event", "error", err)
		// Non-fatal — attribution succeeded
	}

	// 8. Publish content.attributed EventBus event
	if a.eventBus != nil {
		ev, err := eventbus.NewEvent(eventbus.EventContentAttributed, shopID,
			map[string]interface{}{
				"content_piece_id":  piece.ID.String(),
				"short_id":          shortID,
				"order_platform_id": orderPlatformID,
				"revenue":           orderTotal,
				"timestamp":         now.UTC().Format(time.RFC3339),
			},
			"content-attribution")
		if err != nil {
			slog.Warn("content_attr: failed to create event", "error", err)
		} else {
			pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := a.eventBus.Publish(pubCtx, ev); err != nil {
				slog.Error("content_attr: failed to publish content.attributed event",
					"order_id", orderPlatformID, "content_piece_id", piece.ID, "error", err)
			}
		}
	}

	slog.Info("content_attr: order attributed",
		"order_id", orderPlatformID,
		"short_id", shortID,
		"content_piece_id", piece.ID,
		"content_type", piece.ContentType,
		"revenue", orderTotal,
	)

	return &AttributionResult{
		Attributed:    true,
		ContentPieceID: piece.ID,
		ShortID:       shortID,
		Revenue:       orderTotal,
	}, nil
}

// OnContentGenerated is an EventBus subscriber that creates a ContentPerformance
// record when AI content is generated, setting up the attribution target.
func (a *ContentAttributor) OnContentGenerated(ctx context.Context, event *eventbus.Event) error {
	var payload struct {
		JobID    string   `json:"job_id"`
		JobType  string   `json:"job_type"`
		PieceIDs []string `json:"piece_ids"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return nil // not our event format
	}

	for _, pidStr := range payload.PieceIDs {
		pieceID, err := uuid.Parse(pidStr)
		if err != nil {
			continue
		}
		var piece model.ContentPiece
		if err := a.db.WithContext(ctx).Where("id = ?", pieceID).First(&piece).Error; err != nil {
			slog.Warn("content_attr: piece not found for performance init", "piece_id", pidStr, "error", err)
			continue
		}
		perf := model.ContentPerformance{
			ContentPieceID: piece.ID,
			ShopID:         piece.ShopID,
			ContentType:    piece.ContentType,
		}
		if err := a.db.WithContext(ctx).
			Where("shop_id = ? AND content_piece_id = ?", piece.ShopID, piece.ID).
			FirstOrCreate(&perf).Error; err != nil {
			slog.Warn("content_attr: failed to init performance for piece", "piece_id", pidStr, "error", err)
		}
	}
	return nil
}

// parseContentShortID parses a landing page URL's UTM parameters and extracts
// the content_short_id (utm_content) only if utm_source is "vela_ai".
// Returns empty string if the URL is not from Vela content.
func parseContentShortID(landingURL string) string {
	if landingURL == "" {
		return ""
	}

	u, err := url.Parse(landingURL)
	if err != nil {
		slog.Warn("content_attr: failed to parse landing URL", "url", landingURL, "error", err)
		return ""
	}

	params := u.Query()

	// Only attribute if utm_source is "vela_ai"
	source := strings.ToLower(params.Get("utm_source"))
	if source != "vela_ai" {
		return ""
	}

	// Extract content short_id from utm_content
	shortID := strings.TrimSpace(params.Get("utm_content"))
	if shortID == "" {
		slog.Debug("content_attr: vela_ai source but no utm_content", "url", landingURL)
		return ""
	}

	return shortID
}

// extractAttributionFromQuery parses UTM parameters from the URL query.
