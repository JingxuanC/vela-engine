package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
	_ "github.com/JingxuanC/vela-engine/internal/platform/datapipeline/stages"
	"github.com/JingxuanC/vela-engine/internal/platform/taskqueue"
)

// handleEnrichProduct runs the product enrichment pipeline for a task queue task.
func (h *WebhookHandler) HandleEnrichProduct(ctx context.Context, t *asynq.Task) error {
	var payload struct {
		ShopID     uuid.UUID `json:"shop_id"`
		PlatformID string    `json:"platform_id"`
	}
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return fmt.Errorf("enrich_product: unmarshal payload: %w", err)
	}

	var product model.SyncedProduct
	if err := h.db.WithContext(ctx).Where("shop_id = ? AND platform_id = ?", payload.ShopID, payload.PlatformID).First(&product).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			slog.Debug("webhook: enrich product skipped — product not found", "shop_id", payload.ShopID, "platform_id", payload.PlatformID)
			return nil
		}
		return fmt.Errorf("enrich_product: query product: %w", err)
	}

	h.runProductPipeline(ctx, payload.ShopID, payload.PlatformID, product)
	return nil
}

// runProductPipeline runs the data enrichment pipeline on a synced product.
func (h *WebhookHandler) runProductPipeline(ctx context.Context, shopID uuid.UUID, platformID string, product model.SyncedProduct) {
	stageNames := []string{"normalize_currency", "classify_region", "normalize_size", "tag_category", "extract_material"}
	pipeStages := make([]datapipeline.Stage, 0, len(stageNames))
	for _, name := range stageNames {
		if s, ok := datapipeline.GetStage(name); ok { pipeStages = append(pipeStages, s) }
	}
	if len(pipeStages) == 0 { return }

	pipeline := datapipeline.NewPipeline(pipeStages...)
	rawPayload, _ := json.Marshal(product)
	data, err := pipeline.Run(ctx, &datapipeline.RawData{Source: "webhook", Payload: rawPayload})
	if err != nil { slog.Warn("webhook: pipeline enrichment failed (non-fatal)", "platform_id", platformID, "error", err); return }
	if data == nil || len(data.Errors) > 0 { return }

	updates := map[string]any{}
	if v, ok := data.Fields["currency_normalized"]; ok { updates["currency_normalized"] = v }
	if v, ok := data.Fields["region"]; ok { updates["region"] = v }
	if v, ok := data.Fields["size_normalized"]; ok { updates["size_normalized"] = v }
	if v, ok := data.Fields["category_path"]; ok { updates["category_path"] = v }
	if v, ok := data.Fields["materials"]; ok {
		if materialsJSON, marshalErr := json.Marshal(v); marshalErr == nil { updates["materials"] = materialsJSON }
	}
	if len(updates) > 0 {
		if dbErr := h.db.Model(&model.SyncedProduct{}).Where("shop_id = ? AND platform_id = ?", shopID, platformID).Updates(updates).Error; dbErr != nil {
			slog.Warn("webhook: failed to persist pipeline fields", "platform_id", platformID, "error", dbErr)
		}
	}
}

// enqueueEnrichment pushes product enrichment to the task queue for async processing.
// Falls back to an inline goroutine when the task client is unavailable.
func (h *WebhookHandler) enqueueEnrichment(ctx context.Context, shopID uuid.UUID, platformID string, product model.SyncedProduct) {
	if h.taskClient != nil {
		payload := map[string]string{"shop_id": shopID.String(), "platform_id": platformID}
		if _, err := h.taskClient.Enqueue(ctx, &taskqueue.Task{Type: taskqueue.TypeEnrichProduct, Payload: payload}); err != nil {
			slog.Warn("webhook: failed to enqueue enrichment, falling back to goroutine", "platform_id", platformID, "error", err)
			go h.runProductPipeline(bgTimeout(60*time.Second), shopID, platformID, product)
		}
		return
	}
	go h.runProductPipeline(bgTimeout(60*time.Second), shopID, platformID, product)
}
