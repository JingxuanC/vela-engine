package handler

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// AIEventPayload is the payload published when an AI interaction completes.
type AIEventPayload struct {
	Feature string      `json:"feature"`          // e.g. "chat.ask", "content.generate", "size.recommend"
	ShopID  string      `json:"shop_id"`
	Input   interface{} `json:"input,omitempty"`
	Output  interface{} `json:"output,omitempty"`
	Success bool        `json:"success"`
}

// PublishAIEvent is a convenience helper for handlers to publish AI interaction events.
// It is non-blocking and non-fatal — failures are logged at Warn level.
func PublishAIEvent(ctx context.Context, bus eventbus.EventBus, shopID uuid.UUID, feature string, input, output interface{}, success bool) {
	if bus == nil {
		return
	}
	payload := AIEventPayload{
		Feature: feature,
		ShopID:  shopID.String(),
		Input:   input,
		Output:  output,
		Success: success,
	}
	ev, err := eventbus.NewEvent(eventbus.EventAIInteraction, shopID, payload, "handler")
	if err != nil {
		slog.Warn("failed to create AI event", "feature", feature, "error", err)
		return
	}
	// Non-blocking publish — don't slow down the handler response
	go func() {
		if err := bus.Publish(ctx, ev); err != nil {
			slog.Warn("failed to publish AI event", "feature", feature, "error", err)
		}
	}()

	slog.Debug("AI event published", "feature", feature, "shop_id", shopID)
}
