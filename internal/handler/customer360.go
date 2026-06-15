package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// Customer360Handler provides rich customer profile views that aggregate
// data across all Vela modules — orders, reviews, checkouts, AI chat — to
// show merchants the "data density" Vela provides on each customer.
type Customer360Handler struct{ db *gorm.DB }

func NewCustomer360Handler(db *gorm.DB) *Customer360Handler { return &Customer360Handler{db: db} }

// ── Types ──────────────────────────────────────────────────────────────────────

// CustomerProfile is the rich response for GET /api/customer360/{customerID}/profile.
type CustomerProfile struct {
	ID               string              `json:"id"`
	PlatformID       string              `json:"platform_id"`
	Email            string              `json:"email"`
	Name             string              `json:"name"`
	OrdersCount      int                 `json:"orders_count"`
	TotalSpent       float64             `json:"total_spent"`
	Tags             []string            `json:"tags"`
	Velatags         []string            `json:"vela_tags"`          // Vela-computed tags
	DensityScore     DensityScore        `json:"density_score"`
	Summary          ProfileSummary      `json:"summary"`
	ChatProfile      *ChatProfileView    `json:"chat_profile"`       // from CustomerChatProfile
	Orders           []OrderView         `json:"orders"`
	Reviews          []ReviewView        `json:"reviews"`
	Checkouts        []CheckoutView      `json:"checkouts"`
	Timeline         []TimelineEvent     `json:"timeline"`
	Suggestions      []ActionSuggestion  `json:"suggestions"`
}

type ProfileSummary struct {
	LifetimeValue        float64 `json:"lifetime_value"`
	OrderCount           int     `json:"order_count"`
	AvgOrderValue        float64 `json:"avg_order_value"`
	LastOrderAt          *string `json:"last_order_at"`
	NegativeReviewCount  int     `json:"negative_review_count"`
	AbandonedCartCount   int     `json:"abandoned_cart_count"`
	ChatSessions         int     `json:"chat_sessions"`
	VelaInfluencedRevenue float64 `json:"vela_influenced_revenue"`
}

type DensityScore struct {
	Score      int             `json:"score"`       // 0-100
	Dimensions []DimBreakdown  `json:"dimensions"`
	Grade      string          `json:"grade"`       // "rich"|"good"|"basic"|"thin"
}

type DimBreakdown struct {
	Name   string `json:"name"`
	Weight int    `json:"weight"`
	Score  int    `json:"score"`
	Status string `json:"status"` // "full"|"partial"|"empty"
}

type ChatProfileView struct {
	SizePreference    string   `json:"size_preference"`
	ColorPreferences  []string `json:"color_preferences"`
	StylePreferences  []string `json:"style_preferences"`
	BudgetMin         *float64 `json:"budget_min"`
	BudgetMax         *float64 `json:"budget_max"`
	CategoryInterests []string `json:"category_interests"`
	PriceSensitivity  string   `json:"price_sensitivity"`
	IntentStrength    string   `json:"intent_strength"`
	DiscountRequests  int      `json:"discount_requests"`
	PurchasesViaChat  int      `json:"purchases_via_chat"`
	TotalChatSessions int      `json:"total_chat_sessions"`
	FirstSeen         *string  `json:"first_seen"`
	LastSeen          *string  `json:"last_seen"`
}

type OrderView struct {
	ID            string  `json:"id"`
	OrderNumber   int     `json:"order_number"`
	TotalPrice    float64 `json:"total_price"`
	Currency      string  `json:"currency"`
	Status        string  `json:"financial_status"`
	Fulfillment   string  `json:"fulfillment_status"`
	CreatedAt     string  `json:"created_at"`
	VelaAttributed bool   `json:"vela_attributed"` // this order used a Vela discount or content UTM
}

type ReviewView struct {
	ID             string  `json:"id"`
	Rating         float64 `json:"rating"`
	Title          string  `json:"title"`
	Body           string  `json:"body"`
	Status         string  `json:"status"`          // reply status
	FinalReply     string  `json:"final_reply"`
	DiscountCode   string  `json:"discount_code"`
	DiscountUsed   bool    `json:"discount_used"`
	Severity       string  `json:"severity"`
	Issues         string  `json:"issues"`
	CreatedAt      string  `json:"created_at"`
}

