package rag

import (
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── Chunk Factory ────────────────────────────────────────────────────────────

// ChunkInput is the raw input for creating a chunk (before PII stripping).
type ChunkInput struct {
	ShopID   string
	Source   string
	SourceID string
	Content  string
	Meta     ChunkMeta
}

// ChunkFactory converts raw data into PII-stripped, whitelist-filtered Chunks.
type ChunkFactory struct {
	patterns []*regexp.Regexp
}

// NewChunkFactory creates a ChunkFactory with pre-compiled PII patterns.
func NewChunkFactory() *ChunkFactory {
	return &ChunkFactory{
		patterns: []*regexp.Regexp{
			regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`), // email
			regexp.MustCompile(`\b1[3-9]\d{9}\b`),                                      // Chinese mobile
			regexp.MustCompile(`\b\d{3}[\-.]?\d{3}[\-.]?\d{4}\b`),                      // US phone
			regexp.MustCompile(`\d+\s*[一-鿿]*(路|街|道|巷|弄|号|楼|室|层)\s*\d*`), // address
		},
	}
}

// MakeChunks processes raw inputs through whitelist filter → PII stripping → Chunk output.
// Returns only valid chunks; invalid sources or empty content are silently dropped.
func (f *ChunkFactory) MakeChunks(inputs []ChunkInput) []Chunk {
	chunks := make([]Chunk, 0, len(inputs))
	now := time.Now().UTC()

	for _, in := range inputs {
		// Whitelist check
		if !IsValidSource(in.Source) {
			continue
		}

		// PII stripping
		clean := f.stripPII(in.Content)
		clean = strings.TrimSpace(clean)
		if clean == "" {
			continue
		}

		// Auto-summary: use first 200 chars or metadata summary
		summary := in.Meta.Summary
		if summary == "" {
			summary = truncate(clean, 200)
		}

		chunks = append(chunks, Chunk{
			ID:         uuid.New().String(),
			ShopID:     in.ShopID,
			Source:     in.Source,
			SourceID:   in.SourceID,
			Content:    clean,
			Metadata: ChunkMeta{
				ProductID:   in.Meta.ProductID,
				ProductName: in.Meta.ProductName,
				ReturnID:    in.Meta.ReturnID,
				Severity:    in.Meta.Severity,
				Summary:     summary,
			},
			ValidUntil: TTLForSource(in.Source),
			CreatedAt:  now,
		})
	}

	return chunks
}

// stripPII removes personally identifiable information from text.
// Email → removed, phone → removed, address → city only, name → "Customer".
func (f *ChunkFactory) stripPII(text string) string {
	for _, p := range f.patterns {
		text = p.ReplaceAllString(text, "")
	}

	// Replace given names with "Customer" using broad patterns.
	// English: "Alice returned..." → "Customer returned..."
	text = regexp.MustCompile(`\b[A-Z][a-z]{2,}\s+(?:returned|said|wrote|complained|rated|ordered|bought|left|gave|reported)`).
		ReplaceAllString(text, "Customer ${1}")
	// Chinese prefix + name: "顾客 张三 退货..." → "顾客 Customer 退货"
	text = regexp.MustCompile(`顾客\s+\S{1,4}\s*(退货|说|反馈|表示|评价|购买)`).
		ReplaceAllString(text, "顾客 Customer ${1}")
	// "by Alice" → "by Customer"
	text = regexp.MustCompile(`by\s+[A-Z][a-z]{2,}`).
		ReplaceAllString(text, "by Customer")

	// Clean up double spaces from removals
	text = regexp.MustCompile(`\s{2,}`).ReplaceAllString(text, " ")
	// Clean up leading/trailing commas from removals
	text = regexp.MustCompile(`,\s*,`).ReplaceAllString(text, ",")

	return text
}

// truncate cuts text to maxLen, trying to break at a word boundary.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	cut := s[:maxLen]
	if lastSpace := strings.LastIndex(cut, " "); lastSpace > maxLen/2 {
		cut = cut[:lastSpace]
	}
	return cut + "..."
}
