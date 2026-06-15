// Package judgeme provides Judge.me integration services.
package judgeme

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// WebhookEvent represents a Judge.me webhook event payload.
type WebhookEvent struct {
	Event    string `json:"event"`     // review/created, review/updated
	ReviewID int    `json:"review_id"` // The ID of the affected review
	ShopID   string `json:"shop_id"`   // Judge.me shop identifier
}

// SyncService handles syncing reviews from Judge.me to the local database.
type SyncService struct {
	db          *gorm.DB
	eventBus    eventbus.EventBus
	clientMaker func(setting *model.JudgeMeSetting) *JudgeMeClient
}

// NewSyncService creates a new SyncService.
func NewSyncService(db *gorm.DB, eb eventbus.EventBus) *SyncService {
	return &SyncService{
		db:       db,
		eventBus: eb,
		clientMaker: func(setting *model.JudgeMeSetting) *JudgeMeClient {
			return NewJudgeMeClient(setting.APIToken, setting.Domain)
		},
	}
}

// getSettings retrieves the active Judge.me settings for a shop.
func (s *SyncService) getSettings(ctx context.Context, shopID uuid.UUID) (*model.JudgeMeSetting, error) {
	var setting model.JudgeMeSetting
	if err := s.db.WithContext(ctx).Where("shop_id = ? AND is_active = ?", shopID, true).First(&setting).Error; err != nil {
		return nil, fmt.Errorf("judgeme: settings not found for shop %s: %w", shopID, err)
	}
	return &setting, nil
}

// LogSync records a sync operation log entry.
func (s *SyncService) LogSync(ctx context.Context, shopID uuid.UUID, action, status, message string, count int) {
	log := model.JudgeMeSyncLog{
		ShopID:       shopID,
		Action:       action,
		Status:       status,
		Message:      message,
		ReviewsCount: count,
	}
	if err := s.db.WithContext(ctx).Create(&log).Error; err != nil {
		slog.Error("judgeme: failed to write sync log", "shop_id", shopID, "error", err)
	}
}

// FullSync performs a full pull of all reviews from Judge.me to local SyncedReview table.
func (s *SyncService) FullSync(ctx context.Context, shopID uuid.UUID) error {
	setting, err := s.getSettings(ctx, shopID)
	if err != nil {
		return err
	}

	slog.Info("judgeme: starting full sync", "shop_id", shopID)
	client := s.clientMaker(setting)

	page := 1
	totalSynced := 0

	for {
		params := ListReviewsParams{
			Page:    page,
			PerPage: 100,
		}

		resp, err := client.GetReviews(ctx, params)
		if err != nil {
			s.LogSync(ctx, shopID, "full_sync", "failed", fmt.Sprintf("page %d: %v", page, err), totalSynced)
			return fmt.Errorf("judgeme: full sync page %d: %w", page, err)
		}

		for i := range resp.Reviews {
			if err := s.upsertSyncedReview(ctx, shopID, &resp.Reviews[i], "full_sync"); err != nil {
				slog.Error("judgeme: full sync upsert error", "review_id", resp.Reviews[i].ID, "error", err)
				continue
			}
			totalSynced++
		}

		if page >= resp.TotalPages {
			break
		}
		page++
	}

	// Update LastSyncAt
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&model.JudgeMeSetting{}).
		Where("shop_id = ?", shopID).
		Update("last_sync_at", &now).Error; err != nil {
		slog.Error("judgeme: update last_sync_at", "shop_id", shopID, "error", err)
	}

	s.LogSync(ctx, shopID, "full_sync", "success", fmt.Sprintf("synced %d reviews", totalSynced), totalSynced)
	slog.Info("judgeme: full sync complete", "shop_id", shopID, "total", totalSynced)
	return nil
}