type CheckoutView struct {
	ID            string  `json:"id"`
	Status        string  `json:"status"`          // open, abandoned, recovered
	TotalPrice    float64 `json:"total_price"`
	RecoverySent  bool    `json:"recovery_sent"`    // recovery email sent
	RecoveryOpened bool   `json:"recovery_opened"`  // recovery email opened
	DiscountUsed  bool    `json:"discount_used"`    // recovery code used
	CreatedAt     string  `json:"created_at"`
}

// TimelineEvent is a unified event across all Vela modules, ordered by time.
type TimelineEvent struct {
	Timestamp time.Time `json:"ts"`
	Type      string    `json:"type"`       // order, review, auto_reply, checkout, recovery, chat
	Priority  string    `json:"priority"`   // critical, important, background
	Summary   string    `json:"summary"`    // one-line label
	Detail    string    `json:"detail"`     // hover text
	Velatouch bool      `json:"vela_touch"` // Vela intervened
}

// ActionSuggestion is a rule-based next-step recommendation.
type ActionSuggestion struct {
	Priority string `json:"priority"` // high, medium, low
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	CTA      string `json:"cta"`
}

// ── List (enhanced) ────────────────────────────────────────────────────────────

type customerListRow struct {
	ID          string
	PlatformID  string
	Email       string
	FirstName   string
	LastName    string
	OrdersCount int
	TotalSpent  float64
	Tags        string
	// Vela computed
	VelaScore       float64
	VelaRevenue     float64
	ChatSessions    int
	HasNegativeReview bool
	ProfileGrade    string
}

func (h *Customer360Handler) List(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteOK(w, map[string]interface{}{"success": true, "customers": []interface{}{}, "total": 0})
		return
	}

	sid := r.URL.Query().Get("shop_id")
	shopID, _ := uuid.Parse(sid)
	lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	sortBy := r.URL.Query().Get("sort")
	filterTag := r.URL.Query().Get("tag")
	if lim < 1 || lim > 100 { lim = 20 }
	if off < 0 { off = 0 }

	ctx := r.Context()

	// Total count
	var total int64
	h.db.WithContext(ctx).Model(&model.SyncedCustomer{}).Where("shop_id = ?", shopID).Count(&total)

	// Base customer query
	var base []customerListRow
	q := h.db.WithContext(ctx).Table("synced_customers").
		Select("id, platform_id, email, first_name, last_name, orders_count, total_spent, COALESCE(tags, '') AS tags").
		Where("shop_id = ?", shopID)

	if filterTag != "" {
		q = q.Where("tags ILIKE ?", "%"+filterTag+"%")
	}

	switch sortBy {
	case "orders":
		q = q.Order("orders_count DESC")
	case "spent":
		q = q.Order("total_spent DESC")
	default:
		q = q.Order("created_at DESC")
	}
	q.Limit(lim).Offset(off).Scan(&base)

	// Batch preload enrichment data (avoid N+1 queries)
	customerIDs := make([]string, len(base))
	customerEmails := make([]string, len(base))
	for i, r := range base {
		customerIDs[i] = r.PlatformID
		customerEmails[i] = r.Email
	}

	// Single batch query for chat profiles
	var profiles []model.CustomerChatProfile
	h.db.WithContext(ctx).Where("shop_id = ? AND customer_id IN ?", shopID, customerIDs).Find(&profiles)
	profileMap := make(map[string]*model.CustomerChatProfile, len(profiles))
	for i := range profiles {
		profileMap[profiles[i].CustomerID] = &profiles[i]
	}

	// Single batch query for negative reviews
	var negRows []struct {
		CustomerEmail string
		Cnt           int64
	}
	h.db.WithContext(ctx).Model(&model.ReplyRecord{}).
		Select("customer_email, COUNT(*) AS cnt").
		Where("shop_id = ? AND customer_email IN ? AND rating <= 3", shopID, customerEmails).
		Group("customer_email").Scan(&negRows)
	negMap := make(map[string]bool, len(negRows))
	for _, nr := range negRows {
		negMap[nr.CustomerEmail] = nr.Cnt > 0
	}

	// Build enriched items
	type enriched struct {
		ID, PlatformID, Email, Name, Tags string
		Orders, Spent int; SpentF float64
		VelaScore float64; VelaRevenue float64; ChatSessions int
		HasNegative bool; ProfileGrade string
	}
	items := make([]enriched, len(base))
	for i, r := range base {
		items[i] = enriched{
			ID: r.ID, PlatformID: r.PlatformID, Email: r.Email,
			Name: strings.TrimSpace(r.FirstName + " " + r.LastName),
			Orders: r.OrdersCount, SpentF: r.TotalSpent, Tags: r.Tags,
		}

		if cp := profileMap[r.PlatformID]; cp != nil {
			items[i].ChatSessions = cp.TotalChatSessions
			items[i].VelaScore = computeVelaScore(cp, r.OrdersCount, r.TotalSpent)
		} else {
			items[i].VelaScore = float64(r.OrdersCount) * 5
		}

		items[i].HasNegative = negMap[r.Email]
	}

		// Build response
		type out struct {
			ID, Email, Name, Tags string
			Orders int; Spent float64
			VelaScore float64; VelaRevenue float64; ChatSessions int
			HasNegative bool
		}
		outItems := make([]out, len(items))
		for i, it := range items {
			outItems[i] = out{
				ID: it.ID, Email: it.Email, Name: it.Name, Tags: it.Tags,
				Orders: it.Orders, Spent: it.SpentF,
				VelaScore: it.VelaScore, VelaRevenue: it.VelaRevenue, ChatSessions: it.ChatSessions,
				HasNegative: it.HasNegative,
			}
		}

		httputil.WriteOK(w, map[string]interface{}{
			"success":   true,
			"customers": outItems,
			"total":     total,
			"limit":     lim,
			"offset":    off,
		})
	}

