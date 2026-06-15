package service

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// MarketingFlowEngine executes marketing automation flows.
type MarketingFlowEngine struct {
	db      *gorm.DB
	email   *ResendClient
	rfm     *RFMEngine
	eventBus eventbus.EventBus
}

// NewMarketingFlowEngine creates a new marketing flow engine.
func NewMarketingFlowEngine(db *gorm.DB, email *ResendClient, rfm *RFMEngine, eb eventbus.EventBus) *MarketingFlowEngine {
	return &MarketingFlowEngine{db: db, email: email, rfm: rfm, eventBus: eb}
}

// ── Trigger Types ────────────────────────────────────────────────────────────

const (
	TriggerOrderCreated       = "order.created"
	TriggerCheckoutAbandoned  = "checkout.abandoned"
	TriggerOrderFulfilled     = "order.fulfilled"
	TriggerCustomerDormant    = "customer.dormant"
	TriggerFulfillmentStatus  = "fulfillment.status_changed"
)

// Template keys
const (
	TemplateWelcome         = "welcome"
	TemplateAbandonedCart   = "abandoned_cart"
	TemplateRepurchase      = "repurchase"
	TemplateDormantWakeup   = "dormant_wakeup"
	TemplateLogisticsCare   = "logistics_care"
)

// FlowTriggerPayload is the data passed from EventBus to trigger a flow.
type FlowTriggerPayload struct {
	TriggerType     string            `json:"trigger_type"`
	TriggerEventID  string            `json:"trigger_event_id,omitempty"` // dedup key
	CustomerEmail   string            `json:"customer_email"`
	CustomerName    string            `json:"customer_name,omitempty"`
	ShopID          string            `json:"shop_id"`
	EntityID        string            `json:"entity_id,omitempty"`
	ShopName        string            `json:"shop_name,omitempty"`
	FulfillmentStatus string          `json:"fulfillment_status,omitempty"`
	ProductName     string            `json:"product_name,omitempty"`
	Meta            map[string]string `json:"meta,omitempty"`
}

// ── Flow Template Registry ──────────────────────────────────────────────────

// flowTemplate defines a pre-built automation flow.
type flowTemplate struct {
	Name        string                   `json:"name"`
	TriggerType string                   `json:"trigger_type"`
	TemplateKey string                   `json:"template_key"`
	Conditions  []map[string]interface{} `json:"conditions"`
	Actions     []map[string]interface{} `json:"actions"`
}