// IncrementalSync pulls reviews created after a given timestamp.
func (s *SyncService) IncrementalSync(ctx context.Context, shopID uuid.UUID, since time.Time) error {
	setting, err := s.getSettings(ctx, shopID)
	if err != nil {
		return err
	}

	slog.Info("judgeme: starting incremental sync", "shop_id", shopID, "since", since)
	client := s.clientMaker(setting)

	sinceStr := since.UTC().Format(time.RFC3339)
	page := 1
	totalSynced := 0

	for {
		params := ListReviewsParams{
			CreatedAtMin: &sinceStr,
			Page:         page,
			PerPage:      100,
		}

		resp, err := client.GetReviews(ctx, params)
		if err != nil {
			s.LogSync(ctx, shopID, "incremental_sync", "failed", fmt.Sprintf("page %d: %v", page, err), totalSynced)
			return fmt.Errorf("judgeme: incremental sync page %d: %w", page, err)
		}

		if len(resp.Reviews) == 0 {
			break
		}

		for i := range resp.Reviews {
			if err := s.upsertSyncedReview(ctx, shopID, &resp.Reviews[i], "incremental_sync"); err != nil {
				slog.Error("judgeme: incremental sync upsert error", "review_id", resp.Reviews[i].ID, "error", err)
				continue
			}
			totalSynced++
		}

		if page >= resp.TotalPages {
			break
		}
		page++
	}

	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(&model.JudgeMeSetting{}).
		Where("shop_id = ?", shopID).
		Update("last_sync_at", &now).Error; err != nil {
		slog.Error("judgeme: update last_sync_at", "shop_id", shopID, "error", err)
	}

	s.LogSync(ctx, shopID, "incremental_sync", "success", fmt.Sprintf("synced %d reviews", totalSynced), totalSynced)
	slog.Info("judgeme: incremental sync complete", "shop_id", shopID, "total", totalSynced)
	return nil
}

// ProcessNewReview processes a single new review — triggers auto-reply for bad reviews.
func (s *SyncService) ProcessNewReview(ctx context.Context, shopID uuid.UUID, jr *JudgeMeReview) error {
	platformID := strconv.Itoa(jr.ID)

	// Check if review already exists
	var existingCount int64
	if err := s.db.WithContext(ctx).Model(&model.SyncedReview{}).
		Where("shop_id = ? AND platform = ? AND platform_id = ?", shopID, "judgeme", platformID).
		Count(&existingCount).Error; err != nil {
		slog.Error("judgeme: failed to check existing review", "review_id", jr.ID, "error", err)
		// Proceed — upsert will handle the duplicate
	}

	if existingCount > 0 {
		slog.Debug("judgeme: review already exists, skipping", "review_id", jr.ID)
		return nil
	}

	// Upsert the synced review
	if err := s.upsertSyncedReview(ctx, shopID, jr, "webhook"); err != nil {
		return err
	}

	slog.Info("judgeme: new review synced", "review_id", jr.ID, "rating", jr.Rating)

	// Publish event to EventBus for async processing
	s.publishReviewSynced(ctx, shopID, platformID, jr)

	return nil
}

// SyncFromWebhook processes a review event received via webhook.
func (s *SyncService) SyncFromWebhook(ctx context.Context, shopID uuid.UUID, event *WebhookEvent) error {
	if event == nil {
		return fmt.Errorf("judgeme: nil webhook event")
	}

	slog.Info("judgeme: processing webhook event",
		"shop_id", shopID,
		"event_type", event.Event,
		"review_id", event.ReviewID,
	)

	// For webhook events, fetch the full review data from Judge.me API
	setting, err := s.getSettings(ctx, shopID)
	if err != nil {
		return err
	}

	client := s.clientMaker(setting)
	review, err := client.GetReview(ctx, event.ReviewID)
	if err != nil {
		return fmt.Errorf("judgeme: fetch review %d via webhook: %w", event.ReviewID, err)
	}

	switch event.Event {
	case "review/created":
		return s.ProcessNewReview(ctx, shopID, review)
	case "review/updated":
		return s.handleReviewUpdated(ctx, shopID, review)
	default:
		slog.Warn("judgeme: unknown webhook event type", "event", event.Event)
		return nil
	}
}

