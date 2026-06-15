// Package review provides AI-powered review analysis, reply generation,
// and the AutoReplyService (EventBus consumer).
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// AdapterFactory creates a ReviewAdapter for a shop on a given platform.
type AdapterFactory func(ctx context.Context, platform string, shopID string) ReviewAdapter

// AutoReplyService is the EventBus consumer that processes review.synced events.
// It analyzes reviews, generates AI replies, posts them via the platform adapter,
// and handles retries.
type AutoReplyService struct {
	db             *gorm.DB
	eventBus       eventbus.EventBus
	adapterFactory AdapterFactory // creates per-shop adapters on demand
	unsub          func()         // EventBus unsubscribe callback
}

// NewAutoReplyService creates a new AutoReplyService.
func NewAutoReplyService(db *gorm.DB, eb eventbus.EventBus) *AutoReplyService {
	return &AutoReplyService{
		db:       db,
		eventBus: eb,
	}
}

// Shutdown unsubscribes from EventBus. Safe to call multiple times.
func (s *AutoReplyService) Shutdown() {
	if s.unsub != nil {
		s.unsub()
	}
}

// SetAdapterFactory sets the adapter factory for on-demand adapter creation.
func (s *AutoReplyService) SetAdapterFactory(f AdapterFactory) {
	s.adapterFactory = f
}

// getAdapter returns an adapter for the given platform and shop.
func (s *AutoReplyService) getAdapter(ctx context.Context, platform string, shopID string) ReviewAdapter {
	if s.adapterFactory != nil {
		return s.adapterFactory(ctx, platform, shopID)
	}
	return nil
}

// Start subscribes to EventBus and starts consuming review.synced events.
func (s *AutoReplyService) Start(ctx context.Context) error {
	unsub, err := s.eventBus.Subscribe(eventbus.EventReviewSynced, s.handleReviewSynced)
	if err != nil {
		return fmt.Errorf("auto_reply: subscribe review.synced: %w", err)
	}
	s.unsub = unsub
	slog.Info("auto_reply: subscribed to review.synced")
	return nil
}