// ── GetProfile ─────────────────────────────────────────────────────────────────

func (h *Customer360Handler) GetProfile(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "Database not available")
		return
	}

	sid := r.URL.Query().Get("shop_id")
	shopID, _ := uuid.Parse(sid)
	cid := chi.URLParam(r, "customerID")
	ctx := r.Context()

	// 1. Resolve customer
	var cust model.SyncedCustomer
	query := h.db.WithContext(ctx).Where("shop_id = ?", shopID)
	if _, parseErr := uuid.Parse(cid); parseErr == nil {
		query = query.Where("id = ? OR platform_id = ?", cid, cid)
	} else {
		query = query.Where("platform_id = ?", cid)
	}
	if err := query.First(&cust).Error; err != nil {
		httputil.WriteError(w, http.StatusNotFound, "Customer not found")
		return
	}

	// 2. Concurrent data fetch
	var (
		orders     []model.SyncedOrder
		reviews    []model.ReplyRecord
		checkouts  []model.SyncedCheckout
		sends      []model.CartRecoverySend
		codes      []model.CartRecoveryCode
		recoveries []model.ReviewRecoveryAttribution
		profile    model.CustomerChatProfile
		hasProfile bool
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return h.db.WithContext(gctx).Where("shop_id = ? AND customer_email = ?", shopID, cust.Email).Order("created_at DESC").Find(&orders).Error })
	g.Go(func() error { return h.db.WithContext(gctx).Where("shop_id = ? AND customer_email = ?", shopID, cust.Email).Order("created_at DESC").Find(&reviews).Error })
	g.Go(func() error { return h.db.WithContext(gctx).Where("shop_id = ? AND customer_email = ?", shopID, cust.Email).Order("created_at DESC").Find(&checkouts).Error })
	g.Go(func() error { return h.db.WithContext(gctx).Where("shop_id = ? AND customer_email = ?", shopID, cust.Email).Order("sent_at DESC").Find(&sends).Error })
	g.Go(func() error { return h.db.WithContext(gctx).Where("shop_id = ? AND customer_email = ?", shopID, cust.Email).Find(&recoveries).Error })
	g.Go(func() error {
		err := h.db.WithContext(gctx).Where("shop_id = ? AND customer_id = ?", shopID, cust.PlatformID).First(&profile).Error
		if errors.Is(err, gorm.ErrRecordNotFound) { return nil }
		hasProfile = err == nil
		return err
	})
	if err := g.Wait(); err != nil {
		slog.Error("customer360: concurrent fetch failed", "customer_id", cid, "err", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Failed to fetch customer data")
		return
	}
	// Query cart recovery codes only for this customer's checkouts (post-group, uses checkout IDs)
	if len(checkouts) > 0 {
		coIDs := make([]uuid.UUID, len(checkouts))
		for i, co := range checkouts { coIDs[i] = co.ID }
		h.db.WithContext(ctx).Where("shop_id = ? AND checkout_id IN ?", shopID, coIDs).Find(&codes)
	}

	// 3. Build checkout-send-code maps
	type checkoutMeta struct {
		recoverySent   bool
		recoveryOpened bool
		discountUsed   bool
	}
	checkoutMetaMap := make(map[uuid.UUID]*checkoutMeta)
	for _, s := range sends {
		if _, ok := checkoutMetaMap[s.CheckoutID]; !ok {
			checkoutMetaMap[s.CheckoutID] = &checkoutMeta{}
		}
		m := checkoutMetaMap[s.CheckoutID]
		if s.Status == "sent" || s.Status == "opened" { m.recoverySent = true }
		if s.Status == "opened" { m.recoveryOpened = true }
	}
	codeByCheckout := make(map[uuid.UUID][]model.CartRecoveryCode)
	for _, c := range codes {
		codeByCheckout[c.CheckoutID] = append(codeByCheckout[c.CheckoutID], c)
	}
	for coID, clist := range codeByCheckout {
		for _, c := range clist {
			if c.IsUsed {
				if _, ok := checkoutMetaMap[coID]; !ok { checkoutMetaMap[coID] = &checkoutMeta{} }
				checkoutMetaMap[coID].discountUsed = true
			}
		}
	}

	// 4. Compute Vela-attributed revenue
	velaOrderIDs := make(map[string]bool) // Shopify order platform IDs
	// From review recovery attribution
	for _, ra := range recoveries { velaOrderIDs[ra.RepurchaseOrderID] = true }
	// From cart recovery codes (used)
	for _, c := range codes {
		if c.IsUsed && c.OrderID != "" { velaOrderIDs[c.OrderID] = true }
	}
	// TODO: content attribution — add when ContentAttribution has customer_email field

	var velaRevenue float64
	for _, o := range orders {
		if velaOrderIDs[o.PlatformID] {
			velaRevenue += o.TotalPrice
		}
	}

	// 5. Build views
	avgOrder := 0.0
	if len(orders) > 0 { avgOrder = cust.TotalSpent / float64(len(orders)) }

	var lastOrderAt *string
	if len(orders) > 0 {
		t := orders[0].CreatedAt.Format(time.RFC3339)
		lastOrderAt = &t
	}

	negCount := 0
	orderViews := make([]OrderView, len(orders))
	for i, o := range orders {
		orderViews[i] = OrderView{
			ID: o.PlatformID, OrderNumber: o.OrderNumber,
			TotalPrice: o.TotalPrice, Currency: o.Currency,
			Status: o.FinancialStatus, Fulfillment: o.FulfillmentStatus,
			CreatedAt: o.CreatedAt.Format(time.RFC3339),
			VelaAttributed: velaOrderIDs[o.PlatformID],
		}
	}

	reviewViews := make([]ReviewView, len(reviews))
	for i, rv := range reviews {
		if rv.Rating <= 3 { negCount++ }
		reviewViews[i] = ReviewView{
			ID: rv.ID.String(), Rating: rv.Rating,
			Title: rv.ReviewTitle, Body: truncate(rv.ReviewBody, 200),
			Status: rv.Status, FinalReply: truncate(rv.FinalReply, 100),
			DiscountCode: rv.DiscountCode, DiscountUsed: rv.DiscountUsed,
			Severity: rv.Severity, Issues: rv.Issues,
			CreatedAt: rv.CreatedAt.Format(time.RFC3339),
		}
	}

	abandonedCount := 0
	checkoutViews := make([]CheckoutView, 0, len(checkouts))
	for _, co := range checkouts {
		if co.Status == "abandoned" { abandonedCount++ }
		meta := checkoutMetaMap[co.ID]
		cv := CheckoutView{
			ID: co.ID.String(), Status: co.Status, TotalPrice: co.TotalPrice,
			CreatedAt: co.CreatedAt.Format(time.RFC3339),
		}
		if meta != nil {
			cv.RecoverySent = meta.recoverySent
			cv.RecoveryOpened = meta.recoveryOpened
			cv.DiscountUsed = meta.discountUsed
		}
		checkoutViews = append(checkoutViews, cv)
	}

	// Chat profile
	var chatView *ChatProfileView
	if hasProfile {
		cv := &ChatProfileView{
			SizePreference: profile.SizePreference,
			PriceSensitivity: profile.PriceSensitivity,
			IntentStrength: profile.IntentStrength,
			DiscountRequests: profile.DiscountRequests,
			PurchasesViaChat: profile.PurchasesViaChat,
			TotalChatSessions: profile.TotalChatSessions,
		}
		json.Unmarshal(profile.ColorPreferences, &cv.ColorPreferences)
		json.Unmarshal(profile.StylePreferences, &cv.StylePreferences)
		json.Unmarshal(profile.CategoryInterests, &cv.CategoryInterests)
		if profile.BudgetRange != nil {
			var budget struct{ Min, Max float64 }
			if json.Unmarshal(profile.BudgetRange, &budget) == nil {
				cv.BudgetMin = &budget.Min; cv.BudgetMax = &budget.Max
			}
		}
		if profile.FirstSeenAt != nil { t := profile.FirstSeenAt.Format(time.RFC3339); cv.FirstSeen = &t }
		if profile.LastSeenAt != nil { t := profile.LastSeenAt.Format(time.RFC3339); cv.LastSeen = &t }
		chatView = cv
	}

	// 6. Density score
	density := computeDensityScore(len(orders), reviews, checkouts, hasProfile, velaRevenue, &cust)

	// 7. Vela tags
	velaTags := computeVelaTags(negCount > 0, velaRevenue > 0, chatView, &cust)

	// 8. Timeline
	timeline := buildTimeline(orders, reviews, checkouts, sends, codes, velaOrderIDs)

	// 9. Action suggestions
	suggestions := generateSuggestions(shopID, &cust, orders, reviews, checkouts, chatView, velaRevenue)

	resp := CustomerProfile{
		ID: cust.ID.String(), PlatformID: cust.PlatformID,
		Email: cust.Email, Name: strings.TrimSpace(cust.FirstName+" "+cust.LastName),
		OrdersCount: cust.OrdersCount, TotalSpent: cust.TotalSpent,
		Tags: parseTags(cust.Tags), Velatags: velaTags,
		DensityScore: density,
		Summary: ProfileSummary{
			LifetimeValue: cust.TotalSpent, OrderCount: cust.OrdersCount,
			AvgOrderValue: math.Round(avgOrder*100)/100,
			LastOrderAt: lastOrderAt, NegativeReviewCount: negCount,
			AbandonedCartCount: abandonedCount,
			ChatSessions: func() int { if chatView != nil { return chatView.TotalChatSessions }; return 0 }(),
			VelaInfluencedRevenue: math.Round(velaRevenue*100)/100,
		},
		ChatProfile: chatView,
		Orders: orderViews, Reviews: reviewViews, Checkouts: checkoutViews,
		Timeline: timeline, Suggestions: suggestions,
	}

	httputil.WriteOK(w, map[string]interface{}{"success": true, "profile": resp})
}

