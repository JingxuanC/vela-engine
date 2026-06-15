// Package taskqueue provides an Asynq-based task queue framework.
package taskqueue

// TaskType constants for all asynchronous tasks.
const (
	// TypeEnrichProduct enriches a product with AI-generated content.
	TypeEnrichProduct TaskType = "product:enrich"
	// TypeCartRecoveryCheck processes abandoned checkouts for cart recovery campaigns.
	TypeCartRecoveryCheck TaskType = "cart_recovery:check"
	// TypeContentPullMetrics pulls platform analytics metrics (Pinterest, etc.) for published content.
	TypeContentPullMetrics TaskType = "content:pull_metrics"
	// TypeReviewInvitation sends a review invitation email to a customer after a delay.
	TypeReviewInvitation TaskType = "review:invitation"
	// TypeCustomerIntelligenceRefresh triggers LTV/churn recomputation for a shop.
	TypeCustomerIntelligenceRefresh TaskType = "customer:intelligence_refresh"
	// TypeFBTRefresh triggers FBT (Frequently Bought Together) recomputation for a shop.
	TypeFBTRefresh TaskType = "recommendation:fbt_refresh"
	// TypeTrendingRefresh triggers trending products recomputation for a shop.
	TypeTrendingRefresh TaskType = "recommendation:trending_refresh"
)

// TaskType identifies the type of task to be processed.
type TaskType string

// EnrichProductPayload is the payload for TypeEnrichProduct tasks.
type EnrichProductPayload struct {
	ProductID string `json:"product_id"`
	TenantID  string `json:"tenant_id"`
	ShopifyID int64  `json:"shopify_id"`
}

// CartRecoveryCheckPayload is the payload for TypeCartRecoveryCheck tasks.
type CartRecoveryCheckPayload struct {
	ShopID string `json:"shop_id"`
}

// ContentPullMetricsPayload is the payload for TypeContentPullMetrics tasks.
type ContentPullMetricsPayload struct {
	ShopID   string `json:"shop_id"`
	Platform string `json:"platform"` // e.g. "pinterest"
}

// ReviewInvitationPayload is the payload for TypeReviewInvitation tasks.
type ReviewInvitationPayload struct {
	ReviewInvitationID string `json:"review_invitation_id"` // UUID
}

// CustomerIntelligenceRefreshPayload is the payload for TypeCustomerIntelligenceRefresh tasks.
type CustomerIntelligenceRefreshPayload struct {
	ShopID string `json:"shop_id"`
}
