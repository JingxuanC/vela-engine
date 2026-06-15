package service

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service/billing"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type TokenTracker struct {
	db    *gorm.DB
	cache *CacheService
}
type UsageRecord struct {
	ShopID    uuid.UUID
	Feature   string
	Operation string
	Count     int
	CostUSD   float64
	Status    string
	ErrorMsg  string
}

func NewTokenTracker(db *gorm.DB, cache *CacheService) *TokenTracker {
	return &TokenTracker{db: db, cache: cache}
}
func (t *TokenTracker) Record(ctx context.Context, rec UsageRecord) error {
	if rec.Count <= 0 {
		rec.Count = 1
	}
	if rec.Status == "" {
		rec.Status = "success"
	}
	var shop model.Shop
	if t.db.First(&shop, "id = ?", rec.ShopID).Error == nil {
		total, _ := t.GetMonthlyTotal(ctx, rec.ShopID, time.Now().Format("200601"))
		plan := billing.AvailableV2Plans[shop.Plan]
		if plan.MonthlyQuota > 0 && int(total) >= plan.MonthlyQuota {
			return fmt.Errorf("monthly quota exceeded: %d/%d", total, plan.MonthlyQuota)
		}
	}
	u := model.TokenUsage{ShopID: rec.ShopID, Feature: rec.Feature, Operation: rec.Operation, Count: rec.Count, CostUSD: rec.CostUSD, RecordedAt: time.Now().UTC(), Status: rec.Status, ErrorMsg: rec.ErrorMsg}
	if err := t.db.WithContext(ctx).Create(&u).Error; err != nil {
		return fmt.Errorf("token_tracker: %w", err)
	}
	if t.cache != nil && rec.Status == "success" {
		ym := time.Now().UTC().Format("200601")
		t.cache.Client().IncrBy(ctx, fmt.Sprintf("token_usage:%s:%s:%s", rec.ShopID, rec.Feature, ym), int64(rec.Count))
		t.cache.Client().IncrBy(ctx, fmt.Sprintf("token_usage:%s:total:%s", rec.ShopID, ym), int64(rec.Count))
	}
	return nil
}

var asyncSem = make(chan struct{}, 50) // max 50 concurrent async writes

func (t *TokenTracker) RecordAsync(ctx context.Context, rec UsageRecord) {
	select {
	case asyncSem <- struct{}{}:
		go func() {
			defer func() {
				<-asyncSem
				if r := recover(); r != nil {
					slog.Error("token_tracker: panic", "error", r)
				}
			}()
			if err := t.Record(context.Background(), rec); err != nil {
				slog.Error("token_tracker: async failed", "error", err)
			}
		}()
	default:
		slog.Warn("token_tracker: async pool full, record dropped", "shop", rec.ShopID)
	}
}
func (t *TokenTracker) GetMonthlyTotal(ctx context.Context, shopID uuid.UUID, ym string) (int64, error) {
	if t.cache != nil {
		v, err := t.cache.Client().Get(ctx, fmt.Sprintf("token_usage:%s:total:%s", shopID, ym)).Result()
		if err == nil {
			return strconv.ParseInt(v, 10, 64)
		}
	}
	start, _ := time.Parse("200601", ym)
	var sum struct{ Total int64 }
	t.db.Model(&model.TokenUsage{}).Where("shop_id = ? AND recorded_at >= ? AND recorded_at < ?", shopID, start, start.AddDate(0, 1, 0)).Select("COALESCE(SUM(count),0) as total").Scan(&sum)
	return sum.Total, nil
}