// ── Density Score ──────────────────────────────────────────────────────────────

func computeDensityScore(orderCount int, reviews []model.ReplyRecord, checkouts []model.SyncedCheckout, hasProfile bool, velaRevenue float64, cust *model.SyncedCustomer) DensityScore {
	dims := []DimBreakdown{
		{Name: "order_history", Weight: 20, Score: min(orderCount*7, 20), Status: statusFor(orderCount > 0, orderCount >= 3)},
		{Name: "reviews", Weight: 15, Score: scoreReviews(reviews), Status: statusFor(len(reviews) > 0, hasRepliedReview(reviews))},
		{Name: "checkouts", Weight: 15, Score: scoreCheckouts(checkouts), Status: statusFor(len(checkouts) > 0, hasRecoveredCheckout(checkouts))},
		{Name: "chat", Weight: 20, Score: scoreChat(hasProfile), Status: statusFor(hasProfile, hasProfile)},
		{Name: "vci", Weight: 15, Score: scoreVCI(cust), Status: statusFor(cust.Tags != "", len(parseTags(cust.Tags)) >= 3)},
		{Name: "attribution", Weight: 10, Score: condScore(velaRevenue > 0, 10, 0), Status: statusFor(velaRevenue > 0, velaRevenue >= 50)},
		{Name: "repurchase", Weight: 5, Score: condScore(velaRevenue > 0, 5, 0), Status: statusFor(velaRevenue > 0, velaRevenue > 0)},
	}
	total := 0; for _, d := range dims { total += d.Score }
	grade := "thin"
	switch {
	case total >= 80: grade = "rich"
	case total >= 50: grade = "good"
	case total >= 25: grade = "basic"
	}
	return DensityScore{Score: total, Dimensions: dims, Grade: grade}
}

