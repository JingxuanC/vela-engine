// Package review provides AI-powered review analysis, reply generation, and discount logic.
package review

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/service"
)

// --- Analysis Types ---

// BadReviewAnalysis holds the result of analyzing a bad review.
type BadReviewAnalysis struct {
	ComplaintType     string   `json:"complaint_type"` // size / quality / service / shipping / other
	Severity          string   `json:"severity"`       // mild / moderate / severe
	SeverityScore     float64  `json:"severity_score"` // 0-1 scale
	IsActionable      bool     `json:"is_actionable"`
	SpecificIssues    []string `json:"specific_issues"`
	RecommendedAction string   `json:"recommended_action"` // apology_only / discount / refund / exchange
}

// ReplyGeneration holds generated reply drafts.
type ReplyGeneration struct {
	Replies []string `json:"replies"`
}

// DiscountSuggestion holds a generated discount offer.
type DiscountSuggestion struct {
	Percent     float64 `json:"percent"`
	Code        string  `json:"code"`
	ExpiresDays int     `json:"expires_days"`
	Reason      string  `json:"reason"`
}

// AnalyzeBadReview analyzes a bad review and returns structured insights.
// If the LLM is unavailable, falls back to rule-based analysis.
func AnalyzeBadReview(review ReviewInput) BadReviewAnalysis {
	slog.Info("review_auto_reply: analyzing bad review", "review_id", review.ID, "rating", review.Rating)

	// Rule-based severity from rating
	severity := "mild"
	severityScore := 0.3
	if review.Rating <= 1 {
		severity = "severe"
		severityScore = 0.85
	} else if review.Rating <= 2 {
		severity = "moderate"
		severityScore = 0.55
	}

	// Rule-based complaint type detection
	body := strings.ToLower(review.Body)
	title := strings.ToLower(review.Title)
	fullText := body + " " + title

	complaintType := "other"
	specificIssues := []string{}

	switch {
	case containsAny(fullText, "size", "too small", "too big", "too large", "doesn't fit", "tight", "loose", "sizing"):
		complaintType = "size"
		specificIssues = extractIssues(fullText, []string{"size", "fit"})
	case containsAny(fullText, "quality", "cheap", "poor", "fabric", "material", "fell apart", "broke", "defective"):
		complaintType = "quality"
		specificIssues = extractIssues(fullText, []string{"quality", "material", "defect"})
	case containsAny(fullText, "shipping", "delivery", "late", "never arrived", "lost", "damaged"):
		complaintType = "shipping"
		specificIssues = extractIssues(fullText, []string{"shipping", "delivery"})
	case containsAny(fullText, "customer service", "support", "help", "refund", "return"):
		complaintType = "service"
		specificIssues = extractIssues(fullText, []string{"service", "support"})
	default:
		specificIssues = extractIssues(fullText, []string{"disappointed", "bad", "worst", "terrible", "awful"})
	}

	// Determine if actionable
	isActionable := true
	nonActionableKeywords := []string{"just want a refund", "already returned", "never buying", "won't buy"}
	for _, kw := range nonActionableKeywords {
		if strings.Contains(fullText, kw) {
			isActionable = false
			break
		}
	}

	// Recommended action
	recommendedAction := "apology_only"
	if severity == "severe" {
		recommendedAction = "refund"
	} else if severity == "moderate" && isActionable {
		recommendedAction = "discount"
	} else if isActionable {
		recommendedAction = "discount"
	}

	return BadReviewAnalysis{
		ComplaintType:     complaintType,
		Severity:          severity,
		SeverityScore:     severityScore,
		IsActionable:      isActionable,
		SpecificIssues:    specificIssues,
		RecommendedAction: recommendedAction,
	}
}