// handleReviewUpdated updates an existing synced review.
func (s *SyncService) handleReviewUpdated(ctx context.Context, shopID uuid.UUID, jr *JudgeMeReview) error {
	now := time.Now().UTC()
	createdAt, _ := time.Parse(time.RFC3339, jr.CreatedAt)
	if createdAt.IsZero() {
		createdAt = now
	}
	platformID := strconv.Itoa(jr.ID)

	hasReply := jr.PublicReply != nil && *jr.PublicReply != ""

	updates := map[string]interface{}{
		"title":            jr.Title,
		"body":             jr.Body,
		"rating":           jr.Rating,
		"reviewer_name":    jr.ReviewerName,
		"reviewer_email":   jr.ReviewerEmail,
		"product_id":       strconv.FormatInt(jr.ProductExternalID, 10),
		"state":            jr.State,
		"verified":         jr.Verified,
		"has_public_reply": hasReply,
		"synced_from":      "webhook",
		"synced_at":        now,
		"created_at":       createdAt,
		"updated_at":       now,
	}

	result := s.db.WithContext(ctx).Model(&model.SyncedReview{}).
		Where("shop_id = ? AND platform = ? AND platform_id = ?", shopID, "judgeme", platformID).
		Updates(updates)

	if result.Error != nil {
		return fmt.Errorf("judgeme: update synced review %d: %w", jr.ID, result.Error)
	}

	if result.RowsAffected == 0 {
		// Not found locally — insert as new
		return s.ProcessNewReview(ctx, shopID, jr)
	}

	slog.Info("judgeme: review updated", "review_id", jr.ID, "rating", jr.Rating)

	// Publish event for Insights engine and other consumers
	s.publishReviewSynced(ctx, shopID, platformID, jr)

	return nil
}

// publishReviewSynced publishes a review.synced event to the EventBus.
// Payload includes enough data for the consumer to avoid an extra API call.
func (s *SyncService) publishReviewSynced(ctx context.Context, shopID uuid.UUID, platformID string, jr *JudgeMeReview) {
	if s.eventBus == nil {
		return
	}

	hasReply := jr.PublicReply != nil && *jr.PublicReply != ""

	payload := map[string]interface{}{
		"platform":       "judgeme",
		"platform_id":    platformID,
		"product_id":     strconv.FormatInt(jr.ProductExternalID, 10),
		"rating":         jr.Rating,
		"title":          jr.Title,
		"body":           jr.Body,
		"reviewer_name":  jr.ReviewerName,
		"reviewer_email": jr.ReviewerEmail,
		"has_reply":      hasReply,
	}

	event, err := eventbus.NewEvent(eventbus.EventReviewSynced, shopID, payload, "judgeme")
	if err != nil {
		slog.Error("judgeme: failed to create review.synced event", "error", err)
		return
	}

	if err := s.eventBus.Publish(ctx, event); err != nil {
		slog.Error("judgeme: failed to publish review.synced event", "error", err)
	}
}

// upsertSyncedReview creates or updates a synced review in the local database.
func (s *SyncService) upsertSyncedReview(ctx context.Context, shopID uuid.UUID, jr *JudgeMeReview, source string) error {
	now := time.Now().UTC()
	createdAt, _ := time.Parse(time.RFC3339, jr.CreatedAt)
	if createdAt.IsZero() {
		createdAt = now
	}

	platformID := strconv.Itoa(jr.ID)
	hasReply := jr.PublicReply != nil && *jr.PublicReply != ""

	review := model.SyncedReview{
		ShopID:        shopID,
		Platform:      "judgeme",
		PlatformID:    platformID,
		Title:         jr.Title,
		Body:          jr.Body,
		Rating:        jr.Rating,
		ReviewerName:  jr.ReviewerName,
		ReviewerEmail: jr.ReviewerEmail,
		ProductID:     strconv.FormatInt(jr.ProductExternalID, 10),
		State:         jr.State,
		Verified:      jr.Verified,
		HasPublicReply: hasReply,
		SyncedFrom:    source,
		SyncedAt:      now,
		CreatedAt:     createdAt,
		UpdatedAt:     now,
		JudgeMeReviewID: jr.ID, // legacy — kept for migration
	}

	// Use upsert (on conflict do update)
	err := s.db.WithContext(ctx).Where(
		"shop_id = ? AND platform = ? AND platform_id = ?", shopID, "judgeme", platformID,
	).Assign(review).FirstOrCreate(&model.SyncedReview{}).Error

	if err != nil {
		return fmt.Errorf("judgeme: upsert synced review %d: %w", jr.ID, err)
	}

	return nil
}
