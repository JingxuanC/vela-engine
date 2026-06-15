// Package service provides business logic for the Vela AI API.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/notify"
)

// FulfillmentNotifier is an EventBus consumer that sends email + InApp notifications
// when fulfillment status changes occur.
type FulfillmentNotifier struct {
	db           *gorm.DB
	eventBus     eventbus.EventBus
	emailClient  *ResendClient
	notifyCenter *notify.Center
	unsub        func()
}

// NewFulfillmentNotifier creates a new FulfillmentNotifier.
func NewFulfillmentNotifier(db *gorm.DB, eb eventbus.EventBus, emailClient *ResendClient, nc *notify.Center) *FulfillmentNotifier {
	return &FulfillmentNotifier{
		db:           db,
		eventBus:     eb,
		emailClient:  emailClient,
		notifyCenter: nc,
	}
}

// Start subscribes to fulfillment.status_changed events on the EventBus.
func (n *FulfillmentNotifier) Start(ctx context.Context) error {
	unsub, err := n.eventBus.Subscribe(eventbus.EventFulfillmentStatusChanged, n.handleStatusChanged)
	if err != nil {
		return fmt.Errorf("fulfillment_notifier: subscribe: %w", err)
	}
	n.unsub = unsub
	slog.Info("fulfillment_notifier: subscribed to fulfillment.status_changed")
	return nil
}

// Shutdown unsubscribes from the EventBus.
func (n *FulfillmentNotifier) Shutdown() {
	if n.unsub != nil {
		n.unsub()
	}
}

// fulfillmentPayload mirrors the event payload published by FulfillmentTracker.
type fulfillmentPayload struct {
	OrderID           string `json:"order_id"`
	TrackingNumber    string `json:"tracking_number"`
	Carrier           string `json:"carrier"`
	Status            string `json:"status"`
	Message           string `json:"message"`
	City              string `json:"city"`
	Province          string `json:"province"`
	Country           string `json:"country"`
	HappenedAt        string `json:"happened_at"`
	EstimatedDelivery string `json:"estimated_delivery"`
}

// handleStatusChanged processes a fulfillment.status_changed event.
func (n *FulfillmentNotifier) handleStatusChanged(ctx context.Context, event *eventbus.Event) error {
	if event == nil || event.Payload == nil {
		return nil
	}

	var payload fulfillmentPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		slog.Error("fulfillment_notifier: failed to parse payload", "error", err)
		return nil
	}

	status := strings.ToUpper(payload.Status)

	// Build email content based on status
	emailSubject, emailBody := n.buildEmail(status, payload)
	if emailSubject == "" {
		// Unrecognized status — skip
		return nil
	}

	// Find the synced_order by (shop_id, platform_id) to get customer_email
	var order model.SyncedOrder
	if err := n.db.WithContext(ctx).
		Where("shop_id = ? AND platform_id = ?", event.ShopID, payload.OrderID).
		First(&order).Error; err != nil {
		slog.Warn("fulfillment_notifier: order not found", "shop_id", event.ShopID, "platform_id", payload.OrderID, "err", err)
		return nil
	}

	if order.CustomerEmail == "" {
		slog.Debug("fulfillment_notifier: no customer email", "platform_id", payload.OrderID)
		return nil
	}

	// Send email via ResendClient
	if n.emailClient != nil {
		trackingURL := ""
		if order.TrackingURL != "" {
			trackingURL = order.TrackingURL
		}

		htmlBody := n.buildHTMLEmail(emailBody, payload.TrackingNumber, trackingURL, payload.Carrier)
		msgID, err := n.emailClient.SendToCustomer(order.CustomerEmail, emailSubject, htmlBody)
		if err != nil {
			slog.Error("fulfillment_notifier: failed to send email",
				"shop_id", event.ShopID,
				"order_id", payload.OrderID,
				"status", status,
				"error", err,
			)
		} else {
			slog.Info("fulfillment_notifier: email sent",
				"shop_id", event.ShopID,
				"order_id", payload.OrderID,
				"status", status,
				"message_id", msgID,
			)
		}
	}

	// Send InApp notification via NotifyCenter
	if n.notifyCenter != nil {
		notif := &notify.Notification{
			ShopID:   event.ShopID,
			Type:     "fulfillment_update",
			Channel:  notify.ChannelInApp,
			Title:    emailSubject,
			Body:     emailBody,
			Priority: notify.PriorityNormal,
		}
		if err := n.notifyCenter.Send(ctx, notif); err != nil {
			slog.Error("fulfillment_notifier: failed to send in-app notification",
				"shop_id", event.ShopID,
				"order_id", payload.OrderID,
				"error", err,
			)
		}
	}

	return nil
}

