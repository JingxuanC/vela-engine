package salesagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/JingxuanC/vela-engine/internal/model"
	"gorm.io/datatypes"
)

// ── searchProducts ───────────────────────────────────────────────────────────

// searchProducts queries synced_products for matching items.
// For shops with >10K products, add a GIN trigram index on synced_products.title.
func (a *SalesAgent) searchProducts(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	shopID := getStringArg(args, "shop_id")
	query := strings.TrimSpace(getStringArg(args, "query"))
	color := strings.TrimSpace(getStringArg(args, "color"))
	maxPrice := getFloatArg(args, "max_price")

	// Customer preference context for ranking (Phase 2)
	customerSize := getStringArg(args, "customer_size")
	customerColors := getStringArg(args, "customer_colors")
	customerPriceSensitive := getStringArg(args, "customer_price_sensitive")

	// Guard: reject empty or absurdly long queries
	if query == "" {
		slog.Warn("salesagent: searchProducts called with empty query", "shop_id", shopID)
		return map[string]interface{}{"products": []ProductResult{}}, nil
	}
	if len(query) > 200 {
		query = query[:200]
	}
	if len(color) > 50 {
		color = color[:50]
	}

	// Escape LIKE wildcards in user input (% and _)
	query = escapeLikeWildcards(query)
	color = escapeLikeWildcards(color)

	if a.db == nil {
		return nil, fmt.Errorf("search products: database unavailable")
	}

	var products []model.SyncedProduct
	db := a.db.WithContext(ctx).
		Table("synced_products").
		Where("shop_id = ?", shopID).
		Where("status = ?", "active").
		Where("title ILIKE ? ESCAPE '\\' OR product_type ILIKE ? ESCAPE '\\'",
			"%"+query+"%", "%"+query+"%").
		Limit(5)

	if color != "" {
		db = db.Where("options::text ILIKE ? ESCAPE '\\'", "%"+color+"%")
	}
	db.Find(&products)
	if db.Error != nil {
		return nil, fmt.Errorf("search products query failed: %w", db.Error)
	}

	results := make([]ProductResult, 0, len(products))
	for _, p := range products {
		variants := parseVariantList(p.Variants)
		available := filterAvailableVariants(variants)
		if maxPrice > 0 {
			available = filterByMaxPrice(available, maxPrice)
		}
		if len(available) == 0 {
			continue
		}
		results = append(results, ProductResult{
			ID:       p.PlatformID,
			Title:    p.Title,
			Price:    formatVariantPrices(available),
			ImageURL: firstImageURL(p.Images),
			Variants: formatVariantOptions(available),
			InStock:  true,
		})
	}
	if results == nil {
		results = []ProductResult{}
	}

	// Phase 2: Re-rank results by customer preferences
	if len(results) > 1 {
		results = rankByPreferences(results, customerSize, customerColors, customerPriceSensitive)
	}

	return map[string]interface{}{"products": results}, nil
}

// rankByPreferences reorders product results based on customer preferences.
func rankByPreferences(results []ProductResult, size, colors, priceSensitive string) []ProductResult {
	if size == "" && colors == "" && priceSensitive == "" {
		return results
	}

	// Score each product based on preference matches
	type scoredItem struct {
		result ProductResult
		score  int
	}
	items := make([]scoredItem, len(results))
	for i, r := range results {
		s := 0
		// Size match: variants contain the preferred size
		if size != "" {
			for _, v := range r.Variants {
				if strings.Contains(strings.ToUpper(v), strings.ToUpper(size)) {
					s += 3
					break
				}
			}
		}
		// Color match: title or variants mention preferred color
		if colors != "" {
			for _, c := range strings.Split(colors, ",") {
				if strings.Contains(strings.ToLower(r.Title), strings.ToLower(strings.TrimSpace(c))) {
					s += 2
					break
				}
			}
		}
		items[i] = scoredItem{result: r, score: s}
	}

	// Sort: higher score first; if price sensitive, lower price within same score
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		// Tie-break: price-sensitive customers see cheaper first (numeric compare)
		if priceSensitive == "H" || priceSensitive == "true" {
			pi, _ := strconv.ParseFloat(strings.TrimPrefix(items[i].result.Price, "$"), 64)
			pj, _ := strconv.ParseFloat(strings.TrimPrefix(items[j].result.Price, "$"), 64)
			return pi < pj
		}
		return false
	})

	ranked := make([]ProductResult, len(items))
	for i, s := range items {
		ranked[i] = s.result
	}
	return ranked
}

