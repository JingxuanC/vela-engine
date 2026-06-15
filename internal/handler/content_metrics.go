package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/service"
)

// ContentMetricsPuller pulls platform analytics metrics for published content pieces.
type ContentMetricsPuller struct {
	db      *gorm.DB
	crypto  *service.CryptoService
	social  *SocialHandler
}

// NewContentMetricsPuller creates a new ContentMetricsPuller.
func NewContentMetricsPuller(db *gorm.DB, crypto *service.CryptoService, social *SocialHandler) *ContentMetricsPuller {
	return &ContentMetricsPuller{db: db, crypto: crypto, social: social}
}

// PullPlatformMetrics queries all published ContentPieces for a shop+platform,
// fetches analytics from the platform API, and upserts ContentPlatformMetrics rows.
func (p *ContentMetricsPuller) PullPlatformMetrics(ctx context.Context, shopID, platform string) error {
	if p.db == nil {
		return fmt.Errorf("content_metrics: db unavailable")
	}

	pub := p.social.GetPublisher(platform)
	if pub == nil {
		return fmt.Errorf("content_metrics: unsupported platform: %s", platform)
	}

	shopUUID, err := uuid.Parse(shopID)
	if err != nil {
		return fmt.Errorf("content_metrics: invalid shop_id: %w", err)
	}

	// Look up active social connection for access token
	var conn model.SocialConnection
	if err := p.db.WithContext(ctx).Where("shop_id = ? AND platform = ? AND status = ?",
		shopUUID, platform, "active").First(&conn).Error; err != nil {
		slog.Warn("content_metrics: skipping pull — no active connection", "shop_id", shopID, "platform", platform)
			return nil
	}

	// Decrypt access token
	if p.crypto == nil {
		return fmt.Errorf("content_metrics: crypto service not configured")
	}
	accessTokenBytes, err := p.crypto.Decrypt(conn.EncryptedToken)
	if err != nil {
		return fmt.Errorf("content_metrics: decrypt token: %w", err)
	}
	accessToken := string(accessTokenBytes)

	// Query all published ContentPieces for this shop+platform
	var pieces []model.ContentPiece
	if err := p.db.WithContext(ctx).
		Where("shop_id = ? AND platform = ? AND status IN ?",
			shopUUID, platform, []string{"published", "generated"}).
		Find(&pieces).Error; err != nil {
		return fmt.Errorf("content_metrics: query pieces: %w", err)
	}

	if len(pieces) == 0 {
		slog.Info("content_metrics: no published pieces to pull",
			"shop_id", shopID, "platform", platform)
		return nil
	}

	// Default "since" window: last 30 days
	since := time.Now().AddDate(0, 0, -30)
	today := time.Now().UTC().Truncate(24 * time.Hour)

	for _, piece := range pieces {
		// ContentPiece doesn't store the platform post ID directly,
		// so we need to look up the SocialPost(s) associated with this piece.
		// First check if piece has PlatformPostID attached; otherwise
		// look up SocialPosts by ContentPieceID.
		postIDs := p.resolvePlatformPostIDs(ctx, piece.ID, platform, shopUUID)

		for _, postID := range postIDs {
			metrics, err := pub.GetAnalytics(ctx, accessToken, postID, since)
			if err != nil {
				slog.Warn("content_metrics: analytics pull failed",
					"piece_id", piece.ID,
					"platform_post_id", postID,
					"error", err,
				)
				continue
			}

			// Upsert ContentPlatformMetrics for today
			cpm := model.ContentPlatformMetrics{
				ShopID:         shopUUID,
				ContentPieceID: piece.ID,
				Platform:       platform,
				MetricsDate:    today,
				PlatformPostID: postID,
				Impressions:    metrics.Impressions,
				Clicks:         metrics.Clicks,
				Saves:          metrics.Saves,
				Engagement:     metrics.Engagement,
				PulledAt:       time.Now().UTC(),
			}

			if err := p.db.WithContext(ctx).
				Clauses(clause.OnConflict{
					Columns: []clause.Column{{Name: "shop_id"}, {Name: "content_piece_id"}, {Name: "platform"}, {Name: "metrics_date"}},
					DoUpdates: clause.AssignmentColumns([]string{
						"platform_post_id", "impressions", "clicks", "saves",
						"engagement", "pulled_at",
					}),
				}).Create(&cpm).Error; err != nil {
				slog.Warn("content_metrics: upsert failed",
					"piece_id", piece.ID,
					"platform_post_id", postID,
					"error", err,
				)
			}
		}
	}

	slog.Info("content_metrics: pull complete",
		"shop_id", shopID,
		"platform", platform,
		"pieces", len(pieces),
	)

	return nil
}

// resolvePlatformPostIDs returns the platform post IDs associated with a content piece.
// It looks up SocialPosts by ContentPieceID.
func (p *ContentMetricsPuller) resolvePlatformPostIDs(ctx context.Context, pieceID uuid.UUID, platform string, shopID uuid.UUID) []string {
	var posts []model.SocialPost
	if err := p.db.WithContext(ctx).
		Where("content_piece_id = ? AND platform = ? AND status = ? AND shop_id = ?",
			pieceID, platform, "published", shopID).
		Find(&posts).Error; err != nil {
		slog.Warn("content_metrics: query social posts failed",
			"piece_id", pieceID, "error", err)
		return nil
	}

	ids := make([]string, 0, len(posts))
	for _, post := range posts {
		if post.PinID != "" {
			ids = append(ids, post.PinID)
		}
	}
	return ids
}

// HandleTask is the asynq task handler for TypeContentPullMetrics.
// It unmarshals the payload and delegates to PullPlatformMetrics.
func (p *ContentMetricsPuller) HandleTask(ctx context.Context, payload []byte) error {
	var pld taskqueuePayload
	if err := json.Unmarshal(payload, &pld); err != nil {
		return fmt.Errorf("content_metrics: unmarshal payload: %w", err)
	}

	if pld.ShopID == "" || pld.Platform == "" {
		return fmt.Errorf("content_metrics: shop_id and platform required")
	}

	return p.PullPlatformMetrics(ctx, pld.ShopID, pld.Platform)
}

// taskqueuePayload mirrors taskqueue.ContentPullMetricsPayload to avoid
// a circular import from handler -> taskqueue.
type taskqueuePayload struct {
	ShopID   string `json:"shop_id"`
	Platform string `json:"platform"`
}