// GetTemplates returns all 5 pre-built flow templates.
func GetTemplates(shopName string) []flowTemplate {
	return []flowTemplate{
		{
			Name: "Welcome 系列（新客欢迎）", TriggerType: TriggerOrderCreated, TemplateKey: TemplateWelcome,
			Conditions: []map[string]interface{}{},
			Actions: []map[string]interface{}{
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    fmt.Sprintf("Welcome to %s! 🎉 10%% off your first order", shopName),
						"body":       fmt.Sprintf(`<h2>Welcome to %s!</h2><p>Thanks for your first order! Here's a special 10%% off code for your next purchase: <strong>WELCOME10</strong></p><p>Enjoy shopping! 🛍️</p>`, shopName),
						"delay_hours": 0,
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    fmt.Sprintf("Here's what others are loving at %s", shopName),
						"body":       fmt.Sprintf(`<h2>Trending at %s</h2><p>Check out our top 3 products that everyone is raving about!</p><p>👉 <a href="https://%s">Shop Now</a></p>`, shopName, shopName),
						"delay_hours": 72, // Day 3
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "Still thinking? Last chance for your 10% off!",
						"body":       fmt.Sprintf(`<h2>Don't miss out!</h2><p>Your 10%% welcome discount is expiring soon. Use code <strong>WELCOME10</strong> today!</p>`, ),
						"delay_hours": 168, // Day 7
					},
				},
			},
		},
		{
			Name: "弃单挽回", TriggerType: TriggerCheckoutAbandoned, TemplateKey: TemplateAbandonedCart,
			Conditions: []map[string]interface{}{},
			Actions: []map[string]interface{}{
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "You left something behind... 👀",
						"body":       `<h2>Your cart is waiting!</h2><p>We saved your cart for you. Come back and complete your order!</p><p>👉 <a href="#">View Your Cart</a></p>`,
						"delay_hours": 1,
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "Still interested? Free shipping on this order! 📦",
						"body":       `<h2>Free shipping just for you!</h2><p>Use code <strong>FREESHIP</strong> to get free shipping on your order. Don't wait too long!</p>`,
						"delay_hours": 24,
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "Last chance — price may change soon ⏰",
						"body":       `<h2>Prices may go up!</h2><p>This is your final reminder — items in your cart may change in price soon. Checkout now!</p>`,
						"delay_hours": 72,
					},
				},
			},
		},
		{
			Name: "复购提醒", TriggerType: TriggerOrderFulfilled, TemplateKey: TemplateRepurchase,
			Conditions: []map[string]interface{}{},
			Actions: []map[string]interface{}{
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "Running low? Reorder with 1 click! 🔄",
						"body":       `<h2>Time to restock?</h2><p>It's been a while since your last order. Reorder your favorites with just one click!</p><p>👉 <a href="#">Reorder Now</a></p>`,
						"delay_hours": 336, // Day 14
					},
				},
			},
		},
		{
			Name: "沉睡唤醒", TriggerType: TriggerCustomerDormant, TemplateKey: TemplateDormantWakeup,
			Conditions: []map[string]interface{}{},
			Actions: []map[string]interface{}{
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "We miss you! Here's 15% off 💛",
						"body":       `<h2>We've missed you!</h2><p>Come back and enjoy 15% off your next purchase with code <strong>COMEBACK15</strong></p><p>New arrivals are waiting for you!</p>`,
						"delay_hours": 0,
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":    "New arrivals since you left ✨",
						"body":       `<h2>See what's new!</h2><p>We've added some amazing new products since your last visit. Check them out!</p><p>👉 <a href="#">Browse New Arrivals</a></p>`,
						"delay_hours": 720, // Day 30
					},
				},
			},
		},
		{
			Name: "物流关怀", TriggerType: TriggerFulfillmentStatus, TemplateKey: TemplateLogisticsCare,
			Conditions: []map[string]interface{}{},
			Actions: []map[string]interface{}{
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":         "Your order is on the way! 🚚",
						"body":            "<h2>Good news!</h2><p>Your order has been shipped and is on its way to you. Track your package below.</p>",
						"status_trigger":  "SHIPPED",
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":         "Arriving today! 📬",
						"body":            "<h2>Your package is out for delivery!</h2><p>Get ready — your order arrives today!</p>",
						"status_trigger":  "OUT_FOR_DELIVERY",
					},
				},
				{
					"type": "send_email",
					"config": map[string]interface{}{
						"subject":         "Enjoying your purchase? We'd love your feedback ⭐",
						"body":            "<h2>How's everything?</h2><p>We hope you're loving your purchase! Would you mind leaving us a review?</p><p>👉 <a href=\"#\">Leave a Review</a></p>",
						"status_trigger":  "DELIVERED",
					},
				},
			},
		},
	}
}

// ── Flow Execution ─────────────────────────────────────────────────────────

// TriggerFlow finds matching enabled flows for a trigger event and executes them.
func (e *MarketingFlowEngine) TriggerFlow(ctx context.Context, triggerType string, payload FlowTriggerPayload) error {
	shopID, err := uuid.Parse(payload.ShopID)
	if err != nil {
		return fmt.Errorf("marketing_flow: invalid shop_id: %w", err)
	}

	// Find enabled flows matching this trigger type
	var flows []model.MarketingFlow
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND trigger_type = ? AND enabled = true", shopID, triggerType).
		Find(&flows).Error; err != nil {
		return fmt.Errorf("marketing_flow: query flows: %w", err)
	}

	if len(flows) == 0 {
		slog.Debug("marketing_flow: no enabled flows for trigger", "trigger", triggerType, "shop_id", shopID)
		return nil
	}

	for _, flow := range flows {
		if err := e.executeFlow(ctx, &flow, payload); err != nil {
			slog.Error("marketing_flow: flow execution failed",
				"flow_id", flow.ID, "flow_name", flow.Name, "error", err)
			// Continue with other flows
		}
	}

	return nil
}