// ── generateLink ─────────────────────────────────────────────────────────────

// generateLink creates a Shopify product URL with UTM tracking.
// Uses Shopify numeric ID — Shopify redirects to canonical handle URL.
func (a *SalesAgent) generateLink(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	shopID := getStringArg(args, "shop_id")
	productID := getStringArg(args, "product_id")

	numericID := extractShopifyNumericID(productID)
	if numericID == "" {
		slog.Warn("salesagent: generateLink with empty product_id", "shop_id", shopID, "product_id", productID)
		return nil, fmt.Errorf("invalid product identifier")
	}
	if a.db == nil {
		return nil, fmt.Errorf("generate link: database unavailable")
	}

	var shop model.Shop
	err := a.db.WithContext(ctx).Table("shops").
		Where("id = ?", shopID).
		Select("shop_domain").
		First(&shop).Error
	if err != nil {
		return nil, fmt.Errorf("shop not found: %w", err)
	}

	url := fmt.Sprintf(
		"https://%s/products/%s?utm_source=vela_ai_chat&utm_medium=ai_chat&utm_campaign=sales_agent",
		shop.Domain, numericID,
	)
	return map[string]interface{}{"url": url}, nil
}

// ── generateDiscount ─────────────────────────────────────────────────────────

// generateDiscount creates a one-time discount code via Shopify Admin REST API.
// Calls Shopify PriceRule + DiscountCode APIs to generate a real, usable code.
// Falls back to a hardcoded informational code if the shop has no access token.
func (a *SalesAgent) generateDiscount(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	shopID := getStringArg(args, "shop_id")
	customerEmail := getStringArg(args, "customer_email")
	customerID := getStringArg(args, "customer_id")
	sessionID := getStringArg(args, "session_id")

	if a.db == nil {
		slog.Warn("salesagent: generateDiscount called with nil db", "shop_id", shopID)
		return a.fallbackDiscount(ctx)
	}

	// Rate limit: max 1 active discount per session (24h TTL)
	sessionKey := fmt.Sprintf("discount_given:%s", sessionID)
	customerKey := fmt.Sprintf("discount_given_customer:%s:%s", shopID, customerID)
	// hasPersistentID: true for real Shopify customers and anon_ device IDs (localStorage-persisted)
	// guest_ IDs are non-persistent (regenerated every request)
	hasPersistentID := customerID != "" && !strings.HasPrefix(customerID, "guest_")

	if a.cache != nil {
		if exists, _ := a.cache.Get(ctx, sessionKey); exists != "" {
			slog.Warn("salesagent: discount already given for session", "session_id", sessionID)
			return map[string]interface{}{
				"message": "A discount has already been provided for this conversation.",
			}, nil
		}
		// Per-customer limit: logged-in users get max 1 active discount across all sessions
		if hasPersistentID {
			if exists, _ := a.cache.Get(ctx, customerKey); exists != "" {
				slog.Warn("salesagent: discount already active for customer", "customer_id", customerID)
				return map[string]interface{}{
					"message": "You already have an active discount code. It will be valid for 24 hours from when it was created.",
				}, nil
			}
		}
	}

	var shop model.Shop
	err := a.db.WithContext(ctx).Table("shops").
		Where("id = ?", shopID).
		Select("shop_domain, access_token").
		First(&shop).Error
	if err != nil {
		slog.Warn("salesagent: shop not found for discount", "shop_id", shopID, "err", err)
		return a.fallbackDiscount(ctx)
	}

	// If no access token, fall back to informational code
	if "" == "" {
		slog.Warn("salesagent: shop has no access token, using fallback discount", "shop_id", shopID)
		return a.fallbackDiscount(ctx)
	}

	code := generateDiscountCode()
	expiresAt := time.Now().Add(24 * time.Hour)

	// Step 1: Create Shopify PriceRule (customer-scoped if we have their Shopify ID)
	priceRuleID, err := a.createShopifyPriceRule(ctx, shop.Domain, "", expiresAt, customerID)
	if err != nil {
		slog.Error("salesagent: failed to create price rule", "shop_id", shopID, "err", err)
		return a.fallbackDiscount(ctx)
	}

	// Step 2: Create discount code under the price rule
	err = a.createShopifyDiscountCode(ctx, shop.Domain, "", priceRuleID, code, customerEmail)
	if err != nil {
		slog.Error("salesagent: failed to create discount code, cleaning up price rule", "shop_id", shopID, "price_rule_id", priceRuleID, "err", err)
		// Clean up orphaned price rule
		a.deleteShopifyPriceRule(ctx, shop.Domain, "", priceRuleID)
		return a.fallbackDiscount(ctx)
	}

	slog.Info("salesagent: discount code created", "shop_id", shopID, "code", code)

	// Mark rate limit only AFTER successful creation (not on fallback)
	if a.cache != nil {
		a.cache.Set(ctx, sessionKey, "1", 24*time.Hour)
		if hasPersistentID {
			a.cache.Set(ctx, customerKey, "1", 24*time.Hour)
		}
	}

	return map[string]interface{}{
		"code":       code,
		"amount":     "15%",
		"expires_at": expiresAt.Format(time.RFC3339),
	}, nil
}

