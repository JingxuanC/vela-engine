package externaldata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ── Google Trends URLs ────────────────────────────────────────────────────────

const (
	trendsExploreURL    = "https://trends.google.com/trends/api/explore"
	trendsWidgetDataURL = "https://trends.google.com/trends/api/widgetdata/multiline"
	trendsUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64)"
	trendsHTTPTimeout   = 10 * time.Second
	trendsRequestPause  = 1 * time.Second
	trendsCacheTTL      = 6 * time.Hour
)

// ── Data Types ────────────────────────────────────────────────────────────────

// TrendsData holds Google Trends interest-over-time results.
type TrendsData struct {
	Keyword   string      `json:"keyword"`
	Region    string      `json:"region"`
	Timeline  []DataPoint `json:"timeline"`
	Trend     string      `json:"trend"` // rising, falling, stable
	PeakScore int         `json:"peak_score"`
}

// DataPoint is a single time-series value.
type DataPoint struct {
	Date  time.Time `json:"date"`
	Value int       `json:"value"`
}

// TrendQuery is a related search query returned by Google Trends.
type TrendQuery struct {
	Query  string `json:"query"`
	Score  int    `json:"score"`
	Status string `json:"status"` // rising, breakout, top
}

// ── Cache Interface (avoid circular dep on service package) ──────────────────

// TrendsCache defines the minimal cache interface needed by TrendsClient.
// service.CacheService satisfies this interface.
type TrendsCache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key, value string, ttl time.Duration) error
}

// ── Google Trends JSON Structures ─────────────────────────────────────────────

// trendsExploreRequest is the payload sent to trends/explore.
type trendsExploreRequest struct {
	ComparisonItem []trendsComparisonItem `json:"comparisonItem"`
	Category       int                    `json:"category"`
	Property       string                 `json:"property"`
}

type trendsComparisonItem struct {
	Keyword string `json:"keyword"`
	Geo     string `json:"geo"`
	Time    string `json:"time"`
}

// trendsExploreResponse is the )]}'-stripped JSON from explore.
type trendsExploreResponse struct {
	Widgets []trendsWidget `json:"widgets"`
}

type trendsWidget struct {
	ID      string          `json:"id"`
	Token   string          `json:"token"`
	Request json.RawMessage `json:"request"`
}

// trendsWidgetDataResponse is the )]}'-stripped JSON from widgetdata.
type trendsWidgetDataResponse struct {
	Default trendsWidgetDefault `json:"default"`
}

type trendsWidgetDefault struct {
	TimelineData    []trendsTimelinePoint `json:"timelineData"`
	RankedList      []trendsRankedList    `json:"rankedList"`
}

type trendsTimelinePoint struct {
	FormattedTime  string  `json:"formattedTime"`
	FormattedAxisTime string `json:"formattedAxisTime"`
	Time           string  `json:"time"`
	Value          []int   `json:"value"`
	HasData        []bool  `json:"hasData"`
	FormattedValue []string `json:"formattedValue"`
}

type trendsRankedItem struct {
	Query            string `json:"query"`
	Value            int    `json:"value"`
	HasData          bool   `json:"hasData"`
	Link             string `json:"link"`
	FormattedValue   string `json:"formattedValue"`
}

type trendsRankedList struct {
	Title         string             `json:"title"`
	RankedKeyword []trendsRankedItem `json:"rankedKeyword"`
}

// ── GoogleTrendsClient ────────────────────────────────────────────────────────

// GoogleTrendsClient scrapes Google Trends data via HTTP and caches results in Redis.
type GoogleTrendsClient struct {
	httpClient *http.Client
	cache      TrendsCache
	lastReq    time.Time
}

// NewGoogleTrendsClient creates a new GoogleTrendsClient with real HTTP scraping.
// Call SetCache() to enable Redis caching; without it, every call hits Google directly.
func NewGoogleTrendsClient() *GoogleTrendsClient {
	slog.Info("externaldata: GoogleTrendsClient initialized (real HTTP scraping mode)")
	return &GoogleTrendsClient{
		httpClient: &http.Client{Timeout: trendsHTTPTimeout},
	}
}

// SetCache injects a cache backend (e.g., service.CacheService / Redis).
// When nil, caching is skipped silently.
func (c *GoogleTrendsClient) SetCache(cache TrendsCache) {
	c.cache = cache
}

// ── Public Methods ────────────────────────────────────────────────────────────

