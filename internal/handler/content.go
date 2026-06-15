package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	chi "github.com/go-chi/chi/v5"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
	"github.com/JingxuanC/vela-engine/internal/platform/insight"
	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

type ContentHandler struct {
	db        *gorm.DB
	llmRouter *service.LLMRouter
	injector  *insight.ContextInjector
	eventBus  eventbus.EventBus
}

func NewContentHandler(db *gorm.DB, llmRouter *service.LLMRouter, inj *insight.ContextInjector) *ContentHandler {
	return &ContentHandler{db: db, llmRouter: llmRouter, injector: inj}
}

func (h *ContentHandler) SetEventBus(bus eventbus.EventBus) { h.eventBus = bus }

func (h *ContentHandler) Generate(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}
	var req struct {
		ShopID     string   `json:"shop_id"`
		JobType    string   `json:"job_type"`
		ProductIDs []string `json:"product_ids"`
		Tone       string   `json:"tone"`
		Language   string   `json:"language"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, 400, "invalid JSON body")
		return
	}
	if req.ShopID == "" || req.JobType == "" || len(req.ProductIDs) == 0 {
		httputil.WriteError(w, 400, "required fields missing")
		return
	}
	if req.JobType != "desc" && req.JobType != "blog" && req.JobType != "social" {
		httputil.WriteError(w, 400, "invalid job_type")
		return
	}
	shopID, err := uuid.Parse(req.ShopID)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id: must be UUID")
		return
	}
	tone := req.Tone
	lang := req.Language
	if tone == "" {
		tone = "professional"
	}
	if lang == "" {
		lang = "en"
	}
	pids, _ := json.Marshal(req.ProductIDs)
	job := model.ContentJob{ShopID: shopID, JobType: req.JobType, SourceProductIDs: pids, Tone: tone, Language: lang, Status: "processing"}
	if err := h.db.Create(&job).Error; err != nil {
		httputil.WriteError(w, 500, "failed to create job")
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("content: panic", "error", r)
			}
		}()
		// 5-minute timeout per job (covers all product IDs in the batch)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cfg := modeConfig(req.JobType)

		// Set RAG source filter per mode
		if h.injector != nil {
			if fetcher := h.injector.GetFetcher("rag"); fetcher != nil {
				if ss, ok := fetcher.(insight.SourceSetter); ok {
					ss.SetSources(cfg.ragSources)
				}
			}
		}

		var results []map[string]string
		allOK := true
		for _, pid := range req.ProductIDs {
			r := map[string]string{"product_id": pid}

			sys := h.buildContentPrompt(ctx, shopID, pid, req.JobType, tone, lang)

			contProvider, contCfg, contErr := h.llmRouter.GetProvider(ctx, shopID)
			if contErr != nil {
				r["error"] = contErr.Error()
				allOK = false
				results = append(results, r)
				continue
			}
			chatReq := &service.ChatCompletionRequest{
				Model:       contCfg.Model,
				Messages:    []service.ChatMessage{{Role: "system", Content: sys}},
				Temperature: cfg.temperature,
				MaxTokens:   cfg.maxTokens,
			}
			resp, err := contProvider.ChatCompletion(ctx, chatReq)
			if err == nil {
				r["content"] = resp

				// 🆕 Phase 1: Create ContentPiece with UTM tracking
				pieceID := uuid.New()
				shortID := pieceID.String()[:8]

				// Look up shop domain
				var shop model.Shop
				shopDomain := ""
				if h.db.Where("id = ?", shopID).First(&shop).Error == nil {
					shopDomain = shop.Domain
				}

				// Look up product handle from SyncedProduct
				var product model.SyncedProduct
				handle := ""
				if h.db.Where("platform_id = ? AND shop_id = ?", pid, shopID).First(&product).Error == nil {
					handle = product.Handle
					if handle == "" {
						handle = slugify(product.Title)
					}
				}
				if handle == "" {
					handle = "product-" + pid
				}

				utmURL := buildUTMURL(shopDomain, handle, shortID, req.JobType)

				piece := model.ContentPiece{
					ID:          pieceID,
					ShortID:     shortID,
					ShopID:      shopID,
					JobID:       job.ID,
					ContentType: req.JobType,
					ProductID:   pid,
					Body:        resp,
					Tone:        tone,
					Language:    lang,
					UTMURL:      utmURL,
					Status:      "generated",
				}
				if err := h.db.Create(&piece).Error; err != nil {
					slog.Warn("content: failed to create content piece", "error", err)
				} else {
					r["content_piece_id"] = piece.ID.String()
					r["short_id"] = shortID
					r["utm_url"] = utmURL
				}
			} else {
				r["error"] = err.Error()
				allOK = false
			}
			results = append(results, r)
			time.Sleep(200 * time.Millisecond)
		}
		PublishAIEvent(ctx, h.eventBus, shopID, "content.generate", req, results, allOK)
		// 🆕 Phase 1: Publish content.generated event with piece IDs
		if h.eventBus != nil {
			pieceIDs := make([]string, 0)
			for _, r := range results {
				if pid, ok := r["content_piece_id"]; ok {
					pieceIDs = append(pieceIDs, pid)
				}
			}
			if ev, err := eventbus.NewEvent(eventbus.EventContentGenerated, shopID, map[string]interface{}{
				"job_id":    job.ID.String(),
				"job_type":  req.JobType,
				"piece_ids": pieceIDs,
			}, "content-handler"); err == nil {
				go func() { _ = h.eventBus.Publish(ctx, ev) }()
			}
		}
		res, _ := json.Marshal(results)
		status := "completed"
		var emsg string
		if len(results) == 0 {
			status = "failed"
			emsg = "no results"
		}
		h.db.Model(&job).Updates(map[string]interface{}{"status": status, "result": res, "error_message": emsg})
	}()
	httputil.WriteOK(w, map[string]interface{}{"success": true, "job_id": job.ID, "status": "processing"})
}

// contentMode configures generation parameters per content type.
type contentMode struct {
	temperature float64
	maxTokens   int
	ragSources  []string // RAG source filter; nil = all
}

func modeConfig(jobType string) contentMode {
	switch jobType {
	case "desc":
		// Product description: low temp for factual accuracy, focused on reviews + brand voice
		return contentMode{0.3, 500, []string{"product", "review", "ai_reply"}}
	case "blog":
		// Blog post: higher temp for creativity, needs trend data for SEO angle
		return contentMode{0.7, 1000, []string{"product", "review", "insight"}}
	case "social":
		// Social caption: high temp for punchy copy, lightweight context
		return contentMode{0.8, 300, []string{"product", "review"}}
	default:
		return contentMode{0.5, 500, nil}
	}
}

// buildContentPrompt constructs a structured LLM prompt for content generation.
// The prompt structure varies by jobType — desc emphasizes specs, blog emphasizes
// storytelling, social emphasizes brevity.
func (h *ContentHandler) buildContentPrompt(ctx context.Context, shopID uuid.UUID, productID, jobType, tone, lang string) string {
	var sb strings.Builder

	// ── 1. Product data from PG (shared across all modes) ──
	productName := productID
	if h.db != nil {
		var p model.SyncedProduct
		if err := h.db.WithContext(ctx).
			Where("platform_id = ? OR id = ?", productID, productID).
			First(&p).Error; err == nil {
			productName = p.Title
			sb.WriteString("[Product Data]\n")
			sb.WriteString(fmt.Sprintf("Name: %s\n", p.Title))
			sb.WriteString(fmt.Sprintf("Type: %s | Brand: %s\n", p.ProductType, p.Vendor))
			if p.Tags != "" {
				sb.WriteString(fmt.Sprintf("Tags: %s\n", p.Tags))
			}
			if p.Description != "" {
				sb.WriteString(fmt.Sprintf("Current description: %s\n", p.Description))
			}
			sb.WriteString("\n")
		}
	}

	// ── 2. Platform context injection (source-filtered by mode, set before this call) ──
	if h.injector != nil && h.db != nil {
		query := fmt.Sprintf("%s %s %s", jobType, productName, tone)
		injected := h.injector.InjectWithQuery(ctx, h.db, shopID.String(), query)
		if injected != "" {
			sb.WriteString(injected)
			sb.WriteString("\n")
		}
	}

	// ── 3. Mode-specific task instruction ──
	switch jobType {
	case "desc":
		sb.WriteString(fmt.Sprintf(`[Task]
Write a product description for %s in %s with %s tone. 100-150 words.
Guidelines:
- Lead with the strongest selling point from customer reviews
- Mention fabric/material and fit details
- If sizing issues appear in reviews, add a fit tip
- Incorporate trending category keywords naturally
- Match the brand voice from existing product content
Output plain text, no JSON wrapper.`, productName, lang, tone))

	case "blog":
		sb.WriteString(fmt.Sprintf(`[Task]
Write a blog post featuring %s. %s, %s tone. 300-500 words.
Structure:
1. Headline — catchy, SEO-friendly, include category keyword
2. Intro — hook the reader with a use case or trend insight
3. Body (2-3 paragraphs) — product features, customer love, styling tips
4. Closing — call to action, link suggestion
Guidelines:
- Weave in trending keywords from market data naturally
- Reference real customer praise from reviews
- Write like a lifestyle editor, not a product manual
- Include subheadings for readability
Output plain text with the full blog post.`, productName, lang, tone))

	case "social":
		sb.WriteString(fmt.Sprintf(`[Task]
Write a social media caption for %s. %s, %s tone. Max 50 words.
Guidelines:
- Hook in first 5 words — stop the scroll
- Use 3-5 relevant hashtags at the end
- Include a call to action (shop now, link in bio, etc.)
- Keep sentences short and punchy
- Reference the product's strongest review highlight
Output format:
[Caption text]

#hashtag1 #hashtag2 #hashtag3`, productName, lang, tone))
	}

	return sb.String()
}