// fallbackDiscount returns a hardcoded informational code when Shopify API is unavailable.
func (a *SalesAgent) fallbackDiscount(ctx context.Context) (interface{}, error) {
	expiresAt := time.Now().Add(24 * time.Hour)
	return map[string]interface{}{
		"code":       "VELA-SAVE15",
		"amount":     "15%",
		"expires_at": expiresAt.Format(time.RFC3339),
	}, nil
}

// createShopifyPriceRule creates a percentage discount price rule in Shopify.
// Returns the price_rule ID needed to create discount codes.
func (a *SalesAgent) createShopifyPriceRule(ctx context.Context, domain, token string, expiresAt time.Time, customerID string) (string, error) {
	// customer_selection strategy:
	// - Real Shopify customer ID (numeric): "prerequisite" + prerequisite_customer_ids
	// - Device IDs (anon_*) / guest IDs (guest_*): "all" + random unguessable code + usage_limit:1
	isRealCustomer := customerID != "" &&
		!strings.HasPrefix(customerID, "guest_") &&
		!strings.HasPrefix(customerID, "anon_")

	customerSelection := `"customer_selection": "all"`
	if isRealCustomer {
		customerSelection = fmt.Sprintf(`"customer_selection": "prerequisite", "prerequisite_customer_ids": [%s]`, customerID)
	}

	body := fmt.Sprintf(`{
		"price_rule": {
			"title": "Vela AI Chat Discount",
			"target_type": "line_item",
			"target_selection": "all",
			"allocation_method": "across",
			"value_type": "percentage",
			"value": -15.0,
			%s,
			"usage_limit": 1,
			"starts_at": "%s",
			"ends_at": "%s"
		}
	}`, customerSelection, time.Now().UTC().Format(time.RFC3339), expiresAt.UTC().Format(time.RFC3339))

	req, err := http.NewRequestWithContext(ctx, "POST",
		fmt.Sprintf("https://%s/admin/api/2024-04/price_rules.json", domain),
		bytes.NewReader([]byte(body)))
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("shopify request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("shopify price rule: %s — %s", resp.Status, string(b))
	}

	var result struct {
		PriceRule struct {
			ID int64 `json:"id"`
		} `json:"price_rule"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode price rule response: %w", err)
	}
	return fmt.Sprintf("%d", result.PriceRule.ID), nil
}

// deleteShopifyPriceRule removes a price rule (used for cleanup on partial failure).
func (a *SalesAgent) deleteShopifyPriceRule(ctx context.Context, domain, token, priceRuleID string) {
	url := fmt.Sprintf("https://%s/admin/api/2024-04/price_rules/%s.json", domain, priceRuleID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		slog.Warn("salesagent: failed to delete orphaned price rule", "price_rule_id", priceRuleID, "err", err)
		return
	}
	resp.Body.Close()
}

// createShopifyDiscountCode creates a discount code under a price rule.
func (a *SalesAgent) createShopifyDiscountCode(ctx context.Context, domain, token, priceRuleID, code, customerEmail string) error {
	body := fmt.Sprintf(`{"discount_code": {"code": "%s"}}`, code)
	url := fmt.Sprintf("https://%s/admin/api/2024-04/price_rules/%s/discount_codes.json", domain, priceRuleID)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	req.Header.Set("X-Shopify-Access-Token", token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("shopify request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("shopify discount code: %s — %s", resp.Status, string(b))
	}
	return nil
}

// generateDiscountCode creates a random discount code: VELA-XXXXXXXX (4 bytes → 8 hex chars)
func generateDiscountCode() string {
	b := make([]byte, 4)
	rand.Read(b)
	return "VELA-" + strings.ToUpper(hex.EncodeToString(b))
}

// ── Session ──────────────────────────────────────────────────────────────────

// getSession loads or creates the chat session for the current customer.
// Redis key: chat_session:{shopID}:{customerID}, TTL 24h.
func (a *SalesAgent) getAgentSession(ctx context.Context, shopID, customerID string) (*ChatSession, error) {
	key := fmt.Sprintf("chat_session:%s:%s", shopID, customerID)
	data, err := a.cache.Get(ctx, key)
	if err != nil || data == "" {
		return &ChatSession{
			ShopID:     shopID,
			CustomerID: customerID,
			Stage:      "greeting",
			CreatedAt:  time.Now(),
		}, nil
	}
	var sess ChatSession
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return &ChatSession{
			ShopID:     shopID,
			CustomerID: customerID,
			Stage:      "greeting",
			CreatedAt:  time.Now(),
		}, nil
	}
	return &sess, nil
}

// saveSession persists the chat session to Redis with 24h TTL.
func (a *SalesAgent) saveAgentSession(ctx context.Context, sess *ChatSession) error {
	if len(sess.MessageHistory) > maxHistoryMessages {
		sess.MessageHistory = sess.MessageHistory[len(sess.MessageHistory)-maxHistoryMessages:]
	}
	sess.UpdatedAt = time.Now()
	data, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	key := fmt.Sprintf("chat_session:%s:%s", sess.ShopID, sess.CustomerID)
	return a.cache.Set(ctx, key, string(data), 24*time.Hour)
}

// ── Variant Helpers ──────────────────────────────────────────────────────────

type shopifyVariant struct {
	ID                int64   `json:"id"`
	Title             string  `json:"title"`
	Price             string  `json:"price"`
	CompareAtPrice    *string `json:"compare_at_price"`
	InventoryQuantity int     `json:"inventory_quantity"`
}

type shopifyImage struct {
	Src string `json:"src"`
}

func parseVariantList(raw datatypes.JSON) []shopifyVariant {
	var v []shopifyVariant
	if len(raw) > 0 {
		json.Unmarshal(raw, &v)
	}
	return v
}

func filterAvailableVariants(variants []shopifyVariant) []shopifyVariant {
	var out []shopifyVariant
	for _, v := range variants {
		if v.InventoryQuantity > 0 {
			out = append(out, v)
		}
	}
	return out
}

func formatVariantPrices(variants []shopifyVariant) string {
	if len(variants) == 0 {
		return "0"
	}
	// Parse prices to floats for numeric sort, fall back to string order
	type priced struct {
		price float64
		raw   string
	}
	parsed := make([]priced, 0, len(variants))
	for _, v := range variants {
		f, err := strconv.ParseFloat(v.Price, 64)
		if err != nil {
			f = 0
		}
		parsed = append(parsed, priced{price: f, raw: v.Price})
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].price < parsed[j].price })

	if len(parsed) == 1 {
		return "$" + parsed[0].raw
	}
	return fmt.Sprintf("$%s – $%s", parsed[0].raw, parsed[len(parsed)-1].raw)
}

func formatVariantOptions(variants []shopifyVariant) []string {
	out := make([]string, len(variants))
	for i, v := range variants {
		out[i] = fmt.Sprintf("%s - $%s", v.Title, v.Price)
	}
	return out
}

func firstImageURL(raw datatypes.JSON) string {
	var imgs []shopifyImage
	if len(raw) > 0 {
		json.Unmarshal(raw, &imgs)
	}
	if len(imgs) > 0 {
		return imgs[0].Src
	}
	return ""
}

// ── Argument Helpers ─────────────────────────────────────────────────────────

func getStringArg(args map[string]interface{}, key string) string {
	v, ok := args[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return fmt.Sprint(v)
	}
	return s
}

func extractShopifyNumericID(gid string) string {
	idx := strings.LastIndex(gid, "/")
	if idx >= 0 {
		return gid[idx+1:]
	}
	return gid
}

// escapeLikeWildcards escapes % and _ for PostgreSQL ILIKE queries.
func escapeLikeWildcards(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`) // escape the escape char first
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// getFloatArg extracts a float64 value from tool args.
func getFloatArg(args map[string]interface{}, key string) float64 {
	v, ok := args[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0
		}
		return f
	}
	return 0
}

