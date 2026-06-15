package salesagent

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// ── Intent Signal ────────────────────────────────────────────────────────────

// IntentSignal is extracted from conversation and published via EventBus.
// Rule-based extraction — no LLM call, zero additional cost.
type IntentSignal struct {
	Type       string                 `json:"type"`
	ProductID  string                 `json:"product_id,omitempty"`
	CustomerID string                 `json:"customer_id,omitempty"`
	SessionID  string                 `json:"session_id,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// ── Signal Extractor ─────────────────────────────────────────────────────────

// extractSignals analyzes ALL user messages in the conversation to detect intent patterns.
// Call AFTER the final reply is returned to the customer (not during tool calls).
// Scans full message history, not just the last message, to capture preferences
// expressed in earlier conversation turns.
func (a *SalesAgent) extractSignals(sess *ChatSession, finalUserMsg, reply string) []IntentSignal {
	var signals []IntentSignal

	// Collect ALL user messages from history + current message
	allUserMsgs := collectUserMessages(sess, finalUserMsg)

	// Track which signal types we've already emitted to avoid duplicates
	emitted := map[string]bool{}

	// Extract from ALL user messages in the conversation
	for _, msg := range allUserMsgs {
		// Signal 1: Price objection
		if !emitted["price_objection"] && matchPriceObjection(msg) {
			phrase := extractPricePhrase(msg)
			signals = append(signals, IntentSignal{
				Type: "price_objection",
				Metadata: map[string]interface{}{
					"trigger_phrase": phrase,
					"agent_reply":    reply,
				},
			})
			emitted["price_objection"] = true
		}

		// Signal 3: Size preference
		if !emitted["size_preference"] {
			if size := extractSizePreference(msg); size != "" {
				signals = append(signals, IntentSignal{
					Type: "size_preference",
					Metadata: map[string]interface{}{
						"size":        size,
						"agent_reply": reply,
					},
				})
				emitted["size_preference"] = true
			}
		}

		// Signal 4: Color preference
		if !emitted["color_preference"] {
			if color := extractColorPreference(msg); color != "" {
				signals = append(signals, IntentSignal{
					Type: "color_preference",
					Metadata: map[string]interface{}{
						"color":       color,
						"agent_reply": reply,
					},
				})
				emitted["color_preference"] = true
			}
		}
	}

	// Signal 2: Purchase intent (only from final message — closing signal)
	if matchPurchaseIntent(finalUserMsg) {
		pid := extractLastProductFromSession(sess)
		signals = append(signals, IntentSignal{
			Type:      "purchase_signal",
			ProductID: pid,
			Metadata: map[string]interface{}{
				"agent_reply": reply,
			},
		})

		// Signal 2b: High intent but no purchase link generated
		if !strings.Contains(reply, "utm_source=vela_ai_chat") {
			signals = append(signals, IntentSignal{
				Type:      "intent_high_no_purchase",
				ProductID: pid,
				Metadata: map[string]interface{}{
					"reason":      "purchase_signal_without_link",
					"agent_reply": reply,
				},
			})
		}
	}

	// Signal 5: Discount request — check all messages + reply
	if !emitted["discount_request"] {
		for _, msg := range allUserMsgs {
			if matchDiscountRequest(msg, reply) {
				signals = append(signals, IntentSignal{
					Type: "discount_request",
					Metadata: map[string]interface{}{
						"agent_reply": reply,
					},
				})
				break
			}
		}
	}

	return signals
}

// collectUserMessages gathers all user messages from session history + current message.
func collectUserMessages(sess *ChatSession, currentMsg string) []string {
	msgs := make([]string, 0, len(sess.MessageHistory)/2+1)
	for _, m := range sess.MessageHistory {
		if m.Role == "user" {
			msgs = append(msgs, m.Content)
		}
	}
	// Add current message (not yet in history at extraction time)
	if currentMsg != "" {
		msgs = append(msgs, currentMsg)
	}
	return msgs
}

// ── Pattern Matchers ─────────────────────────────────────────────────────────

// Common price objection phrases.
var priceObjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(too\s+(expensive|much|pricey|costly|high|steep))\b`),
	regexp.MustCompile(`(?i)\b(over\s*(my\s*)?budget)\b`),
	regexp.MustCompile(`(?i)\b(can('?t|not)\s+afford)\b`),
	regexp.MustCompile(`(?i)\b(cheaper|lower\s+price|any\s+(cheaper|discount)|more\s+affordable)\b`),
	regexp.MustCompile(`(?i)\b(is\s+there\s+(a|any)\s+(discount|coupon|code|deal|offer|promo))\b`),
	regexp.MustCompile(`(?i)\b(do\s+you\s+have\s+(a|any)\s+(discount|coupon|code|deal|offer|promo))\b`),
	regexp.MustCompile(`(?i)\b(got\s+(any|a)\s+(discount|coupon|code|deal|offer))\b`),
}
// Chinese price objection patterns — matches "太贵了", "能便宜点吗", etc.
var chinesePriceObjectionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`太贵[了啦嘛啊]?`),
	regexp.MustCompile(`有点贵`),
	regexp.MustCompile(`好贵`),
	regexp.MustCompile(`这么贵`),
	regexp.MustCompile(`能便宜[点些]吗?`),
	regexp.MustCompile(`便宜[一]?[点些]`),
	regexp.MustCompile(`[有给]优惠[吗嘛]?`),
	regexp.MustCompile(`打折[吗嘛]?`),
	regexp.MustCompile(`[有没]折扣[码吗]?`),
	regexp.MustCompile(`超出预算`),
	regexp.MustCompile(`买不起`),
}