// AnalyzeBadReviewWithLLM uses the LLM for deeper analysis.
func AnalyzeBadReviewWithLLM(ctx context.Context, client service.LLMProvider, review ReviewInput, productName string) BadReviewAnalysis {
	slog.Info("review_auto_reply: LLM analyzing bad review", "review_id", review.ID)

	prompt := fmt.Sprintf(`你是一个电商客户体验分析专家。分析以下差评，输出 JSON。

分析维度：
1. complaint_type：投诉类型（size/quality/service/shipping/other）
2. severity：严重程度（mild/moderate/severe）
3. severity_score：0-1 的严重程度评分
4. is_actionable：商家是否可以补救（true/false）
5. specific_issues：具体的投诉点列表
6. recommended_action：推荐的处理方式（apology_only / discount / refund / exchange）

输入：
{
  "product_name": "%s",
  "rating": %.1f,
  "title": "%s",
  "body": "%s"
}

返回 JSON 格式。`, productName, review.Rating, escJSON(review.Title), escJSON(review.Body))

	messages := []service.ChatMessage{
		{Role: "system", Content: "你是一个电商客户体验分析专家。只返回 JSON，不要额外文字。"},
		{Role: "user", Content: prompt},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   512,
	}

	var analysis BadReviewAnalysis
	err := client.ChatCompletionJSON(ctx, req, &analysis)
	if err != nil {
		slog.Warn("review_auto_reply: LLM analysis failed, falling back to rule-based", "error", err)
		return AnalyzeBadReview(review)
	}

	// Ensure defaults
	if analysis.Severity == "" {
		analysis.Severity = "mild"
	}
	if analysis.ComplaintType == "" {
		analysis.ComplaintType = "other"
	}
	if analysis.RecommendedAction == "" {
		analysis.RecommendedAction = "apology_only"
	}

	return analysis
}

// GenerateReplies generates 3 reply drafts for a bad review.
// Uses rule-based templates as fallback when LLM is unavailable.
func GenerateReplies(analysis BadReviewAnalysis, productName string) []string {
	_ = analysis // available for more nuanced generation in LLM mode

	complaintAck := map[string]string{
		"size":     "我们非常抱歉这款产品的尺码未能符合您的预期。您的反馈对我们的尺码优化非常有帮助。",
		"quality":  "我们非常抱歉产品的质量未能达到您的期望。我们始终致力于提供优质产品，您的反馈将直接传达给质量团队。",
		"shipping": "我们非常抱歉配送体验未能让您满意。物流环节我们一直在持续优化。",
		"service":  "我们非常抱歉服务体验未能达到您的期望。您的反馈对我们改进服务至关重要。",
		"other":    "我们非常抱歉您的体验未能达到预期。感谢您抽出时间分享宝贵的反馈。",
	}

	ack := complaintAck[analysis.ComplaintType]
	if ack == "" {
		ack = complaintAck["other"]
	}

	return []string{
		fmt.Sprintf("尊敬的顾客，您好！%s 关于%s的反馈我们已经认真阅读，我们会持续改进。如有任何问题，欢迎随时联系我们的客服团队。感谢您的理解与支持！", ack, productName),
		fmt.Sprintf("亲爱的顾客，感谢您的坦诚反馈。%s 我们非常重视每位顾客的声音，关于%s的问题我们已经记录并会尽快排查。期待有机会为您提供更好的购物体验。", ack, productName),
		fmt.Sprintf("您好！非常感谢您花时间分享使用%s的体验。%s 我们希望能为您提供更满意的解决方案，我们的客服团队会主动与您联系。祝您生活愉快！", productName, ack),
	}
}

