// Package notify provides a notification infrastructure with support for
// multiple channels (email, in-app) and a condition-based trigger system.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/model"
	"gorm.io/gorm"
)

// Priority levels for notifications.
const (
	PriorityLow    = "low"
	PriorityNormal = "normal"
	PriorityHigh   = "high"
	PriorityUrgent = "urgent"
)

// Channel types.
const (
	ChannelEmail = "email"
	ChannelInApp = "in_app"
)

// Notification represents a single notification to be delivered.
type Notification struct {
	ShopID       uuid.UUID `json:"shop_id"`
	Type         string    `json:"type"`
	Channel      string    `json:"channel"`
	Title        string    `json:"title"`
	Body         string    `json:"body"`
	ActionURL    string    `json:"action_url,omitempty"`
	Priority     string    `json:"priority"`
	ContactEmail string    `json:"contact_email,omitempty"` // resolved email for email channel
}

// Notifier defines the interface for sending notifications.
type Notifier interface {
	// Send delivers a notification. Implementations are responsible for
	// logging errors and should return the error if the send fails.
	Send(ctx context.Context, n *Notification) error
}

// EmailNotifier sends notifications via the Resend email client.
// It wraps the existing emailClient from internal/service/email.go.
type EmailNotifier struct {
	client EmailSender
	db     *gorm.DB
}

// EmailSender abstracts the Resend email client for testability.
type EmailSender interface {
	SendContactNotification(name, email, message string) error    // existing contact form
	SendNotification(to, subject, body string) (string, error)    // notify-specific, returns message ID
}

// NewEmailNotifier creates a new EmailNotifier.
func NewEmailNotifier(client EmailSender, db *gorm.DB) *EmailNotifier {
	return &EmailNotifier{client: client, db: db}
}

// Send sends a notification as an email via Resend.
// Resolves the shop's ContactEmail from the database and passes it as the recipient.
func (e *EmailNotifier) Send(ctx context.Context, n *Notification) error {
	if e.client == nil {
		return fmt.Errorf("email notifier: no email client configured")
	}

	// Resolve recipient email: use Notification.ContactEmail if set,
	// otherwise look up Shop.ContactEmail from DB by ShopID.
	recipient := n.ContactEmail
	if recipient == "" && e.db != nil {
		var shop model.Shop
		if err := e.db.WithContext(ctx).Where("id = ?", n.ShopID).Select("contact_email").First(&shop).Error; err == nil && shop.ContactEmail != "" {
			recipient = shop.ContactEmail
		}
	}

	subject := fmt.Sprintf("[%s] %s", n.Priority, n.Title)
	if n.Priority == PriorityNormal || n.Priority == "" {
		subject = n.Title
	}
	body := n.Body
	if n.ActionURL != "" {
		body = fmt.Sprintf("%s\n\nAction: %s", body, n.ActionURL)
	}
	// Send via Resend. Falls back to configured default if recipient is empty.
	if _, err := e.client.SendNotification(recipient, subject, body); err != nil {
		return fmt.Errorf("email notifier: %w", err)
	}
	slog.Debug("email notification sent",
		"shop_id", n.ShopID,
		"type", n.Type,
		"priority", n.Priority,
		"recipient", recipient,
	)
	return nil
}

// InAppNotifier persists notifications to the in_app_notifications table
// for in-app display to users.
type InAppNotifier struct {
	db *gorm.DB
}

// NewInAppNotifier creates a new InAppNotifier backed by GORM.
func NewInAppNotifier(db *gorm.DB) *InAppNotifier {
	return &InAppNotifier{db: db}
}

// Send persists a notification to the in_app_notifications table.
func (n *InAppNotifier) Send(ctx context.Context, notif *Notification) error {
	if n.db == nil {
		return fmt.Errorf("in-app notifier: no database configured")
	}
	priority := notif.Priority
	if priority == "" {
		priority = PriorityNormal
	}
	record := model.InAppNotification{
		ShopID:    notif.ShopID,
		Type:      notif.Type,
		Channel:   ChannelInApp,
		Title:     notif.Title,
		Body:      notif.Body,
		ActionURL: notif.ActionURL,
		Priority:  priority,
	}
	if err := n.db.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("in-app notifier: create notification: %w", err)
	}
	slog.Debug("in-app notification saved",
		"shop_id", notif.ShopID,
		"type", notif.Type,
		"id", record.ID,
	)
	return nil
}

// Center orchestrates sending notifications through one or more notifiers.
type Center struct {
	notifiers map[string]Notifier
	triggers  []Trigger
	db        *gorm.DB
	mu        sync.RWMutex
}

// NewCenter creates a new notification center.
func NewCenter(db *gorm.DB) *Center {
	return &Center{
		notifiers: make(map[string]Notifier),
		db:        db,
	}
}

// Register adds a notifier for the given channel name.
func (c *Center) Register(channel string, n Notifier) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifiers[channel] = n
}

// RegisterTrigger adds a condition-based trigger to the center.
func (c *Center) RegisterTrigger(t Trigger) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.triggers = append(c.triggers, t)
}

// Check evaluates all triggers registered for the given event and returns
// matching notifications. Safe to call concurrently.
func (c *Center) Check(ctx context.Context, event string, data interface{}) (results []Notification) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, t := range c.triggers {
		if t.Event != event || t.Condition == nil {
			continue
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("notify: trigger panicked", "event", event, "panic", r)
				}
			}()
			ok, notif := t.Condition(ctx, data)
			if ok && notif != nil {
				results = append(results, *notif)
			}
		}()
	}
	return
}

// ShouldSend checks whether the shop's notification preferences allow sending
// a notification of the given type through the given channel.
func (c *Center) ShouldSend(ctx context.Context, shopID uuid.UUID, channel, notifType string) bool {
	if c.db == nil {
		return true // no DB means no preferences — allow all
	}
	var prefs model.NotificationPrefs
	if err := c.db.WithContext(ctx).Where("shop_id = ?", shopID).First(&prefs).Error; err != nil {
		// No preferences set — use defaults (allow all)
		return true
	}
	switch channel {
	case ChannelEmail:
		switch notifType {
		case "return_alert":
			return prefs.EmailReturnUpdates
		case "usage_alert":
			return prefs.EmailUsageAlerts
		case "marketing":
			return prefs.EmailMarketing
		case "review_invitation":
			return prefs.EmailReviewInvites
		default:
			return true // unknown types allowed by default
		}
	case ChannelInApp:
		return prefs.InAppNotifications
	default:
		return true
	}
}

// Send dispatches a notification to the registered notifier for its channel.
// Checks notification preferences before sending.
// Returns an error if no notifier is registered for the channel.
func (c *Center) Send(ctx context.Context, n *Notification) error {
	if n == nil {
		return fmt.Errorf("center: nil notification")
	}
	channel := n.Channel
	if channel == "" {
		channel = ChannelInApp
	}

	// Check notification preferences — skip if opted out
	if !c.ShouldSend(ctx, n.ShopID, channel, n.Type) {
		slog.Debug("notify: notification skipped — opted out",
			"shop_id", n.ShopID,
			"type", n.Type,
			"channel", channel,
		)
		return nil
	}

	notifier, ok := c.notifiers[channel]
	if !ok {
		return fmt.Errorf("center: no notifier registered for channel %q", channel)
	}
	return notifier.Send(ctx, n)
}

// SendMulti dispatches a notification to multiple channels.
func (c *Center) SendMulti(ctx context.Context, n *Notification, channels []string) []error {
	var errs []error
	for _, ch := range channels {
		dup := *n
		dup.Channel = ch
		if err := c.Send(ctx, &dup); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