// ── analyzeReturns ─────────────────────────────────────────────────────────────

func (a *SalesAgent) analyzeReturns(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	shopID := getStringArg(args, "shop_id")
	period := getStringArg(args, "period")
	if period == "" { period = "30d" }
	if a.db == nil { return nil, fmt.Errorf("database unavailable") }

	var totalOrders, totalReturns int64
	a.db.WithContext(ctx).Table("synced_orders").Where("shop_id = ?", shopID).Count(&totalOrders)
	a.db.WithContext(ctx).Table("returns").Where("shop_id = ?", shopID).Count(&totalReturns)

	rate := 0.0
	if totalOrders > 0 { rate = float64(totalReturns) / float64(totalOrders) * 100 }

	type reasonRow struct { Reason string; Count int64 }
	var reasons []reasonRow
	a.db.WithContext(ctx).Table("returns").Select("return_reason as reason, COUNT(*) as count").Where("shop_id = ?", shopID).Group("return_reason").Order("count DESC").Limit(5).Scan(&reasons)

	top := make([]map[string]interface{}, 0, len(reasons))
	for _, r := range reasons {
		top = append(top, map[string]interface{}{"reason": r.Reason, "count": r.Count})
	}

	return map[string]interface{}{
		"total_orders":  totalOrders,
		"total_returns": totalReturns,
		"return_rate":   fmt.Sprintf("%.1f%%", rate),
		"top_reasons":   top,
	}, nil
}

