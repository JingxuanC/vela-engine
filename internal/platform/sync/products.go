package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
	_ "github.com/JingxuanC/vela-engine/internal/platform/datapipeline/stages"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// shopifyProductRaw mirrors the Shopify REST API /products.json response structure
// so we can capture variants, images, and options as raw JSON.
type shopifyProductRaw struct {
	ID          int64           `json:"id"`
	Title       string          `json:"title"`
	BodyHTML    string          `json:"body_html"`
	Vendor      string          `json:"vendor"`
	ProductType string          `json:"product_type"`
	Status      string          `json:"status"`
	Tags        string          `json:"tags"`
	UpdatedAt   time.Time       `json:"updated_at"`
	Variants    json.RawMessage `json:"variants"`
	Images      json.RawMessage `json:"images"`
	Options     json.RawMessage `json:"options"`
}

type ProductSyncer struct {
	db       *gorm.DB
	client   *ShopifyRESTClient
	eventBus eventbus.EventBus
}

func NewProductSyncer(db *gorm.DB, c *ShopifyRESTClient, bus eventbus.EventBus) *ProductSyncer {
	return &ProductSyncer{db: db, client: c, eventBus: bus}
}
func (s *ProductSyncer) EntityType() string { return "products" }

// upsertProduct creates or updates a single SyncedProduct row.
// It returns true if a new record was created (RowsAffected == 1),
// false if an existing record was updated.
func (s *ProductSyncer) upsertProduct(ctx context.Context, p shopifyProductRaw, shopID uuid.UUID) (bool, error) {
	variants := p.Variants
	if len(variants) == 0 {
		variants = json.RawMessage("[]")
	}
	images := p.Images
	if len(images) == 0 {
		images = json.RawMessage("[]")
	}
	options := p.Options
	if len(options) == 0 {
		options = json.RawMessage("[]")
	}

	m := model.SyncedProduct{
		ID:          uuid.New(),
		ShopID:      shopID,
		PlatformID:  fmt.Sprintf("%d", p.ID),
		Title:       p.Title,
		Description: p.BodyHTML,
		Vendor:      p.Vendor,
		ProductType: p.ProductType,
		Status:      p.Status,
		Tags:        p.Tags,
		Variants:    datatypes.JSON(variants),
		Images:      datatypes.JSON(images),
		Options:     datatypes.JSON(options),
	}
	res := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "shop_id"}, {Name: "platform_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"title", "description", "vendor", "product_type", "status", "tags", "variants", "images", "options", "currency_normalized", "region", "size_normalized", "category_path", "materials"}),
		}).
		Create(&m)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// runEnrichPipeline runs the data pipeline on a synced product and persists the enriched fields.
func (s *ProductSyncer) runEnrichPipeline(ctx context.Context, p shopifyProductRaw, shopID uuid.UUID) {
	stageNames := []string{"normalize_currency", "classify_region", "normalize_size", "tag_category", "extract_material"}
	pipeStages := make([]datapipeline.Stage, 0, len(stageNames))
	for _, name := range stageNames {
		if stage, ok := datapipeline.GetStage(name); ok {
			pipeStages = append(pipeStages, stage)
		}
	}
	if len(pipeStages) == 0 {
		return
	}
	pipeline := datapipeline.NewPipeline(pipeStages...)
	rawPayload, _ := json.Marshal(p)
	data, err := pipeline.Run(ctx, &datapipeline.RawData{Source: "sync", Payload: rawPayload})
	if err != nil {
		slog.Warn("sync: pipeline enrichment failed (non-fatal)", "platform_id", p.ID, "error", err)
		return
	}
	if data == nil || len(data.Errors) > 0 {
		return
	}
	updates := map[string]any{}
	if v, ok := data.Fields["currency_normalized"]; ok {
		updates["currency_normalized"] = v
	}
	if v, ok := data.Fields["region"]; ok {
		updates["region"] = v
	}
	if v, ok := data.Fields["size_normalized"]; ok {
		updates["size_normalized"] = v
	}
	if v, ok := data.Fields["category_path"]; ok {
		updates["category_path"] = v
	}
	if v, ok := data.Fields["materials"]; ok {
		materialsJSON, marshalErr := json.Marshal(v)
		if marshalErr == nil {
			updates["materials"] = materialsJSON
		}
	}
	if len(updates) > 0 {
		platformID := fmt.Sprintf("%d", p.ID)
		if dbErr := s.db.Model(&model.SyncedProduct{}).Where("shop_id = ? AND platform_id = ?", shopID, platformID).Updates(updates).Error; dbErr != nil {
			slog.Warn("sync: failed to persist pipeline fields", "platform_id", p.ID, "error", dbErr)
		}
	}
}

func (s *ProductSyncer) Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error) {
	r := &SyncResult{}
	start := time.Now()
	defer func() { r.DurationMs = time.Since(start).Milliseconds() }()
	var shop model.Shop
	if err := s.db.First(&shop, "id = ?", opts.ShopID).Error; err != nil {
		return nil, fmt.Errorf("sync products: %w", err)
	}
	params := url.Values{}
	params.Set("limit", "250")
	if !opts.FullSync && !opts.Since.IsZero() {
		params.Set("updated_at_min", opts.Since.Format(time.RFC3339))
	}
	page, err := s.client.GetPaginated(ctx, shop.ShopDomain, shop.AccessToken, "products.json", params)
	if err != nil {
		return nil, err
	}

	const maxPages = 100 // safety limit: 100 pages × 250 = 25,000 products
	for pageNum := 1; page != nil && pageNum <= maxPages; pageNum++ {
		// Check context cancellation between pages
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		default:
		}

		var w struct {
			Products []shopifyProductRaw `json:"products"`
		}
		if err := json.Unmarshal(page.Body, &w); err != nil {
			slog.Error("sync: unmarshal products", "page", pageNum, "error", err)
			r.Errors++
			r.FailedPages++
			break
		}
		for _, p := range w.Products {
			isNew, err := s.upsertProduct(ctx, p, shop.ID)
			if err != nil {
				slog.Error("sync: upsert product", "platform_id", p.ID, "error", err)
				r.Errors++
			} else {
				if isNew {
					r.NewRecords++
				} else {
					r.UpdatedRecords++
				}
				r.TotalRecords++
				// Run data pipeline to enrich product fields
				s.runEnrichPipeline(ctx, p, shop.ID)
				// Publish event for downstream consumers
				if s.eventBus != nil {
					ev, _ := eventbus.NewEvent(eventbus.EventProductSynced, shop.ID,
						eventbus.PipelineEventPayload{
							EntityID:  fmt.Sprintf("%d", p.ID),
							ShopID:    shop.ID.String(),
							Source:    "cron",
							Action:    "upsert",
							Timestamp: time.Now().UTC().Format(time.RFC3339),
						}, "sync/products")
					_ = s.eventBus.Publish(ctx, ev)
				}
			}
		}
		if page.NextPageURL == "" {
			break
		}
		var nextErr error
		page, nextErr = s.client.GetNextPage(ctx, page.NextPageURL, shop.AccessToken)
		if nextErr != nil {
			slog.Error("sync: next page failed", "page", pageNum+1, "url", page.NextPageURL, "error", nextErr)
			r.Errors++
			r.FailedPages++
			break
		}
	}
	slog.Info("sync", "pipeline", "products", "shop_id", opts.ShopID.String(), "count", r.TotalRecords, "new", r.NewRecords, "updated", r.UpdatedRecords, "errors", r.Errors, "duration_ms", r.DurationMs)
	return r, nil
}