// executeFlow checks conditions and executes actions for a single flow.
func (e *MarketingFlowEngine) executeFlow(ctx context.Context, flow *model.MarketingFlow, payload FlowTriggerPayload) error {
	// Check conditions
	if !e.checkConditions(ctx, flow, payload) {
		e.recordRun(ctx, flow, payload, "skipped", 0, "")
		return nil
	}

	// Execute actions
	var conditions []map[string]interface{}
	json.Unmarshal(flow.Conditions, &conditions)

	var actions []map[string]interface{}
	json.Unmarshal(flow.Actions, &actions)

	for i, action := range actions {
		actionType, _ := action["type"].(string)
		config, _ := action["config"].(map[string]interface{})

		switch actionType {
		case "send_email":
			if err := e.executeEmailAction(ctx, flow, payload, config, i); err != nil {
				e.recordRun(ctx, flow, payload, "failed", i, err.Error())
				return err
			}
		default:
			slog.Warn("marketing_flow: unknown action type", "type", actionType, "flow_id", flow.ID)
		}
	}

	e.recordRun(ctx, flow, payload, "sent", len(actions)-1, "")
	return nil
}

// executeEmailAction sends an email via Resend and records the run.
func (e *MarketingFlowEngine) executeEmailAction(ctx context.Context, flow *model.MarketingFlow, payload FlowTriggerPayload, config map[string]interface{}, actionIndex int) error {
	if e.email == nil {
		return fmt.Errorf("email client not configured")
	}
	if payload.CustomerEmail == "" {
		return fmt.Errorf("no customer email in payload")
	}

	// Dedup: skip if this flow+customer+event was already sent in the last 24h
	if payload.TriggerEventID != "" {
		var existing model.MarketingFlowRun
		if err := e.db.WithContext(ctx).
			Where("flow_id = ? AND customer_email = ? AND trigger_event_id = ? AND status = ?",
				flow.ID, payload.CustomerEmail, payload.TriggerEventID, "sent").
			Where("executed_at > NOW() - INTERVAL '24 hours'").
			First(&existing).Error; err == nil {
			slog.Debug("marketing_flow: skipping duplicate trigger", "flow", flow.Name, "customer", payload.CustomerEmail, "event", payload.TriggerEventID)
			return nil
		}
	}

	subject, _ := config["subject"].(string)
	body, _ := config["body"].(string)

	// For logistics: check status_trigger matches
	if statusTrigger, ok := config["status_trigger"].(string); ok && statusTrigger != "" {
		if !strings.EqualFold(payload.FulfillmentStatus, statusTrigger) {
			// Skip this action — status doesn't match
			return nil
		}
	}

	shopName := html.EscapeString(payload.ShopName)
	if shopName == "" {
		shopName = "our store"
	}

	// Replace placeholders in body
	body = strings.ReplaceAll(body, "{shop_name}", shopName)

	htmlBody := fmt.Sprintf(`<html><body style="font-family:Arial,sans-serif;padding:20px;max-width:600px;margin:0 auto">
%s
<hr style="margin-top:30px;border:0;border-top:1px solid #eee">
<p style="color:#888;font-size:12px">Sent by Vela AI Marketing Automation — %s</p>
</body></html>`, body, shopName)

	msgID, err := e.email.SendToCustomer(payload.CustomerEmail, subject, htmlBody)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	// Store the Resend message ID
	shopID, _ := uuid.Parse(payload.ShopID)
	run := model.MarketingFlowRun{
		FlowID:          flow.ID,
		ShopID:          shopID,
		CustomerEmail:   payload.CustomerEmail,
		CustomerName:    payload.CustomerName,
		TriggerEventID:  payload.EntityID,
		Status:          "sent",
		ResendMessageID: msgID,
		ActionIndex:     actionIndex,
	}
	now := time.Now()
	run.ExecutedAt = &now

	if err := e.db.WithContext(ctx).Create(&run).Error; err != nil {
		slog.Error("marketing_flow: failed to record run", "error", err)
	}

	slog.Info("marketing_flow: email sent",
		"flow", flow.Name,
		"to", payload.CustomerEmail,
		"resend_msg_id", msgID,
	)
	return nil
}

// checkConditions evaluates whether the flow conditions are met for the given payload.
func (e *MarketingFlowEngine) checkConditions(ctx context.Context, flow *model.MarketingFlow, payload FlowTriggerPayload) bool {
	var conditions []map[string]interface{}
	if err := json.Unmarshal(flow.Conditions, &conditions); err != nil || len(conditions) == 0 {
		return true // No conditions = always true
	}

	for _, cond := range conditions {
		field, _ := cond["field"].(string)
		op, _ := cond["op"].(string)
		value, _ := cond["value"]

		if !e.evaluateCondition(ctx, field, op, value, payload) {
			return false
		}
	}
	return true
}