// ── sendEmail ─────────────────────────────────────────────────────────────────

func (a *SalesAgent) sendEmail(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	to := getStringArg(args, "to")
	subject := getStringArg(args, "subject")
	body := getStringArg(args, "body")
	if to == "" || subject == "" || body == "" {
		return nil, fmt.Errorf("send_email requires to, subject, and body")
	}

	apiKey := os.Getenv("RESEND_API_KEY")
	fromEmail := os.Getenv("RESEND_FROM_EMAIL")
	if fromEmail == "" { fromEmail = "hello@vela.ai" }
	if apiKey == "" { return nil, fmt.Errorf("send_email: RESEND_API_KEY not configured") }

	payload := map[string]interface{}{
		"from":    fmt.Sprintf("Vela AI <%s>", fromEmail),
		"to":      []string{to},
		"subject": subject,
		"text":    body,
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.resend.com/emails", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil { return nil, fmt.Errorf("resend: %w", err) }
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("resend: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return map[string]interface{}{"sent": true, "to": to}, nil
}

// ── generateReport ─────────────────────────────────────────────────────────────

func (a *SalesAgent) generateReport(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	shopID := getStringArg(args, "shop_id")
	reportType := getStringArg(args, "type")
	if reportType == "" { reportType = "summary" }
	if a.db == nil { return nil, fmt.Errorf("database unavailable") }

	var products, orders, returns, customers int64
	a.db.WithContext(ctx).Table("synced_products").Where("shop_id = ? AND status = ?", shopID, "active").Count(&products)
	a.db.WithContext(ctx).Table("synced_orders").Where("shop_id = ?", shopID).Count(&orders)
	a.db.WithContext(ctx).Table("returns").Where("shop_id = ?", shopID).Count(&returns)
	a.db.WithContext(ctx).Table("synced_customers").Where("shop_id = ?", shopID).Count(&customers)

	return map[string]interface{}{
		"type":          reportType,
		"products":      products,
		"orders":        orders,
		"returns":       returns,
		"customers":     customers,
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// ── webSearch ─────────────────────────────────────────────────────────────────

func (a *SalesAgent) webSearch(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	query := getStringArg(args, "query")
	if query == "" { return nil, fmt.Errorf("web_search requires a query") }

	// Use DuckDuckGo Instant Answer API (free, no key required)
	url := fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1&skip_disambig=1", strings.ReplaceAll(query, " ", "+"))
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := a.httpClient.Do(req)
	if err != nil { return nil, fmt.Errorf("websearch: %w", err) }
	defer resp.Body.Close()

	var result struct {
		Abstract    string `json:"Abstract"`
		Answer      string `json:"Answer"`
		Heading     string `json:"Heading"`
		RelatedTopics []struct {
			Text string `json:"Text"`
		} `json:"RelatedTopics"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("websearch parse: %w", err)
	}

	summary := result.Abstract
	if result.Answer != "" { summary = result.Answer }
	if summary == "" && len(result.RelatedTopics) > 0 {
		summaries := make([]string, 0, 3)
		for i, t := range result.RelatedTopics {
			if i >= 3 { break }
			if t.Text != "" { summaries = append(summaries, t.Text) }
		}
		summary = strings.Join(summaries, " | ")
	}

	return map[string]interface{}{
		"query":   query,
		"summary": summary,
	}, nil
}

// filterByMaxPrice keeps only variants with price <= maxPrice.
func filterByMaxPrice(variants []shopifyVariant, maxPrice float64) []shopifyVariant {
	var out []shopifyVariant
	for _, v := range variants {
		p, err := strconv.ParseFloat(v.Price, 64)
		if err != nil {
			p = 0
		}
		if p <= maxPrice {
			out = append(out, v)
		}
	}
	return out
}

// ── getTracking ───────────────────────────────────────────────────────────────

// getTracking looks up fulfillment tracking info for an order by order number.
// Returns the latest tracking status, events, and carrier info.
func (a *SalesAgent) getTracking(ctx context.Context, args map[string]interface{}) (interface{}, error) {
	shopID := getStringArg(args, "shop_id")
	orderNumber := getStringArg(args, "order_number")

	if orderNumber == "" {
		return map[string]interface{}{"error": "Please provide an order number (e.g. #1234)."}, nil
	}

	// Strip leading # if present
	orderNumber = strings.TrimPrefix(orderNumber, "#")

	if a.db == nil {
		return map[string]interface{}{"error": "Order tracking is unavailable right now."}, nil
	}

	// Find the synced_order by shop_id and order_number
	var order model.SyncedOrder
	err := a.db.WithContext(ctx).
		Where("shop_id = ? AND order_number = ?", shopID, orderNumber).
		First(&order).Error
	if err != nil {
		return map[string]interface{}{
			"message": fmt.Sprintf("I couldn't find order #%s in our system. Double-check the order number?", orderNumber),
		}, nil
	}

	// Check if there are fulfillment events
	var events []model.FulfillmentEvent
	a.db.WithContext(ctx).
		Where("order_id = ?", order.ID).
		Order("happened_at DESC").
		Find(&events)

	result := map[string]interface{}{
		"order_number": fmt.Sprintf("#%d", order.OrderNumber),
	}

	if order.TrackingNumber != "" {
		result["tracking_number"] = order.TrackingNumber
		result["carrier"] = order.Carrier
		if order.TrackingURL != "" {
			result["tracking_url"] = order.TrackingURL
		}
	}

	if len(events) > 0 {
		latest := events[0]
		result["status"] = latest.Status
		result["latest_message"] = latest.Message
		result["latest_location"] = fmt.Sprintf("%s, %s", latest.City, latest.Province)
		if latest.EstimatedDeliveryAt != nil {
			result["estimated_delivery"] = latest.EstimatedDeliveryAt.Format("January 2")
		}

		// Include a few recent events
		recentEvents := make([]map[string]interface{}, 0, min(3, len(events)))
		for i := 0; i < len(events) && i < 3; i++ {
			e := events[i]
			evt := map[string]interface{}{
				"status":  e.Status,
				"message": e.Message,
				"date":    e.HappenedAt.Format("Jan 2"),
			}
			if e.City != "" {
				evt["location"] = fmt.Sprintf("%s, %s", e.City, e.Province)
			}
			recentEvents = append(recentEvents, evt)
		}
		result["recent_events"] = recentEvents
	} else if order.LatestEventStatus != "" {
		result["status"] = order.LatestEventStatus
	} else {
		result["message"] = fmt.Sprintf("Order #%d has been placed but hasn't shipped yet. I'll keep you updated!", order.OrderNumber)
	}

	return result, nil
}