// GetInterestOverTime returns trend time-series data for the given keywords.
// Uses cache if available, falls back to mock data on scraping failure.
func (c *GoogleTrendsClient) GetInterestOverTime(ctx context.Context, keywords []string, region string) (*TrendsData, error) {
	kw := "product"
	if len(keywords) > 0 {
		kw = keywords[0]
	}
	timeframe := "today 12-m"

	// 1. Try cache
	if data := c.getCachedTrends(ctx, "timeseries", kw, region, timeframe); data != nil {
		return data, nil
	}

	// 2. Scrape Google Trends
	data, err := c.scrapeInterestOverTime(ctx, kw, region, timeframe)
	if err != nil {
		slog.Warn("externaldata: Google Trends scraping failed, falling back to mock",
			"keyword", kw, "region", region, "error", err)
		return c.mockInterestOverTime(keywords, region), nil
	}

	// 3. Cache result
	c.setCachedTrends(ctx, "timeseries", kw, region, timeframe, data)

	return data, nil
}

// GetRelatedQueries returns related search queries for the given keyword.
// Uses cache if available, falls back to mock data on scraping failure.
func (c *GoogleTrendsClient) GetRelatedQueries(ctx context.Context, keyword string) ([]TrendQuery, error) {
	geo := "US"
	timeframe := "today 12-m"

	// 1. Try cache
	if cached := c.getCachedQueries(ctx, "related", keyword, geo, timeframe); cached != nil {
		return cached, nil
	}

	// 2. Scrape Google Trends
	queries, err := c.scrapeRelatedQueries(ctx, keyword, geo, timeframe)
	if err != nil {
		slog.Warn("externaldata: Google Trends scraping failed, falling back to mock",
			"keyword", keyword, "error", err)
		return c.mockRelatedQueries(keyword), nil
	}

	// 3. Cache result
	c.setCachedQueries(ctx, "related", keyword, geo, timeframe, queries)

	return queries, nil
}

// ── Scraping Logic ────────────────────────────────────────────────────────────

func (c *GoogleTrendsClient) scrapeInterestOverTime(ctx context.Context, keyword, geo, timeframe string) (*TrendsData, error) {
	// Request rate limiting
	c.rateLimit()

	// Step 1: Get widget token
	token, widgetReq, err := c.fetchExploreToken(ctx, keyword, geo, timeframe, "TIMESERIES")
	if err != nil {
		return nil, fmt.Errorf("explore: %w", err)
	}

	c.rateLimit()

	// Step 2: Fetch widget data
	rawResp, err := c.fetchWidgetData(ctx, token, widgetReq)
	if err != nil {
		return nil, fmt.Errorf("widgetdata: %w", err)
	}

	// Step 3: Parse timeline
	return c.parseTimelineResponse(keyword, geo, rawResp)
}

func (c *GoogleTrendsClient) scrapeRelatedQueries(ctx context.Context, keyword, geo, timeframe string) ([]TrendQuery, error) {
	c.rateLimit()

	// Step 1: Get widget token for RELATED_QUERIES
	token, widgetReq, err := c.fetchExploreToken(ctx, keyword, geo, timeframe, "RELATED_QUERIES")
	if err != nil {
		return nil, fmt.Errorf("explore: %w", err)
	}

	c.rateLimit()

	// Step 2: Fetch widget data
	rawResp, err := c.fetchWidgetData(ctx, token, widgetReq)
	if err != nil {
		return nil, fmt.Errorf("widgetdata: %w", err)
	}

	// Step 3: Parse related queries
	return c.parseRelatedQueriesResponse(rawResp)
}

// fetchExploreToken POSTs to trends/explore and extracts the token for the requested widget type.
func (c *GoogleTrendsClient) fetchExploreToken(ctx context.Context, keyword, geo, timeframe, widgetType string) (string, json.RawMessage, error) {
	reqBody := trendsExploreRequest{
		ComparisonItem: []trendsComparisonItem{
			{Keyword: keyword, Geo: geo, Time: timeframe},
		},
		Category: 0,
		Property: "",
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return "", nil, err
	}

	// Build URL with URL-encoded req parameter
	reqURL := fmt.Sprintf("%s?hl=en-US&tz=-480&req=%s",
		trendsExploreURL, url.QueryEscape(string(jsonBody)))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, nil)
	if err != nil {
		return "", nil, err
	}
	httpReq.Header.Set("User-Agent", trendsUserAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, err
	}

	// Strip )]}'\n prefix
	body = stripTrendsPrefix(body)

	var exploreResp trendsExploreResponse
	if err := json.Unmarshal(body, &exploreResp); err != nil {
		return "", nil, fmt.Errorf("parse explore response: %w", err)
	}

	// Find the widget matching the requested type
	for _, w := range exploreResp.Widgets {
		if strings.EqualFold(w.ID, widgetType) {
			return w.Token, w.Request, nil
		}
	}

	return "", nil, fmt.Errorf("widget type %q not found in explore response (got %d widgets)",
		widgetType, len(exploreResp.Widgets))
}