// buildEmail returns (subject, body) for the given status. Returns empty strings if the status should be ignored.
func (n *FulfillmentNotifier) buildEmail(status string, p fulfillmentPayload) (subject, body string) {
	tracking := p.TrackingNumber
	city := p.City
	province := p.Province

	switch status {
	case "LABEL_PRINTED":
		subject = fmt.Sprintf("Your order #%s has shipped!", p.OrderID)
		body = fmt.Sprintf("Your order %s has shipped! Track it here.", tracking)
		if tracking == "" {
			body = fmt.Sprintf("Your order #%s has shipped! We'll send tracking details soon.", p.OrderID)
		}

	case "IN_TRANSIT":
		subject = fmt.Sprintf("Your order #%s is on the way!", p.OrderID)
		location := "transit"
		if city != "" {
			location = fmt.Sprintf("%s, %s", city, province)
		}
		body = fmt.Sprintf("Your order is on the way! Current location: %s.", location)

	case "OUT_FOR_DELIVERY":
		subject = fmt.Sprintf("Your order #%s is out for delivery!", p.OrderID)
		body = "Your order is out for delivery today!"
		if p.EstimatedDelivery != "" {
			if eta, err := time.Parse(time.RFC3339, p.EstimatedDelivery); err == nil {
				body += fmt.Sprintf(" Estimated delivery: %s.", eta.Format("Jan 2, 2006"))
			}
		}

	case "DELIVERED":
		subject = fmt.Sprintf("Your order #%s was delivered!", p.OrderID)
		body = "Your order was delivered! Everything ok? Reply here if you need anything."

	case "ATTEMPTED_DELIVERY":
		subject = fmt.Sprintf("Delivery attempt for order #%s", p.OrderID)
		body = "Delivery attempt failed. Please check your phone/address."

	case "FAILURE":
		subject = fmt.Sprintf("Shipping update for order #%s", p.OrderID)
		body = "There's a shipping delay. We're monitoring and will update you."

	default:
		// Unknown status — ignore
		return "", ""
	}

	return subject, body
}

// buildHTMLEmail builds a nice HTML email body.
func (n *FulfillmentNotifier) buildHTMLEmail(body, trackingNumber, trackingURL, carrier string) string {
	trackingLine := ""
	if trackingNumber != "" {
		trackingLine = fmt.Sprintf("<p><strong>Tracking Number:</strong> %s", trackingNumber)
		if carrier != "" {
			trackingLine += fmt.Sprintf(" via %s", carrier)
		}
		if trackingURL != "" {
			trackingLine += fmt.Sprintf(`<br><a href="%s" style="color:#3b82f6;">Track your package →</a>`, trackingURL)
		}
		trackingLine += "</p>"
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;max-width:600px;margin:0 auto;padding:20px;">
<div style="background:#f8fafc;border-radius:12px;padding:24px;border:1px solid #e2e8f0;">
<h2 style="color:#1e293b;margin-top:0;">📦 Shipping Update</h2>
<p style="color:#334155;font-size:16px;">%s</p>
%s
<p style="color:#64748b;font-size:13px;margin-top:24px;">Sent by Vela AI — your AI shopping assistant</p>
</div>
</body>
</html>`, body, trackingLine)
}