func matchPriceObjection(msg string) bool {
	for _, p := range append(priceObjectionPatterns, chinesePriceObjectionPatterns...) {
		if p.MatchString(msg) {
			return true
		}
	}
	return false
}
func extractPricePhrase(msg string) string {
	for _, p := range append(priceObjectionPatterns, chinesePriceObjectionPatterns...) {
		if loc := p.FindStringSubmatch(msg); len(loc) > 0 {
			return loc[1] // the capture group
		}
	}
	return "price_concern"
}

// Purchase intent signals.
var purchaseIntentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(i'?ll\s+take\s+(it|one|that|this))\b`),
	regexp.MustCompile(`(?i)\b(i\s+(want|need|would\s+like)\s+to\s+buy)\b`),
	regexp.MustCompile(`(?i)\b(add\s+(it|this|that|one)\s+to\s+(my\s+)?cart)\b`),
	regexp.MustCompile(`(?i)\b(how\s+do\s+i\s+(buy|order|purchase|check\s*out))\b`),
	regexp.MustCompile(`(?i)\b(give\s+me\s+the\s+link|send\s+(me\s+)?(the\s+)?link|checkout)\b`),
	regexp.MustCompile(`(?i)\b(let'?s\s+(do\s+it|go|buy|check\s*out))\b`),
	regexp.MustCompile(`(?i)\b(i'?m\s+(ready|going)\s+to\s+(buy|order|purchase|check\s*out))\b`),
	regexp.MustCompile(`(?i)\b(sold|ring\s+me\s+up|let'?s\s+do\s+this)\b`),
}

func matchPurchaseIntent(msg string) bool {
	for _, p := range purchaseIntentPatterns {
		if p.MatchString(msg) {
			return true
		}
	}
	return false
}

// extractLastProductFromSession finds the most recent product ID from session history.
func extractLastProductFromSession(sess *ChatSession) string {
	// Scan history backwards for tool results containing product IDs
	for i := len(sess.MessageHistory) - 1; i >= 0; i-- {
		m := sess.MessageHistory[i]
		if m.Role == "tool" {
			var result struct {
				Products []struct {
					ID string `json:"id"`
				} `json:"products"`
			}
			if err := json.Unmarshal([]byte(m.Content), &result); err == nil && len(result.Products) > 0 {
				return result.Products[0].ID
			}
		}
	}
	// Fallback: check discussed products
	if len(sess.DiscussedProducts) > 0 {
		return sess.DiscussedProducts[len(sess.DiscussedProducts)-1]
	}
	return ""
}

// Size preference extraction.
var sizePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(my\s+size\s+is\s+(\w+))\b`),
	regexp.MustCompile(`(?i)\b(i\s+(wear|am|need)\s+(a\s+)?size\s+(\w+))\b`),
	regexp.MustCompile(`(?i)\b(i'?m\s+(a\s+)?(size\s+)?(\w+))\b`),
	regexp.MustCompile(`(?i)\b(size\s+(\w+))\b`),
	regexp.MustCompile(`(?i)\b(do\s+you\s+have\s+(it\s+)?in\s+(\w+))\b`),
}

func extractSizePreference(msg string) string {
	for _, p := range sizePatterns {
		matches := p.FindStringSubmatch(msg)
		if len(matches) >= 2 {
			candidate := strings.ToUpper(matches[len(matches)-1])
			// Filter out false positives
			if isValidSize(candidate) {
				return candidate
			}
		}
	}
	return ""
}