func scoreReviews(reviews []model.ReplyRecord) int {
	score := 0
	for _, r := range reviews {
		if r.Status == "sent" { score += 5 }
		if r.DiscountUsed { score += 5 }
	}
	return min(score, 15)
}

func scoreCheckouts(checkouts []model.SyncedCheckout) int {
	score := 0
	for _, c := range checkouts {
		if c.Status == "recovered" { score += 10 }
	}
	return min(score, 15)
}

func scoreChat(hasProfile bool) int {
	if hasProfile { return 20 }
	return 0
}

func scoreVCI(cust *model.SyncedCustomer) int {
	tags := parseTags(cust.Tags)
	if len(tags) >= 3 { return 15 }
	if len(tags) >= 1 { return 8 }
	return 0
}

func hasRepliedReview(reviews []model.ReplyRecord) bool {
	for _, r := range reviews { if r.Status == "sent" { return true } }
	return false
}

func hasRecoveredCheckout(checkouts []model.SyncedCheckout) bool {
	for _, c := range checkouts { if c.Status == "recovered" { return true } }
	return false
}

func statusFor(has bool, full bool) string {
	if full { return "full" }
	if has { return "partial" }
	return "empty"
}

func condScore(cond bool, ifTrue, ifFalse int) int {
	if cond { return ifTrue }
	return ifFalse
}

