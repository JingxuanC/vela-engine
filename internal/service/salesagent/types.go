package salesagent

import (
	"time"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// ── Tool Call ─────────────────────────────────────────────────────────────────

// ToolCall represents a parsed tool invocation from LLM output.
type ToolCall struct {
	Name string
	Args map[string]interface{}
}

// ToolSchema describes a tool for injection into the LLM prompt.
type ToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ── Product Result (search_products output) ──────────────────────────────────

// ProductResult is the formatted result returned to the LLM.
type ProductResult struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Price    string   `json:"price"`
	ImageURL string   `json:"image_url"`
	Variants []string `json:"variants"`
	InStock  bool     `json:"in_stock"`
}

// ── Chat Session (Redis) ─────────────────────────────────────────────────────

// ChatSession stores per-customer conversation context.
// Serialized as JSON in Redis: chat_session:{shopID}:{customerID}, TTL 24h.
type ChatSession struct {
	ShopID            string               `json:"shop_id"`
	CustomerID        string               `json:"customer_id"`
	Preferences       map[string]string    `json:"preferences"`
	DiscussedProducts []string             `json:"discussed_products"`
	Stage             string               `json:"stage"`
	MessageHistory    []service.ChatMessage `json:"message_history"`
	CreatedAt         time.Time            `json:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at"`
}

const maxHistoryMessages = 20

// ── Few-Shot Example ─────────────────────────────────────────────────────────

// FewShotExample is injected into the system prompt to teach the model.
type FewShotExample struct {
	Scenario      string `json:"scenario"`
	UserMessage   string `json:"user_message"`
	ToolCallXML   string `json:"tool_call_xml,omitempty"`
	ToolResult    string `json:"tool_result,omitempty"`
	ExpectedReply string `json:"expected_reply"`
}

// ── Discount Config ──────────────────────────────────────────────────────────

// DiscountConfig is read from Redis when the merchant has configured AI chat discount rules.
type DiscountConfig struct {
	ValueType string  `json:"value_type"` // percentage | fixed_amount
	Value     float64 `json:"value"`
	Enabled   bool    `json:"enabled"`
}