// handleReviewSynced is the EventBus handler for review.synced events.
func (s *AutoReplyService) handleReviewSynced(ctx context.Context, event *eventbus.Event) error {
	if event == nil || event.Payload == nil {
		return nil
	}

	var payload struct {
		Platform      string  `json:"platform"`
		PlatformID    string  `json:"platform_id"`
		ProductID     string  `json:"product_id"`
		Rating        float64 `json:"rating"`
		Title         string  `json:"title"`
		Body          string  `json:"body"`
		ReviewerName  string  `json:"reviewer_name"`
		ReviewerEmail string  `json:"reviewer_email"`
		HasReply      bool    `json:"has_reply"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Error("auto_reply: failed to parse review.synced payload", "error", err)
		return nil
	}

	// Skip if already has a reply or is positive (rating >= 4)
	if payload.HasReply {
		slog.Debug("auto_reply: skipping — review already has reply", "platform_id", payload.PlatformID)
		return nil
	}
	if payload.Rating >= 4 {
		slog.Debug("auto_reply: skipping — positive review", "platform_id", payload.PlatformID, "rating", payload.Rating)
		return nil
	}

	slog.Info("auto_reply: processing review", "platform", payload.Platform, "platform_id", payload.PlatformID, "rating", payload.Rating)

	// Build ReviewInput from payload (no extra API call needed)
	input := ReviewInput{
		ID:     payload.PlatformID,
		Author: payload.ReviewerName,
		Rating: payload.Rating,
		Title:  payload.Title,
		Body:   payload.Body,
	}

	// Load config — check AutoReplyConfig first, fall back to legacy ReviewAutoReplySetting
	var enabled bool
	var sendMode string

	var config model.AutoReplyConfig
	if err := s.db.WithContext(ctx).Where("shop_id = ?", event.ShopID).First(&config).Error; err == nil {
		enabled = config.Enabled
		sendMode = config.Mode
	} else if err != gorm.ErrRecordNotFound {
		slog.Error("auto_reply: failed to load config", "shop_id", event.ShopID, "error", err)
		return nil
	}

	// Fallback: legacy ReviewAutoReplySetting (from JudgeMe connection)
	if !enabled {
		var legacy model.ReviewAutoReplySetting
		if err := s.db.WithContext(ctx).Where("shop_id = ?", event.ShopID).First(&legacy).Error; err == nil {
			enabled = true // connected JudgeMe implies enabled
			if legacy.RequireApproval {
				sendMode = "manual"
			} else {
				sendMode = "auto"
			}
		}
	}

	if !enabled {
		slog.Debug("auto_reply: skipping — no config/settings for shop", "shop_id", event.ShopID)
		return nil
	}
	if sendMode == "" {
		sendMode = "manual"
	}

	// Check for existing ReplyRecord (idempotency)
	var existing model.ReplyRecord
	if err := s.db.WithContext(ctx).Where("shop_id = ? AND platform_id = ?", event.ShopID, payload.PlatformID).First(&existing).Error; err == nil {
		slog.Debug("auto_reply: skipping — ReplyRecord already exists", "platform_id", payload.PlatformID, "status", existing.Status)
		return nil
	}

	// Analyze the review (uses payload data directly — no extra API call)
	analysis := AnalyzeBadReview(input)

	// Generate AI replies (using templates — LLM mode can be toggled later)
	productName := payload.ProductID
	replies := GenerateReplies(analysis, productName)
	repliesJSON, _ := json.Marshal(replies)

	// sendMode already resolved above (AutoReplyConfig or legacy setting)

	// Create ReplyRecord
	record := model.ReplyRecord{
		ShopID:         event.ShopID,
		PlatformID:     payload.PlatformID,
		ProductID:      payload.ProductID,
		Rating:         payload.Rating,
		ReviewTitle:    payload.Title,
		ReviewBody:     payload.Body,
		AiReplies:      repliesJSON,
		FinalReply:     replies[0], // default to first candidate
		SentimentScore: analysis.SeverityScore,
		SendMode:       sendMode,
		Status:         "pending",
	}

	if err := s.db.WithContext(ctx).Create(&record).Error; err != nil {
		slog.Error("auto_reply: failed to create ReplyRecord", "error", err)
		return nil
	}

	s.logEvent(ctx, event.ShopID, payload.PlatformID, record.ID, "generate", "success", "reply record created", 0)

	// If auto-send mode, create adapter on-demand and post reply immediately
	if sendMode == "auto" {
		adapter := s.getAdapter(ctx, payload.Platform, event.ShopID.String())
		if adapter != nil {
			s.postReplyWithRetry(ctx, adapter, &record)
		} else {
			slog.Warn("auto_reply: no adapter available for platform", "platform", payload.Platform, "shop_id", event.ShopID)
		}
	}

	return nil
}

// PostReply resolves the platform adapter and posts a reply. Used by the manual approval flow.
func (s *AutoReplyService) PostReply(ctx context.Context, platform, shopID, reviewID, content string) error {
	adapter := s.getAdapter(ctx, platform, shopID)
	if adapter == nil {
		return fmt.Errorf("no adapter registered for platform %s", platform)
	}
	return adapter.PostReply(ctx, reviewID, content)
}

// postReplyWithRetry attempts to post a reply with up to 3 retries.
func (s *AutoReplyService) postReplyWithRetry(ctx context.Context, adapter ReviewAdapter, record *model.ReplyRecord) {
	maxRetries := 3
	backoffs := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute}

	// Atomic state transition: pending → processing
	updated := s.db.WithContext(ctx).Model(&model.ReplyRecord{}).
		Where("id = ? AND status = ?", record.ID, "pending").
		Update("status", "processing").RowsAffected
	if updated == 0 {
		slog.Info("auto_reply: skip posting — status no longer pending", "record_id", record.ID, "status", record.Status)
		return
	}
	record.Status = "processing"

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := adapter.PostReply(ctx, record.PlatformID, record.FinalReply)
		if err == nil {
			// Success
			now := time.Now().UTC()
			if dbErr := s.db.WithContext(ctx).Model(record).Where("id = ?", record.ID).Updates(map[string]interface{}{
				"status":  "sent",
				"sent_at": &now,
			}).Error; dbErr != nil {
				slog.Error("auto_reply: failed to update status to sent", "record_id", record.ID, "error", dbErr)
			}
			s.logEvent(ctx, record.ShopID, record.PlatformID, record.ID, "post_reply", "success", "reply posted successfully", record.RetryCount)

			// Publish review.replied event
			s.publishReviewReplied(ctx, record.ShopID, record.PlatformID, record.FinalReply)
			return
		}

		lastErr = err
		record.RetryCount = attempt + 1

		// Handle non-retryable errors
		if isNonRetryableError(err) {
			// 422 already replied → mark as sent (not failed)
			if isAlreadyRepliedError(err) {
				now := time.Now().UTC()
				if dbErr := s.db.WithContext(ctx).Model(record).Where("id = ?", record.ID).Updates(map[string]interface{}{
					"status":  "sent",
					"sent_at": &now,
				}).Error; dbErr != nil {
					slog.Error("auto_reply: failed to update status to sent", "record_id", record.ID, "error", dbErr)
				}
				s.logEvent(ctx, record.ShopID, record.PlatformID, record.ID, "post_reply", "success", "already replied on platform", record.RetryCount)
				s.publishReviewReplied(ctx, record.ShopID, record.PlatformID, record.FinalReply)
				return
			}
			slog.Warn("auto_reply: non-retryable post error", "record_id", record.ID, "error", err)
			break
		}

		slog.Warn("auto_reply: post reply failed, retrying",
			"record_id", record.ID,
			"attempt", attempt+1,
			"error", err,
		)
		s.logEvent(ctx, record.ShopID, record.PlatformID, record.ID, "retry", "failed", err.Error(), attempt+1)

		// Wait before retry
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoffs[attempt]):
		}
	}

	// All retries exhausted
	now := time.Now().UTC()
	if dbErr := s.db.WithContext(ctx).Model(record).Where("id = ?", record.ID).Updates(map[string]interface{}{
		"status":      "failed",
		"failed_at":   &now,
		"fail_reason": lastErr.Error(),
		"retry_count": record.RetryCount,
	}).Error; dbErr != nil {
		slog.Error("auto_reply: failed to update status to failed", "record_id", record.ID, "error", dbErr)
	}
	s.logEvent(ctx, record.ShopID, record.PlatformID, record.ID, "fail", "failed", lastErr.Error(), record.RetryCount)
	slog.Error("auto_reply: post reply failed after max retries",
		"record_id", record.ID,
		"retry_count", record.RetryCount,
		"error", lastErr,
	)
}

// isNonRetryableError checks if an error should not be retried.
func isNonRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "401") {
		return true
	}
	if isAlreadyRepliedError(err) {
		return true
	}
	return false
}

// isAlreadyRepliedError checks if the error indicates the reply already exists on the platform.
func isAlreadyRepliedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "422") ||
		strings.Contains(msg, "already") ||
		strings.Contains(msg, "duplicate")
}

// publishReviewReplied publishes a review.replied event.
func (s *AutoReplyService) publishReviewReplied(ctx context.Context, shopID uuid.UUID, platformID, finalReply string) {
	if s.eventBus == nil {
		return
	}
	payload := map[string]interface{}{
		"platform_id": platformID,
		"final_reply": finalReply,
	}
	event, err := eventbus.NewEvent(eventbus.EventReviewReplied, shopID, payload, "auto_reply")
	if err != nil {
		return
	}
	s.eventBus.Publish(ctx, event)
}

// logEvent writes an AutoReplyLog entry.
func (s *AutoReplyService) logEvent(ctx context.Context, shopID uuid.UUID, platformID string, replyRecordID uuid.UUID, action, status, message string, retryCount int) {
	if s.db == nil {
		return
	}
	log := model.AutoReplyLog{
		ShopID:        shopID,
		PlatformID:    platformID,
		ReplyRecordID: replyRecordID,
		Action:        action,
		Status:        status,
		Message:       message,
		RetryCount:    retryCount,
	}
	if err := s.db.WithContext(ctx).Create(&log).Error; err != nil {
		slog.Error("auto_reply: failed to write log", "error", err)
	}
}