// ── Vela Tags ──────────────────────────────────────────────────────────────────

func computeVelaTags(hasNegative bool, hasVelaRevenue bool, chat *ChatProfileView, cust *model.SyncedCustomer) []string {
	var tags []string
	if hasNegative && hasVelaRevenue { tags = append(tags, "recovered") }
	if chat != nil && chat.PriceSensitivity == "H" { tags = append(tags, "price_sensitive") }
	if chat != nil && chat.IntentStrength == "H" { tags = append(tags, "high_intent") }
	if chat != nil && chat.DiscountRequests >= 2 { tags = append(tags, "discount_seeker") }
	if chat != nil && chat.TotalChatSessions >= 5 { tags = append(tags, "chat_active") }
	if cust.OrdersCount >= 5 { tags = append(tags, "loyal") }
	if cust.TotalSpent >= 500 { tags = append(tags, "high_value") }
	if cust.OrdersCount == 0 || cust.TotalSpent == 0 { tags = append(tags, "new") }
	return tags
}

func computeVelaScore(cp *model.CustomerChatProfile, orderCount int, totalSpent float64) float64 {
	score := float64(orderCount) * 5 + totalSpent / 50 + float64(cp.TotalChatSessions) * 3
	return math.Round(score*10) / 10
}

func sumAttributionRevenue(ctx context.Context, db *gorm.DB, shopID uuid.UUID, customerID, email string, total *float64) {
	var reviewSum, cartSum struct{ Total float64 }
	db.WithContext(ctx).Model(&model.ReviewRecoveryAttribution{}).Select("COALESCE(SUM(repurchase_amount), 0) AS total").Where("shop_id = ? AND customer_email = ?", shopID, email).Scan(&reviewSum)
	db.WithContext(ctx).Model(&model.CartRecoveryCode{}).Select("COALESCE(SUM(order_total), 0) AS total").Where("shop_id = ? AND is_used = true", shopID).Scan(&cartSum) // approximate — matches by checkout email later
	*total = reviewSum.Total
}

// ── Timeline ────────────────────────────────────────────────────────────────────