// fetchWidgetData GETs widget data from trends/widgetdata/multiline.
func (c *GoogleTrendsClient) fetchWidgetData(ctx context.Context, token string, widgetReq json.RawMessage) ([]byte, error) {
	reqURL := fmt.Sprintf("%s?hl=en-US&tz=-480&req=%s&token=%s",
		trendsWidgetDataURL, url.QueryEscape(string(widgetReq)), url.QueryEscape(token))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", trendsUserAgent)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return stripTrendsPrefix(body), nil
}

// ── Response Parsing ──────────────────────────────────────────────────────────

func (c *GoogleTrendsClient) parseTimelineResponse(keyword, geo string, raw []byte) (*TrendsData, error) {
	var resp trendsWidgetDataResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse widget response: %w", err)
	}

	points := make([]DataPoint, 0, len(resp.Default.TimelineData))
	peakValue := 0

	for _, tdp := range resp.Default.TimelineData {
		// Parse epoch timestamp (seconds)
		var ts int64
		if _, err := fmt.Sscanf(tdp.Time, "%d", &ts); err != nil {
			continue
		}
		date := time.Unix(ts, 0).UTC()

		val := 0
		if len(tdp.Value) > 0 {
			val = tdp.Value[0]
		}
		if val > peakValue {
			peakValue = val
		}

		points = append(points, DataPoint{Date: date, Value: val})
	}

	// Determine trend direction
	trend := "stable"
	if len(points) >= 2 {
		firstHalf := avgValue(points[:len(points)/2])
		secondHalf := avgValue(points[len(points)/2:])
		if secondHalf > firstHalf+5 {
			trend = "rising"
		} else if firstHalf > secondHalf+5 {
			trend = "falling"
		}
	}

	return &TrendsData{
		Keyword:   keyword,
		Region:    geo,
		Timeline:  points,
		Trend:     trend,
		PeakScore: peakValue,
	}, nil
}

func (c *GoogleTrendsClient) parseRelatedQueriesResponse(raw []byte) ([]TrendQuery, error) {
	var resp trendsWidgetDataResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse widget response: %w", err)
	}

	queries := make([]TrendQuery, 0)

	// The response contains rankedList entries: each has a title ("Top"/"Rising") + rankedKeyword array
	for _, rk := range resp.Default.RankedList {
		for _, item := range rk.RankedKeyword {
			status := "rising"
			if strings.EqualFold(rk.Title, "top") {
				status = "top"
			}
			if item.Value > 500 {
				status = "breakout"
			}

			queries = append(queries, TrendQuery{
				Query:  item.Query,
				Score:  item.Value,
				Status: status,
			})
		}
	}

	return queries, nil
}

// ── Cache Helpers ─────────────────────────────────────────────────────────────

func trendsCacheKey(dataType, keyword, geo, timeframe string) string {
	return fmt.Sprintf("trends:%s:%s:%s:%s", dataType, keyword, geo, timeframe)
}

func (c *GoogleTrendsClient) getCachedTrends(ctx context.Context, dataType, keyword, geo, timeframe string) *TrendsData {
	if c.cache == nil {
		return nil
	}
	key := trendsCacheKey(dataType, keyword, geo, timeframe)
	raw, err := c.cache.Get(ctx, key)
	if err != nil || raw == "" {
		return nil
	}
	var data TrendsData
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		slog.Warn("externaldata: failed to unmarshal cached trends", "key", key, "error", err)
		return nil
	}
	slog.Debug("externaldata: trends cache hit", "key", key)
	return &data
}