// evaluateCondition checks a single condition against the payload.
func (e *MarketingFlowEngine) evaluateCondition(ctx context.Context, field, op string, value interface{}, payload FlowTriggerPayload) bool {
	switch field {
	case "order_amount":
		// Checked via meta
		if amt, ok := payload.Meta["order_amount"]; ok {
			return evaluateNumOp(amt, op, value)
		}
		return false
	case "fulfillment_status":
		if op == "eq" {
			return strings.EqualFold(payload.FulfillmentStatus, fmt.Sprint(value))
		}
		return false
	case "customer_tier":
		// Check if customer is in the specified RFM segment
		if e.rfm != nil && payload.CustomerEmail != "" {
			shopID, _ := uuid.Parse(payload.ShopID)
			customers, err := e.rfm.GetSegmentCustomers(ctx, shopID, fmt.Sprint(value))
			if err == nil {
				for _, c := range customers {
					if strings.EqualFold(c.Email, payload.CustomerEmail) {
						return true
					}
				}
			}
		}
		return false
	default:
		return false // unknown field — fail closed
}
}

func evaluateNumOp(fieldValue string, op string, expected interface{}) bool {
	var fv, ev float64
	fmt.Sscanf(fieldValue, "%f", &fv)
	switch v := expected.(type) {
	case float64:
		ev = v
	case string:
		fmt.Sscanf(v, "%f", &ev)
	default:
		return false
	}

	switch op {
	case "gt":
		return fv > ev
	case "gte":
		return fv >= ev
	case "lt":
		return fv < ev
	case "lte":
		return fv <= ev
	case "eq":
		return fv == ev
	default:
		return false
	}
}

// recordRun logs a flow execution run.
func (e *MarketingFlowEngine) recordRun(ctx context.Context, flow *model.MarketingFlow, payload FlowTriggerPayload, status string, actionIndex int, errMsg string) {
	shopID, _ := uuid.Parse(payload.ShopID)
	run := model.MarketingFlowRun{
		FlowID:         flow.ID,
		ShopID:         shopID,
		CustomerEmail:  payload.CustomerEmail,
		CustomerName:   payload.CustomerName,
		TriggerEventID: payload.EntityID,
		Status:         status,
		ActionIndex:    actionIndex,
		ErrorMessage:   errMsg,
	}

	if status == "sent" || status == "failed" {
		now := time.Now()
		run.ExecutedAt = &now
	}

	if err := e.db.WithContext(ctx).Create(&run).Error; err != nil {
		slog.Error("marketing_flow: failed to record run", "error", err)
	}
}

// ── Template Seeding ────────────────────────────────────────────────────────

// SeedTemplates ensures the 5 pre-built templates exist for a shop.
func (e *MarketingFlowEngine) SeedTemplates(ctx context.Context, shopID uuid.UUID, shopName string) error {
	templates := GetTemplates(shopName)

	for _, tmpl := range templates {
		var existing model.MarketingFlow
		err := e.db.WithContext(ctx).
			Where("shop_id = ? AND template_key = ?", shopID, tmpl.TemplateKey).
			First(&existing).Error

		if err == nil {
			continue // Already exists
		}

		conditionsJSON, _ := json.Marshal(tmpl.Conditions)
		actionsJSON, _ := json.Marshal(tmpl.Actions)
		triggerConfigJSON, _ := json.Marshal(map[string]interface{}{})

		flow := model.MarketingFlow{
			ShopID:        shopID,
			Name:          tmpl.Name,
			TriggerType:   tmpl.TriggerType,
			TriggerConfig: datatypes.JSON(triggerConfigJSON),
			Conditions:    datatypes.JSON(conditionsJSON),
			Actions:       datatypes.JSON(actionsJSON),
			Enabled:       true,
			IsTemplate:    true,
			TemplateKey:   tmpl.TemplateKey,
		}

		if err := e.db.WithContext(ctx).Create(&flow).Error; err != nil {
			slog.Warn("marketing_flow: failed to seed template", "template", tmpl.TemplateKey, "error", err)
		} else {
			slog.Info("marketing_flow: seeded template", "template", tmpl.TemplateKey, "shop_id", shopID)
		}
	}

	return nil
}