// GenerateRepliesWithLLM uses DashScope Qwen to generate personalized reply drafts.
func GenerateRepliesWithLLM(ctx context.Context, client service.LLMProvider, analysis BadReviewAnalysis, productName string) []string {
	slog.Info("review_auto_reply: LLM generating replies")

	prompt := fmt.Sprintf(`你是一个礼貌专业的品牌客服代表。基于以下差评信息，生成 3 条不同的回复（正式/温暖/简洁），输出 JSON 数组。

每条回复必须：
- 真诚道歉，不推卸责任
- 具体回应投诉点（显示真的读了评论）
- 不包含具体折扣信息
- 署名品牌名称
- 控制在 120 字以内

输入：
{
  "customer_name": "",
  "rating": %.1f,
  "complaint_type": "%s",
  "specific_issues": %s,
  "product_name": "%s",
  "discount_offered": %t,
  "brand_tone": "friendly"
}

输出 JSON 数组，如 ["回复1", "回复2", "回复3"]`,
		calculateRating(analysis.Severity),
		analysis.ComplaintType,
		toJSON(analysis.SpecificIssues),
		productName,
		analysis.IsActionable,
	)

	messages := []service.ChatMessage{
		{Role: "system", Content: "你是一个礼貌专业的品牌客服代表。只返回 JSON 数组，不要额外文字。"},
		{Role: "user", Content: prompt},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.7,
		MaxTokens:   1024,
	}

	var replies []string
	err := client.ChatCompletionJSON(ctx, req, &replies)
	if err != nil || len(replies) == 0 {
		slog.Warn("review_auto_reply: LLM reply generation failed, using fallback", "error", err)
		return GenerateReplies(analysis, productName)
	}

	// Ensure we return at most 3
	if len(replies) > 3 {
		replies = replies[:3]
	}

	return replies
}

// GenerateDiscount creates a discount suggestion based on severity.
func GenerateDiscount(severity string) DiscountSuggestion {
	var dc DiscountSuggestion
	dc.Code = generateCouponCode()

	switch severity {
	case "severe":
		dc.Percent = 50
		dc.ExpiresDays = 90
		dc.Reason = "Full satisfaction guarantee — we want to make it right"
	case "moderate":
		dc.Percent = 30
		dc.ExpiresDays = 60
		dc.Reason = "We appreciate your patience and want to give you a better experience"
	default: // mild
		dc.Percent = 15
		dc.ExpiresDays = 30
		dc.Reason = "Thank you for your feedback — here's a small token of appreciation"
	}

	return dc
}

// GenerateDiscountWithLLM uses LLM to determine optimal discount.
func GenerateDiscountWithLLM(ctx context.Context, client service.LLMProvider, severity string, analysis BadReviewAnalysis) DiscountSuggestion {
	prompt := fmt.Sprintf(`基于差评分析结果，生成合适的补偿折扣：

规则：
- severe + product_issue → 全额退款 + 50%% OFF 下次（有效期 90 天）
- moderate + actionable → 30%% OFF + 免运费（有效期 60 天）
- mild + minor_issue → 15%% OFF（有效期 30 天）
- size_issue（不影响商品质量）→ 20%% OFF 下次购买

分析结果：{"severity":"%s","complaint_type":"%s","is_actionable":%t,"specific_issues":%s}

返回 JSON：{"percent": 数字, "expires_days": 数字, "reason": "原因"}`, severity, analysis.ComplaintType, analysis.IsActionable, toJSON(analysis.SpecificIssues))

	messages := []service.ChatMessage{
		{Role: "system", Content: "你是一个电商折扣策略专家。只返回 JSON，不要额外文字。"},
		{Role: "user", Content: prompt},
	}

	req := &service.ChatCompletionRequest{
		Messages:    messages,
		Temperature: 0.3,
		MaxTokens:   256,
	}

	var dc DiscountSuggestion
	err := client.ChatCompletionJSON(ctx, req, &dc)
	if err != nil || dc.Percent <= 0 {
		slog.Warn("review_auto_reply: LLM discount generation failed, using rule-based", "error", err)
		return GenerateDiscount(severity)
	}

	dc.Code = generateCouponCode()
	return dc
}

// --- helpers ---

func containsAny(s string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func extractIssues(text string, keywords []string) []string {
	var issues []string
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			issues = append(issues, kw)
		}
	}
	if len(issues) == 0 {
		return []string{"general dissatisfaction"}
	}
	return issues
}

func generateCouponCode() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	code := make([]byte, 10)
	for i := range code {
		code[i] = charset[rand.Intn(len(charset))]
	}
	return "VT" + string(code)
}

func calculateRating(severity string) float64 {
	switch severity {
	case "severe":
		return 1.0
	case "moderate":
		return 2.0
	default:
		return 3.0
	}
}

func escJSON(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

func toJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}