func (c *GoogleTrendsClient) setCachedTrends(ctx context.Context, dataType, keyword, geo, timeframe string, data *TrendsData) {
	if c.cache == nil || data == nil {
		return
	}
	key := trendsCacheKey(dataType, keyword, geo, timeframe)
	raw, err := json.Marshal(data)
	if err != nil {
		slog.Warn("externaldata: failed to marshal trends for cache", "key", key, "error", err)
		return
	}
	if err := c.cache.Set(ctx, key, string(raw), trendsCacheTTL); err != nil {
		slog.Warn("externaldata: failed to cache trends", "key", key, "error", err)
	}
}

func (c *GoogleTrendsClient) getCachedQueries(ctx context.Context, dataType, keyword, geo, timeframe string) []TrendQuery {
	if c.cache == nil {
		return nil
	}
	key := trendsCacheKey(dataType, keyword, geo, timeframe)
	raw, err := c.cache.Get(ctx, key)
	if err != nil || raw == "" {
		return nil
	}
	var queries []TrendQuery
	if err := json.Unmarshal([]byte(raw), &queries); err != nil {
		slog.Warn("externaldata: failed to unmarshal cached queries", "key", key, "error", err)
		return nil
	}
	slog.Debug("externaldata: queries cache hit", "key", key)
	return queries
}

func (c *GoogleTrendsClient) setCachedQueries(ctx context.Context, dataType, keyword, geo, timeframe string, queries []TrendQuery) {
	if c.cache == nil {
		return
	}
	key := trendsCacheKey(dataType, keyword, geo, timeframe)
	raw, err := json.Marshal(queries)
	if err != nil {
		slog.Warn("externaldata: failed to marshal queries for cache", "key", key, "error", err)
		return
	}
	if err := c.cache.Set(ctx, key, string(raw), trendsCacheTTL); err != nil {
		slog.Warn("externaldata: failed to cache queries", "key", key, "error", err)
	}
}

// ── Rate Limiting ─────────────────────────────────────────────────────────────

func (c *GoogleTrendsClient) rateLimit() {
	if c.lastReq.IsZero() {
		c.lastReq = time.Now()
		return
	}
	elapsed := time.Since(c.lastReq)
	if elapsed < trendsRequestPause {
		time.Sleep(trendsRequestPause - elapsed)
	}
	c.lastReq = time.Now()
}

// ── Mock Fallback ─────────────────────────────────────────────────────────────

func (c *GoogleTrendsClient) mockInterestOverTime(keywords []string, region string) *TrendsData {
	now := time.Now().UTC()
	points := make([]DataPoint, 0, 30)
	for i := 29; i >= 0; i-- {
		points = append(points, DataPoint{
			Date:  now.AddDate(0, 0, -i),
			Value: 50 + (i%10)*5,
		})
	}
	kw := "product"
	if len(keywords) > 0 {
		kw = keywords[0]
	}
	return &TrendsData{
		Keyword:   kw,
		Region:    region,
		Timeline:  points,
		Trend:     "stable",
		PeakScore: 85,
	}
}

func (c *GoogleTrendsClient) mockRelatedQueries(keyword string) []TrendQuery {
	// Deterministic-ish variety based on keyword length
	suffixes := []string{" sale", " reviews", " alternatives", " coupon", " near me",
		" vs", " for women", " for men", " wholesale", " clearance"}
	statuses := []string{"rising", "top", "rising", "rising", "rising", "rising", "top", "rising", "rising", "stable"}

	queries := make([]TrendQuery, 0, 5)
	rng := rand.New(rand.NewSource(int64(len(keyword))))
	for i := 0; i < 5; i++ {
		idx := rng.Intn(len(suffixes))
		queries = append(queries, TrendQuery{
			Query:  keyword + suffixes[idx],
			Score:  100 - i*15 - rng.Intn(10),
			Status: statuses[idx],
		})
	}
	return queries
}

// ── Utilities ─────────────────────────────────────────────────────────────────

// stripTrendsPrefix removes the )]}'\n prefix from Google Trends responses.
func stripTrendsPrefix(data []byte) []byte {
	s := string(data)
	// Cut after the first newline (the prefix is )]}'\n)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return []byte(s[idx+1:])
	}
	// Fallback: try stripping known prefix
	s = strings.TrimPrefix(s, ")]}'\n")
	return []byte(s)
}

func avgValue(points []DataPoint) float64 {
	if len(points) == 0 {
		return 0
	}
	sum := 0
	for _, p := range points {
		sum += p.Value
	}
	return float64(sum) / float64(len(points))
}