// ── Dormant Customer Scanner ────────────────────────────────────────────────

// ScanDormantCustomers scans for dormant customers (30-90 days since last order)
// and triggers the customer.dormant flow for each.
func (e *MarketingFlowEngine) ScanDormantCustomers(ctx context.Context) error {
	// Get all active shops
	var shops []model.Shop
	if err := e.db.WithContext(ctx).Where("uninstalled_at IS NULL").Find(&shops).Error; err != nil {
		return fmt.Errorf("scan dormant: query shops: %w", err)
	}

	for _, shop := range shops {
		segments, err := e.rfm.ComputeSegments(ctx, shop.ID)
		if err != nil {
			slog.Warn("scan dormant: failed to compute segments", "shop_id", shop.ID, "error", err)
			continue
		}

		if segments.Dormant.Count == 0 && segments.Lost.Count == 0 {
			continue
		}

		// Get dormant customers
		for _, segmentName := range []string{"Dormant", "Lost"} {
			customers, err := e.rfm.GetSegmentCustomers(ctx, shop.ID, segmentName)
			if err != nil {
				slog.Warn("scan dormant: failed to get customers", "shop_id", shop.ID, "segment", segmentName, "error", err)
				continue
			}

			for _, c := range customers {
				payload := FlowTriggerPayload{
					TriggerType:    TriggerCustomerDormant,
					TriggerEventID: fmt.Sprintf("dormant:%s", c.Email),
					CustomerEmail:  c.Email,
					CustomerName:   c.Name,
					ShopID:         shop.ID.String(),
				}

				if err := e.TriggerFlow(ctx, TriggerCustomerDormant, payload); err != nil {
					slog.Warn("scan dormant: trigger flow failed",
						"shop_id", shop.ID, "email", c.Email, "error", err)
				}
			}
		}
	}

	return nil
}

// ── EventBus Integration ────────────────────────────────────────────────────

// StartEventBusConsumers subscribes to EventBus events for flow triggering.
// Returns a cleanup function.
func (e *MarketingFlowEngine) StartEventBusConsumers(ctx context.Context, eb eventbus.EventBus) error {
	// Order created → Welcome flow + check for repurchase triggers
	eb.Subscribe(eventbus.EventOrderCreated, func(ctx context.Context, ev *eventbus.Event) error {
		return e.handleOrderCreated(ctx, ev)
	})

	// Order updated (fulfilled) → Repurchase flow
	eb.Subscribe(eventbus.EventOrderUpdated, func(ctx context.Context, ev *eventbus.Event) error {
		return e.handleOrderUpdated(ctx, ev)
	})

	// Checkout synced → Abandoned cart flow
	eb.Subscribe(eventbus.EventCheckoutSynced, func(ctx context.Context, ev *eventbus.Event) error {
		return e.handleCheckoutSynced(ctx, ev)
	})

	// Fulfillment status changed → Logistics care flow
	eb.Subscribe(eventbus.EventFulfillmentStatusChanged, func(ctx context.Context, ev *eventbus.Event) error {
		return e.handleFulfillmentStatusChanged(ctx, ev)
	})

	slog.Info("marketing_flow: event bus consumers registered")
	return nil
}

func (e *MarketingFlowEngine) handleOrderCreated(ctx context.Context, ev *eventbus.Event) error {
	var order model.SyncedOrder

	// Try to parse as PipelineEventPayload first
	var pp eventbus.PipelineEventPayload
	if err := json.Unmarshal(ev.Payload, &pp); err == nil && pp.EntityID != "" {
		if err := e.db.WithContext(ctx).Where("shop_id = ? AND platform_id = ?", ev.ShopID, pp.EntityID).First(&order).Error; err != nil {
			return nil
		}
	} else {
		// Fallback: try to unmarshal as SyncedOrder directly
		if err := json.Unmarshal(ev.Payload, &order); err != nil || order.CustomerEmail == "" {
			return nil
		}
	}

	if order.CustomerEmail == "" {
		return nil
	}

	// Get shop info
	var shop model.Shop
	e.db.WithContext(ctx).First(&shop, "id = ?", ev.ShopID)

	payload := FlowTriggerPayload{
		TriggerType:   TriggerOrderCreated,
		CustomerEmail: order.CustomerEmail,
		CustomerName:  order.CustomerName,
		ShopID:        ev.ShopID.String(),
		EntityID:      order.PlatformID,
		ShopName:      shop.Domain,
		Meta: map[string]string{
			"order_amount": fmt.Sprintf("%.2f", order.TotalPrice),
		},
	}

	return e.TriggerFlow(ctx, TriggerOrderCreated, payload)
}

