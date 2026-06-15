// Package judgeme provides a Judge.me adapter implementing review.ReviewAdapter.
package judgeme

import (
	"context"
	"fmt"
	"strconv"

	"github.com/JingxuanC/vela-engine/internal/service/review"
)

// JudgeMeAdapter implements review.ReviewAdapter and review.WebhookVerifier
// by wrapping JudgeMeClient.
type JudgeMeAdapter struct {
	client *JudgeMeClient
}

// NewAdapter creates a JudgeMeAdapter from an API token and shop domain.
func NewAdapter(apiToken, shopDomain string) *JudgeMeAdapter {
	return &JudgeMeAdapter{
		client: NewJudgeMeClient(apiToken, shopDomain),
	}
}

// NewAdapterFromClient creates a JudgeMeAdapter from an existing client (for testing).
func NewAdapterFromClient(client *JudgeMeClient) *JudgeMeAdapter {
	return &JudgeMeAdapter{client: client}
}

// Name returns "judgeme".
func (a *JudgeMeAdapter) Name() string {
	return "judgeme"
}

// GetReview fetches a single review by ID. Converts string ID to int for JudgeMe API.
func (a *JudgeMeAdapter) GetReview(ctx context.Context, reviewID string) (*review.ReviewInput, error) {
	id, err := strconv.Atoi(reviewID)
	if err != nil {
		return nil, fmt.Errorf("judgeme adapter: invalid review ID %q: %w", reviewID, err)
	}

	jr, err := a.client.GetReview(ctx, id)
	if err != nil {
		return nil, err
	}

	return toReviewInput(jr), nil
}

// GetReviews fetches a paginated list of reviews.
func (a *JudgeMeAdapter) GetReviews(ctx context.Context, params review.ReviewQueryParams) (*review.ReviewList, error) {
	resp, err := a.client.GetReviews(ctx, toJudgeMeParams(params))
	if err != nil {
		return nil, err
	}

	reviews := make([]review.ReviewInput, len(resp.Reviews))
	for i, jr := range resp.Reviews {
		reviews[i] = *toReviewInput(&jr)
	}

	return &review.ReviewList{
		Reviews:    reviews,
		Total:      resp.Total,
		Page:       resp.Page,
		PerPage:    resp.PerPage,
		TotalPages: resp.TotalPages,
	}, nil
}

// PostReply posts a public reply to a review on JudgeMe.
func (a *JudgeMeAdapter) PostReply(ctx context.Context, reviewID string, content string) error {
	id, err := strconv.Atoi(reviewID)
	if err != nil {
		return fmt.Errorf("judgeme adapter: invalid review ID %q: %w", reviewID, err)
	}

	return a.client.CreateReply(ctx, id, content)
}

// Verify verifies a Judge.me HMAC-SHA256 webhook signature.
func (a *JudgeMeAdapter) Verify(signature string, body []byte, secret string) bool {
	return VerifyWebhookSignature(signature, body, secret)
}

// --- helpers ---

func toReviewInput(jr *JudgeMeReview) *review.ReviewInput {
	if jr == nil {
		return nil
	}
	return &review.ReviewInput{
		ID:     strconv.Itoa(jr.ID),
		Author: jr.ReviewerName,
		Rating: jr.Rating,
		Title:  jr.Title,
		Body:   jr.Body,
		Date:   jr.CreatedAt,
	}
}

func toJudgeMeParams(params review.ReviewQueryParams) ListReviewsParams {
	return ListReviewsParams{
		RatingMin:    params.RatingMin,
		RatingMax:    params.RatingMax,
		CreatedAtMin: params.CreatedAtMin,
		CreatedAtMax: params.CreatedAtMax,
		Page:         params.Page,
		PerPage:      params.PerPage,
	}
}