func buildTimeline(orders []model.SyncedOrder, reviews []model.ReplyRecord, checkouts []model.SyncedCheckout, sends []model.CartRecoverySend, codes []model.CartRecoveryCode, velaOrderIDs map[string]bool) []TimelineEvent {
	var events []TimelineEvent

	// Build send/code lookup
	sendByCheckout := make(map[uuid.UUID][]model.CartRecoverySend)
	for _, s := range sends { sendByCheckout[s.CheckoutID] = append(sendByCheckout[s.CheckoutID], s) }
	codeByCheckout := make(map[uuid.UUID][]model.CartRecoveryCode)
	for _, c := range codes { codeByCheckout[c.CheckoutID] = append(codeByCheckout[c.CheckoutID], c) }

	for _, o := range orders {
		v := velaOrderIDs[o.PlatformID]
		pri := "background"
		if v { pri = "critical" }
		summary := fmt.Sprintf("Order #%d — $%.2f", o.OrderNumber, o.TotalPrice)
		detail := summary
		if v { detail += " · Vela influenced" }
		events = append(events, TimelineEvent{Timestamp: o.CreatedAt, Type: "order", Priority: pri, Summary: summary, Detail: detail, Velatouch: v})
	}

	for _, rv := range reviews {
		pri := "important"
		if rv.Rating <= 3 { pri = "critical" }
		summary := fmt.Sprintf("Review %s ★%.0f", rv.ReviewTitle, rv.Rating)
		detail := summary
		if rv.FinalReply != "" { detail += " · AI replied" }
		if rv.DiscountCode != "" { detail += " · compensation: " + rv.DiscountCode }
		vt := rv.FinalReply != ""
		events = append(events, TimelineEvent{Timestamp: rv.CreatedAt, Type: "review", Priority: pri, Summary: summary, Detail: detail, Velatouch: vt})

		// Auto-reply sent event (separate timeline entry)
		if rv.SentAt != nil {
			events = append(events, TimelineEvent{Timestamp: *rv.SentAt, Type: "auto_reply", Priority: "important",
				Summary: "Auto-reply sent", Detail: truncate(rv.FinalReply, 80), Velatouch: true})
		}
	}

	for _, co := range checkouts {
		pri := "background"
		if co.Status == "abandoned" { pri = "important" }
		summary := fmt.Sprintf("Cart $%.2f · %s", co.TotalPrice, co.Status)
		detail := summary
		vt := false
		// Check if recovery was attempted
		for _, s := range sendByCheckout[co.ID] {
			if s.Status == "sent" || s.Status == "opened" {
				vt = true; pri = "important"
				detail += " · recovery email sent"
				break
			}
		}
		for _, c := range codeByCheckout[co.ID] {
			if c.IsUsed { pri = "critical"; detail += " · code used ✓"; vt = true; break }
		}
		events = append(events, TimelineEvent{Timestamp: co.CreatedAt, Type: "checkout", Priority: pri, Summary: summary, Detail: detail, Velatouch: vt})
	}

	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.After(events[j].Timestamp) })

	// Insert Vela boundary marker
	for i := range events {
		if events[i].Velatouch {
			boundary := TimelineEvent{Timestamp: events[i].Timestamp.Add(1 * time.Minute), Type: "vela_boundary", Priority: "background", Summary: "── Vela AI 介入 ──", Detail: "Vela started interacting with this customer"}
			events = append(events[:i], append([]TimelineEvent{boundary}, events[i:]...)...)
			break
		}
	}

	return events
}

// ── Action Suggestions ─────────────────────────────────────────────────────────