func (e *MarketingFlowEngine) handleOrderUpdated(ctx context.Context, ev *eventbus.Event) error {
	var order model.SyncedOrder

	// Try PipelineEventPayload format first
	var pp eventbus.PipelineEventPayload
	if err := json.Unmarshal(ev.Payload, &pp); err == nil && pp.EntityID != "" {
		if err := e.db.WithContext(ctx).Where("shop_id = ? AND platform_id = ?", ev.ShopID, pp.EntityID).First(&order).Error; err != nil {
			return nil
		}
	} else {
		// Fallback: direct SyncedOrder payload
		if err := json.Unmarshal(ev.Payload, &order); err != nil {
			return nil
		}
	}

	// Only trigger for fulfilled orders
	if order.FulfillmentStatus != "fulfilled" {
		return nil
	}

	if order.CustomerEmail == "" {
		return nil
	}

	var shop model.Shop
	e.db.WithContext(ctx).First(&shop, "id = ?", ev.ShopID)

	payload := FlowTriggerPayload{
		TriggerType:   TriggerOrderFulfilled,
		CustomerEmail: order.CustomerEmail,
		CustomerName:  order.CustomerName,
		ShopID:        ev.ShopID.String(),
		EntityID:      order.PlatformID,
		ShopName:      shop.Domain,
	}

	return e.TriggerFlow(ctx, TriggerOrderFulfilled, payload)
}

func (e *MarketingFlowEngine) handleCheckoutSynced(ctx context.Context, ev *eventbus.Event) error {
	var pp eventbus.PipelineEventPayload
	if err := json.Unmarshal(ev.Payload, &pp); err != nil || pp.EntityID == "" {
		return nil
	}

	// Only trigger for abandoned checkouts
	// Check if the checkout has been abandoned (not converted to order)
	var checkout model.SyncedCheckout
	if err := e.db.WithContext(ctx).
		Where("shop_id = ? AND platform_token = ?", ev.ShopID, pp.EntityID).
		First(&checkout).Error; err != nil {
		return nil
	}

	// Only trigger abandoned cart flow if the checkout is still open/abandoned
	if checkout.Status == "ordered" || checkout.Status == "recovered" {
		return nil
	}

	if checkout.CustomerEmail == "" {
		return nil
	}

	var shop model.Shop
	e.db.WithContext(ctx).First(&shop, "id = ?", ev.ShopID)

	payload := FlowTriggerPayload{
		TriggerType:   TriggerCheckoutAbandoned,
		CustomerEmail: checkout.CustomerEmail,
		CustomerName:  checkout.CustomerName,
		ShopID:        ev.ShopID.String(),
		EntityID:      checkout.PlatformToken,
		ShopName:      shop.Domain,
		Meta: map[string]string{
			"total_price": fmt.Sprintf("%.2f", checkout.TotalPrice),
		},
	}

	return e.TriggerFlow(ctx, TriggerCheckoutAbandoned, payload)
}

func (e *MarketingFlowEngine) handleFulfillmentStatusChanged(ctx context.Context, ev *eventbus.Event) error {
	// Extract fulfillment status from the event
	var payload struct {
		OrderID           string `json:"order_id"`
		Status            string `json:"status"`
		CustomerEmail     string `json:"customer_email"`
		CustomerName      string `json:"customer_name"`
		TrackingNumber    string `json:"tracking_number"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return nil
	}

	if payload.CustomerEmail == "" || payload.Status == "" {
		return nil
	}

	var shop model.Shop
	e.db.WithContext(ctx).First(&shop, "id = ?", ev.ShopID)

	flowPayload := FlowTriggerPayload{
		TriggerType:       TriggerFulfillmentStatus,
		CustomerEmail:     payload.CustomerEmail,
		CustomerName:      payload.CustomerName,
		ShopID:            ev.ShopID.String(),
		EntityID:          payload.OrderID,
		ShopName:          shop.Domain,
		FulfillmentStatus: payload.Status,
	}

	return e.TriggerFlow(ctx, TriggerFulfillmentStatus, flowPayload)
}
