package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/notify"
	"github.com/JingxuanC/vela-engine/internal/platform/taskqueue"
)

// ReviewInviter listens for order.fulfilled events and creates review invitation tasks.
type ReviewInviter struct {
	db           *gorm.DB
	emailClient  *ResendClient
	notifyCenter *notify.Center
	taskClient   *taskqueue.AsynqClient
	eventBus     eventbus.EventBus
}

// NewReviewInviter creates a new ReviewInviter service.
func NewReviewInviter(db *gorm.DB, emailClient *ResendClient, notifyCenter *notify.Center) *ReviewInviter {
	return &ReviewInviter{
		db:           db,
		emailClient:  emailClient,
		notifyCenter: notifyCenter,
	}
}

// SetTaskClient injects the Asynq task client for enqueuing delayed review invitations.
func (r *ReviewInviter) SetTaskClient(tc *taskqueue.AsynqClient) {
	r.taskClient = tc
}

// SetEventBus injects the EventBus for publishing review.invitation_* events.
func (r *ReviewInviter) SetEventBus(eb eventbus.EventBus) {
	r.eventBus = eb
}

// ReviewInvitationPayload is the event payload for order.fulfilled events.
type ReviewInvitationPayload struct {
	OrderID       int64  `json:"order_id"`
	ProductID     int64  `json:"product_id"`
	ProductTitle  string `json:"product_title"`
	ProductImage  string `json:"product_image"`
	CustomerEmail string `json:"customer_email"`
	CustomerName  string `json:"customer_name"`
}

// HandleEvent implements eventbus.EventHandler. It processes order.fulfilled events.
func (r *ReviewInviter) HandleEvent(ctx context.Context, event *eventbus.Event) error {
	if event.Type != eventbus.EventOrderFulfilled {
		return nil
	}

	var payload ReviewInvitationPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Error("review_inviter: failed to parse event payload", "event_id", event.ID, "error", err)
		return nil // non-retryable
	}

	if payload.CustomerEmail == "" || payload.OrderID == 0 || payload.ProductID == 0 {
		slog.Warn("review_inviter: incomplete payload, skipping", "event_id", event.ID)
		return nil
	}

	// Check notification preferences
	if !r.shouldSend(ctx, event.ShopID) {
		slog.Info("review_inviter: EmailReviewInvites disabled for shop", "shop_id", event.ShopID)
		return nil
	}

	// Calculate delay: default 5 days
	delayDays := 5
	sendAt := time.Now().UTC().Add(time.Duration(delayDays) * 24 * time.Hour)

	// Create review_invitations record
	invitation := model.ReviewInvitation{
		ShopID:        event.ShopID,
		OrderID:       payload.OrderID,
		ProductID:     payload.ProductID,
		CustomerEmail: payload.CustomerEmail,
		CustomerName:  payload.CustomerName,
		ProductTitle:  payload.ProductTitle,
		ProductImage:  payload.ProductImage,
		Status:        "pending",
		SendAt:        &sendAt,
	}

	if err := r.db.WithContext(ctx).Create(&invitation).Error; err != nil {
		slog.Error("review_inviter: failed to create invitation", "error", err)
		return fmt.Errorf("create review_invitation: %w", err)
	}

	slog.Info("review_inviter: invitation created",
		"invitation_id", invitation.ID,
		"order_id", payload.OrderID,
		"send_at", sendAt.Format(time.RFC3339),
	)

	// Enqueue Asynq delayed task
	if r.taskClient != nil {
		task := &taskqueue.Task{
			Type: taskqueue.TypeReviewInvitation,
			Payload: taskqueue.ReviewInvitationPayload{
				ReviewInvitationID: invitation.ID.String(),
			},
		}
		if _, err := r.taskClient.EnqueueAt(ctx, task, sendAt); err != nil {
			slog.Error("review_inviter: failed to enqueue task", "invitation_id", invitation.ID, "error", err)
			return fmt.Errorf("enqueue review invitation task: %w", err)
		}
		slog.Info("review_inviter: task enqueued", "invitation_id", invitation.ID, "process_at", sendAt.Format(time.RFC3339))
	}

	return nil
}

// shouldSend checks notification preferences for review_invitation.
func (r *ReviewInviter) shouldSend(ctx context.Context, shopID uuid.UUID) bool {
	if r.notifyCenter != nil {
		return r.notifyCenter.ShouldSend(ctx, shopID, notify.ChannelEmail, "review_invitation")
	}
	// No NotifyCenter — check DB directly
	var prefs model.NotificationPrefs
	if err := r.db.WithContext(ctx).Where("shop_id = ?", shopID).First(&prefs).Error; err != nil {
		return true // no prefs set, default to true
	}
	return prefs.EmailReviewInvites
}