func (h *ContentHandler) JobStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		httputil.WriteError(w, 503, "DB unavailable")
		return
	}
	sid := r.URL.Query().Get("shop_id")
	shopID, err := uuid.Parse(sid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid shop_id")
		return
	}
	jid := chi.URLParam(r, "jobID")
	id, err := uuid.Parse(jid)
	if err != nil {
		httputil.WriteError(w, 400, "invalid job_id")
		return
	}
	var job model.ContentJob
	if err := h.db.Where("shop_id=? AND id=?", shopID, id).First(&job).Error; err != nil {
		httputil.WriteError(w, 404, "job not found")
		return
	}
	var results []map[string]string
	if job.Status == "completed" && job.Result != nil {
		json.Unmarshal(job.Result, &results)
	}
	// 🆕 Phase 1: Include content pieces for UTM tracking
	var pieces []model.ContentPiece
	h.db.Where("job_id = ?", id).Find(&pieces)
	pieceInfos := make([]map[string]string, len(pieces))
	for i, p := range pieces {
		pieceInfos[i] = map[string]string{
			"id":       p.ID.String(),
			"short_id": p.ShortID,
			"utm_url":  p.UTMURL,
		}
	}
	httputil.WriteOK(w, map[string]interface{}{
		"success":        true,
		"job_id":         jid,
		"job_type":       job.JobType,
		"status":         job.Status,
		"tone":           job.Tone,
		"result":         results,
		"content_pieces": pieceInfos,
		"error":          job.ErrorMessage,
	})
}

// ── UTM helpers ──────────────────────────────────────────────────────────

// slugify converts a product title to a Shopify-compatible URL handle.
func slugify(title string) string {
	slug := strings.ToLower(title)
	slug = strings.TrimSpace(slug)
	// Replace any non-alphanumeric char (including spaces) with a single dash
	var b strings.Builder
	prevDash := false
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// buildUTMURL constructs a UTM-tracked product URL for a content piece.
func buildUTMURL(shopDomain, handle, shortID, contentType string) string {
	if shopDomain == "" {
		return ""
	}
	// Map content type to UTM medium
	medium := contentType
	switch contentType {
	case "desc":
		medium = "product_desc"
	case "blog":
		medium = "blog"
	case "social":
		medium = "social"
	}

	base := fmt.Sprintf("https://%s/products/%s", shopDomain, handle)
	params := url.Values{}
	params.Set("utm_source", "vela_ai")
	params.Set("utm_medium", medium)
	params.Set("utm_campaign", "content_factory")
	params.Set("utm_content", shortID)
	return base + "?" + params.Encode()
}
