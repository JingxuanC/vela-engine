// Package review provides Adapter interfaces for multi-platform review integration.
package review

import (
	"context"
)

// ReviewQueryParams holds filter/pagination parameters for listing reviews.
type ReviewQueryParams struct {
	RatingMin    *int
	RatingMax    *int
	CreatedAtMin *string // RFC3339
	CreatedAtMax *string // RFC3339
	Page         int
	PerPage      int
}

// ReviewList is a paginated list of reviews from any platform.
type ReviewList struct {
	Reviews    []ReviewInput `json:"reviews"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	PerPage    int           `json:"per_page"`
	TotalPages int           `json:"total_pages"`
}

// ReviewAdapter abstracts review platform operations (read reviews, post replies).
type ReviewAdapter interface {
	// Name returns the platform identifier: "judgeme" | "loox" | "yotpo" | "shopify_native"
	Name() string

	// GetReview fetches a single review by platform-internal ID.
	GetReview(ctx context.Context, reviewID string) (*ReviewInput, error)

	// GetReviews fetches a paginated list of reviews (for full/incremental sync).
	GetReviews(ctx context.Context, params ReviewQueryParams) (*ReviewList, error)

	// PostReply posts a public reply to the review on the platform.
	PostReply(ctx context.Context, reviewID string, content string) error
}

// WebhookVerifier verifies webhook signatures from review platforms.
type WebhookVerifier interface {
	Verify(signature string, body []byte, secret string) bool
}