// ExecuteTask handles the Asynq task to actually send the review invitation email.
// This is the handler registered on the Asynq mux for TypeReviewInvitation.
func (r *ReviewInviter) ExecuteTask(ctx context.Context, invitationID string) error {
	id, err := uuid.Parse(invitationID)
	if err != nil {
		return fmt.Errorf("invalid invitation ID: %w", err)
	}

	var inv model.ReviewInvitation
	if err := r.db.WithContext(ctx).Where("id = ? AND status = ?", id, "pending").First(&inv).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			slog.Warn("review_inviter: invitation not found or already processed", "id", invitationID)
			return nil
		}
		return fmt.Errorf("query review_invitation: %w", err)
	}

	// Re-check notification preferences (user may have disabled during the delay)
	if !r.shouldSend(ctx, inv.ShopID) {
		slog.Info("review_inviter: preferences disabled during delay, skipping", "invitation_id", invitationID)
		return nil
	}

	// Build email content
	// Get shop domain + product handle for the review link
	shopDomain := "myshopify.com"
	var shop model.Shop
	if err := r.db.WithContext(ctx).Where("id = ?", inv.ShopID).First(&shop).Error; err == nil && shop.ShopDomain != "" {
		shopDomain = shop.ShopDomain
	}

	// Resolve product handle from synced_products for the review URL
	productSlug := fmt.Sprintf("%d", inv.ProductID) // fallback to numeric ID
	var synced model.SyncedProduct
	if err := r.db.WithContext(ctx).Where("shop_id = ? AND platform_id = ?", inv.ShopID, fmt.Sprintf("%d", inv.ProductID)).First(&synced).Error; err == nil && synced.Handle != "" {
		productSlug = synced.Handle
	}

	subject := fmt.Sprintf("喜欢 %s 吗？分享你的体验 ✨", inv.ProductTitle)
	htmlBody := fmt.Sprintf(`<div style="max-width:600px;margin:0 auto;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif">
	<h2>你的体验很重要！</h2>
	<p>Hi%s，你最近购买了 <strong>%s</strong>，使用感受如何？</p>
	%s
	<p style="margin-top:24px;">
		<a href="https://%s/products/%s?review=1#write-review" style="display:inline-block;padding:12px 24px;background:#5c6ac4;color:#fff;text-decoration:none;border-radius:8px;font-weight:bold;">写评价</a>
	</p>
	<p style="color:#888;font-size:12px;margin-top:24px;">此邮件由 Vela AI 自动发送。如果你不想再收到评价邀请，请在通知偏好中关闭。</p>
</div>`, formatCustomerGreeting(inv.CustomerName), inv.ProductTitle, formatProductImage(inv.ProductImage), shopDomain, productSlug)

	// Send email
	if r.emailClient == nil {
		slog.Warn("review_inviter: email client not configured, skipping", "invitation_id", invitationID)
		return nil
	}

	_, err = r.emailClient.SendToCustomer(inv.CustomerEmail, subject, htmlBody)
	if err != nil {
		slog.Error("review_inviter: failed to send email", "invitation_id", invitationID, "error", err)
		return fmt.Errorf("send review invitation email: %w", err)
	}

	// Update status
	now := time.Now().UTC()
	if err := r.db.WithContext(ctx).Model(&inv).Updates(map[string]interface{}{
		"status":  "sent",
		"sent_at": now,
	}).Error; err != nil {
		slog.Error("review_inviter: failed to update status", "invitation_id", invitationID, "error", err)
		return fmt.Errorf("update review_invitation status: %w", err)
	}

	slog.Info("review_inviter: invitation sent", "invitation_id", invitationID, "customer", inv.CustomerEmail)

	// Publish review.invitation_sent event for downstream consumers (analytics, attribution)
	r.publishEvent(ctx, eventbus.EventReviewInvitationSent, inv)

	return nil
}

// publishEvent wraps a best-effort event publish (non-fatal if EventBus is unavailable).
func (r *ReviewInviter) publishEvent(ctx context.Context, evType eventbus.EventType, inv model.ReviewInvitation) {
	if r.eventBus == nil {
		return
	}
	payload := map[string]interface{}{
		"invitation_id":  inv.ID.String(),
		"order_id":       inv.OrderID,
		"product_id":     inv.ProductID,
		"customer_email": inv.CustomerEmail,
		"status":         inv.Status,
	}
	event, err := eventbus.NewEvent(evType, inv.ShopID, payload, "review_inviter")
	if err != nil {
		slog.Error("review_inviter: failed to create event", "type", string(evType), "error", err)
		return
	}
	if err := r.eventBus.Publish(ctx, event); err != nil {
		slog.Warn("review_inviter: failed to publish event", "type", string(evType), "error", err)
	}
}

func formatCustomerGreeting(name string) string {
	if name == "" {
		return ""
	}
	return " " + name + "，"
}

func formatProductImage(imageURL string) string {
	if imageURL == "" {
		return ""
	}
	return fmt.Sprintf(`<img src="%s" alt="Product" style="max-width:200px;border-radius:8px;margin:16px 0;" />`, imageURL)
}