func isValidSize(s string) bool {
	s = strings.TrimSpace(strings.ToUpper(s))
	// Standard clothing sizes
	standardSizes := map[string]bool{
		"XS": true, "S": true, "SM": true, "M": true, "MD": true,
		"L": true, "XL": true, "XXL": true, "2XL": true, "XXXL": true, "3XL": true,
		"4XL": true, "5XL": true,
	}
	if standardSizes[s] {
		return true
	}
	// Numeric sizes: 0-24
	if n, err := strconv.Atoi(s); err == nil {
		return n >= 0 && n <= 24
	}
	// Shoe sizes: 5-16, sometimes with half
	if strings.Contains(s, ".") {
		parts := strings.Split(s, ".")
		if len(parts) == 2 {
			whole, e1 := strconv.Atoi(parts[0])
			_, e2 := strconv.Atoi(parts[1])
			if e1 == nil && e2 == nil && whole >= 5 && whole <= 16 {
				return true
			}
		}
	}
	return false
}

// Color preference extraction.
var commonColors = map[string]bool{
	"black": true, "white": true, "red": true, "blue": true, "green": true,
	"yellow": true, "purple": true, "pink": true, "orange": true, "brown": true,
	"grey": true, "gray": true, "navy": true, "beige": true, "gold": true,
	"silver": true, "cream": true, "tan": true, "teal": true, "olive": true,
	"burgundy": true, "maroon": true, "coral": true, "mint": true, "lavender": true,
	"charcoal": true, "ivory": true, "khaki": true, "magenta": true, "turquoise": true,
	"indigo": true, "violet": true, "plum": true, "crimson": true, "ruby": true,
	"sapphire": true, "emerald": true, "amber": true, "jade": true, "mustard": true,
}

func extractColorPreference(msg string) string {
	lower := strings.ToLower(msg)
	// Check for color-related phrases
	colorPhrases := []*regexp.Regexp{
		regexp.MustCompile(`(?i)\b(in\s+(\w+)\s*(color|colour)?)\b`),
		regexp.MustCompile(`(?i)\b((\w+)\s*(color|colour))\b`),
		regexp.MustCompile(`(?i)\b(do\s+you\s+have\s+(it\s+)?in\s+(\w+))\b`),
		regexp.MustCompile(`(?i)\b(looking\s+for\s+(a\s+)?(\w+))\b`),
	}

	for _, p := range colorPhrases {
		matches := p.FindStringSubmatch(msg)
		if len(matches) >= 2 {
			for _, m := range matches[1:] {
				candidate := strings.ToLower(strings.TrimSpace(m))
				if commonColors[candidate] {
					return candidate
				}
			}
		}
	}
	// Direct color word match in the entire message
	for color := range commonColors {
		if strings.Contains(lower, " "+color) || strings.HasPrefix(lower, color+" ") {
			return color
		}
	}
	return ""
}

// Discount request detection.
var discountRequestPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(any\s+(discount|coupon|code|promo|deal|offer|sale))\b`),
	regexp.MustCompile(`(?i)\b(got\s+(a|any)\s+(discount|coupon|code|promo|deal|offer))\b`),
	regexp.MustCompile(`(?i)\b(give\s+me\s+(a|some)\s+(discount|coupon|code|promo|deal|offer))\b`),
	regexp.MustCompile(`(?i)\b(can\s+(you|i)\s+get\s+(a|some)\s+(discount|coupon|code|promo|deal|offer))\b`),
	regexp.MustCompile(`(?i)\b(is\s+there\s+(a|any)\s+(discount|coupon|code|promo|deal|offer))\b`),
	regexp.MustCompile(`(?i)\b(do\s+you\s+have\s+(a|any)\s+(discount|coupon|code|promo|deal|offer))\b`),
}

func matchDiscountRequest(userMsg, reply string) bool {
	for _, p := range discountRequestPatterns {
		if p.MatchString(userMsg) {
			return true
		}
	}
	// Also check if the agent gave a discount (reply contains code)
	return strings.Contains(reply, "VELA-") || strings.Contains(reply, "VELA-SAVE15")
}

// ── Session Signal Storage (Redis-based, Phase 1) ────────────────────────────

// applySignalsToSession stores extracted signals in session Preferences (not MessageHistory).
// This avoids polluting the LLM context with role:"signal" messages.
func applySignalsToSession(sess *ChatSession, signals []IntentSignal) {
	if sess.Preferences == nil {
		sess.Preferences = make(map[string]string)
	}
	for _, sig := range signals {
		switch sig.Type {
		case "size_preference":
			if s, ok := sig.Metadata["size"].(string); ok {
				sess.Preferences["size"] = s
			}
		case "color_preference":
			if c, ok := sig.Metadata["color"].(string); ok {
				sess.Preferences["color"] = c
			}
		case "purchase_signal":
			sess.Preferences["intent"] = "high"
		case "price_objection":
			sess.Preferences["price_sensitive"] = "true"
		case "discount_request":
			sess.Preferences["discount_requested"] = "true"
		}
	}
}