func generateSuggestions(shopID uuid.UUID, cust *model.SyncedCustomer, orders []model.SyncedOrder, reviews []model.ReplyRecord, checkouts []model.SyncedCheckout, chat *ChatProfileView, velaRevenue float64) []ActionSuggestion {
	var actions []ActionSuggestion

	// Rule 1: Has negative review with unused discount → manual follow-up
	for _, rv := range reviews {
		if rv.Rating <= 3 && rv.DiscountCode != "" && !rv.DiscountUsed && rv.SentAt != nil {
			daysSince := int(time.Since(*rv.SentAt).Hours() / 24)
			if daysSince > 7 {
				actions = append(actions, ActionSuggestion{
					Priority: "high",
					Action:   fmt.Sprintf("差评回复 %d 天未回购", daysSince),
					Reason:   fmt.Sprintf("已回复差评并发 15%% 折扣码 %s，但未使用。顾客可能还在观望。", rv.DiscountCode),
					CTA:      "发送跟进邮件",
				})
			}
			break // one suggestion per customer for this rule
		}
	}

	// Rule 2: High-value abandoned checkout not recovered
	var highestAbandoned float64
	for _, co := range checkouts {
		if co.Status == "abandoned" && co.TotalPrice > highestAbandoned {
			highestAbandoned = co.TotalPrice
		}
	}
	if highestAbandoned >= 100 {
		actions = append(actions, ActionSuggestion{
			Priority: "high",
			Action:   fmt.Sprintf("$%.0f 弃购未挽回", highestAbandoned),
			Reason:   "高价值弃购，已发挽回邮件但未打开。建议加大折扣或尝试其他渠道。",
			CTA:      "调整挽回策略",
		})
	}

	// Rule 3: Chat active but never purchased through chat
	if chat != nil && chat.TotalChatSessions >= 3 && chat.PurchasesViaChat == 0 {
		actions = append(actions, ActionSuggestion{
			Priority: "medium",
			Action:   fmt.Sprintf("对话 %d 次未转化", chat.TotalChatSessions),
			Reason:   "多次对话但未在 AI 导购中下单。价格敏感？在等折扣？建议查看对话记录分析原因。",
			CTA:      "查看对话摘要",
		})
	}

	// Rule 4: High LTV customer churning
	if cust.TotalSpent >= 200 && len(orders) > 0 {
		daysSince := int(time.Since(orders[0].CreatedAt).Hours() / 24)
		if daysSince > 60 {
			actions = append(actions, ActionSuggestion{
				Priority: "high",
				Action:   "高价值客户可能流失",
				Reason:   fmt.Sprintf("LTV $%.0f，距上次购买 %d 天。建议主动联系或发送专属折扣。", cust.TotalSpent, daysSince),
				CTA:      "发送专属折扣",
			})
		}
	}

	// Rule 5: Frequent discount requests → educate or upsell
	if chat != nil && chat.DiscountRequests >= 3 {
		actions = append(actions, ActionSuggestion{
			Priority: "low",
			Action:   "频繁请求折扣",
			Reason:   fmt.Sprintf("已请求折扣 %d 次。建议在下次对话中先解释产品价值而非直接给折扣。", chat.DiscountRequests),
			CTA:      "调整导购策略",
		})
	}

	if actions == nil { actions = []ActionSuggestion{} }
	return actions
}

// ── Helpers ─────────────────────────────────────────────────────────────────────

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen { return s }
	return s[:maxLen] + "..."
}

func parseTags(tagStr string) []string {
	if tagStr == "" { return nil }
	parts := strings.Split(tagStr, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" { out = append(out, p) }
	}
	return out
}

func min(a, b int) int {
	if a < b { return a }
	return b
}

// ── Legacy: Get (kept for backward compat) ─────────────────────────────────────

func (h *Customer360Handler) Get(w http.ResponseWriter, r *http.Request) {
	if h.db == nil { httputil.WriteError(w, http.StatusServiceUnavailable, "DB unavailable"); return }
	sid := r.URL.Query().Get("shop_id"); shopID, _ := uuid.Parse(sid)
	cid := chi.URLParam(r, "customerID")
	var c model.SyncedCustomer
	query := h.db.WithContext(r.Context()).Where("shop_id = ?", shopID)
	if _, parseErr := uuid.Parse(cid); parseErr == nil {
		query = query.Where("id = ? OR platform_id = ?", cid, cid)
	} else {
		query = query.Where("platform_id = ?", cid)
	}
	if err := query.First(&c).Error; err != nil { httputil.WriteError(w, http.StatusNotFound, "Customer not found"); return }
	httputil.WriteOK(w, map[string]interface{}{
		"success": true,
		"customer": map[string]interface{}{
			"id": c.ID, "email": c.Email, "name": strings.TrimSpace(c.FirstName+" "+c.LastName),
			"orders": c.OrdersCount, "spent": c.TotalSpent, "tags": c.Tags,
		},
	})
}
