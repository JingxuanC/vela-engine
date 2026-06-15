package insight

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/platform/externaldata"
	"github.com/JingxuanC/vela-engine/internal/platform/eventbus"
)

// ---------------------------------------------------------------------------
// Helper: setup in-memory SQLite DB with test data
// ---------------------------------------------------------------------------

type testOrder struct {
	ID        uint   `gorm:"primaryKey"`
	ShopID    string `gorm:"index"`
	CreatedAt string
}

type testReturn struct {
	ID           uint   `gorm:"primaryKey"`
	ShopID       string `gorm:"index"`
	ReturnReason string
	CreatedAt    string
}

type testReturnItem struct {
	ID       uint   `gorm:"primaryKey"`
	ReturnID uint   `gorm:"index"`
	Reason   string
	Size     string
}

type testProduct struct {
	ID          uint   `gorm:"primaryKey"`
	ShopID      string `gorm:"index"`
	ProductType string
	Price       float64
}

func setupTestDB(t *testing.T, orders []testOrder, returns []testReturn, products []testProduct) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	// Create tables
	db.AutoMigrate(&testOrder{}, &testReturn{}, &testReturnItem{}, &testProduct{})

	// Use raw SQL to create tables with the correct table names the engine queries
	db.Exec("CREATE TABLE IF NOT EXISTS synced_orders (id integer primary key, shop_id text, created_at text)")
	db.Exec("CREATE TABLE IF NOT EXISTS returns (id integer primary key, shop_id text, return_reason text, created_at text)")
	db.Exec("CREATE TABLE IF NOT EXISTS return_items (id integer primary key, return_id integer, reason text, size text)")
	db.Exec("CREATE TABLE IF NOT EXISTS synced_returns (id integer primary key, shop_id text, synced_at text, line_items text)")
	db.Exec("CREATE TABLE IF NOT EXISTS synced_products (id integer primary key, shop_id text, product_type text, price real)")

	for _, o := range orders {
		db.Exec("INSERT INTO synced_orders (shop_id, created_at) VALUES (?, ?)", o.ShopID, o.CreatedAt)
	}
	for i := range returns {
		r := &returns[i]
		db.Exec("INSERT INTO returns (shop_id, return_reason, created_at) VALUES (?, ?, ?)", r.ShopID, r.ReturnReason, r.CreatedAt)
		// Get the auto-generated return ID for return_items FK
		var returnID int
		db.Raw("SELECT last_insert_rowid()").Scan(&returnID)
		db.Exec("INSERT INTO return_items (return_id, reason) VALUES (?, ?)", returnID, r.ReturnReason)
	}
	for _, p := range products {
		db.Exec("INSERT INTO synced_products (shop_id, product_type, price) VALUES (?, ?, ?)", p.ShopID, p.ProductType, p.Price)
	}

	return db
}

func newTestEngine(db *gorm.DB) *InsightEngine {
	trends := externaldata.NewGoogleTrendsClient()
	eccompass := externaldata.NewECCompassClient("test-key")
	return NewInsightEngine(db, trends, eccompass, nil)
}

// ---------------------------------------------------------------------------
// Tests: Analyze
// ---------------------------------------------------------------------------

func TestAnalyze_HighReturnRate(t *testing.T) {
	db := setupTestDB(t,
		[]testOrder{
			{ShopID: "shop-1", CreatedAt: "2026-05-28T00:00:00Z"},
			{ShopID: "shop-1", CreatedAt: "2026-05-29T00:00:00Z"},
			{ShopID: "shop-1", CreatedAt: "2026-05-30T00:00:00Z"},
			{ShopID: "shop-1", CreatedAt: "2026-05-31T00:00:00Z"},
			{ShopID: "shop-1", CreatedAt: "2026-06-01T00:00:00Z"},
		},
		[]testReturn{
			{ShopID: "shop-1", ReturnReason: "size_too_small", CreatedAt: "2026-05-29T00:00:00Z"},
			{ShopID: "shop-1", ReturnReason: "not_as_described", CreatedAt: "2026-05-30T00:00:00Z"},
			{ShopID: "shop-1", ReturnReason: "defective", CreatedAt: "2026-05-31T00:00:00Z"},
		},
		[]testProduct{
			{ShopID: "shop-1", ProductType: "clothing", Price: 39.99},
			{ShopID: "shop-1", ProductType: "clothing", Price: 45.00},
			{ShopID: "shop-1", ProductType: "electronics", Price: 99.99},
		},
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	insights, err := engine.Analyze(ctx, "shop-1")
	require.NoError(t, err)
	require.NotEmpty(t, insights)

	// Find return rate insight
	var returnInsight *Insight
	for i := range insights {
		if insights[i].Type == InsightTypeReturnRate {
			returnInsight = &insights[i]
			break
		}
	}
	require.NotNil(t, returnInsight, "should have a return rate insight")
	assert.Equal(t, SeverityCritical, returnInsight.Severity)
	assert.Contains(t, returnInsight.Title, "Critical")

	// Should also have trend and pricing insights
	hasTrend := false
	hasPricing := false
	for i := range insights {
		if insights[i].Type == InsightTypeTrend {
			hasTrend = true
		}
		if insights[i].Type == InsightTypePricing {
			hasPricing = true
		}
	}
	assert.True(t, hasTrend, "should have a trend insight")
	assert.True(t, hasPricing, "should have a pricing insight")
}

func TestAnalyze_MediumReturnRate(t *testing.T) {
	db := setupTestDB(t,
		[]testOrder{
			{ShopID: "shop-2", CreatedAt: "2026-05-23T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-24T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-25T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-26T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-27T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-28T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-29T00:00:00Z"},
			{ShopID: "shop-2", CreatedAt: "2026-05-30T00:00:00Z"},
		},
		[]testReturn{
			{ShopID: "shop-2", ReturnReason: "size_too_large", CreatedAt: "2026-05-26T00:00:00Z"},
		},
		[]testProduct{},
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	insights, err := engine.Analyze(ctx, "shop-2")
	require.NoError(t, err)
	require.NotEmpty(t, insights)

	var returnInsight *Insight
	for i := range insights {
		if insights[i].Type == InsightTypeReturnRate {
			returnInsight = &insights[i]
			break
		}
	}
	require.NotNil(t, returnInsight)
	assert.Equal(t, SeverityMedium, returnInsight.Severity)
}

func TestAnalyze_LowReturnRate(t *testing.T) {
	db := setupTestDB(t,
		[]testOrder{
			{ShopID: "shop-3", CreatedAt: "2026-05-26T00:00:00Z"},
			{ShopID: "shop-3", CreatedAt: "2026-05-27T00:00:00Z"},
			{ShopID: "shop-3", CreatedAt: "2026-05-28T00:00:00Z"},
			{ShopID: "shop-3", CreatedAt: "2026-05-29T00:00:00Z"},
			{ShopID: "shop-3", CreatedAt: "2026-05-30T00:00:00Z"},
		},
		[]testReturn{}, // no returns
		[]testProduct{},
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	insights, err := engine.Analyze(ctx, "shop-3")
	require.NoError(t, err)
	require.NotEmpty(t, insights)

	var returnInsight *Insight
	for i := range insights {
		if insights[i].Type == InsightTypeReturnRate {
			returnInsight = &insights[i]
			break
		}
	}
	require.NotNil(t, returnInsight)
	assert.Equal(t, SeverityLow, returnInsight.Severity)
	assert.Contains(t, returnInsight.Body, "0.0")
}

func TestAnalyze_NoData(t *testing.T) {
	db := setupTestDB(t,
		[]testOrder{},
		[]testReturn{},
		[]testProduct{},
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	insights, err := engine.Analyze(ctx, "shop-empty")
	require.NoError(t, err)
	require.NotEmpty(t, insights)

	// Should get low-severity healthy rate insight
	assert.Equal(t, "Healthy return rate", insights[0].Title)
	assert.Equal(t, SeverityLow, insights[0].Severity)
}

func TestAnalyze_NoDB(t *testing.T) {
	engine := NewInsightEngine(nil,
		externaldata.NewGoogleTrendsClient(),
		externaldata.NewECCompassClient("test-key"),
		nil,
	)
	ctx := context.Background()

	// Should not panic with nil DB
	insights, err := engine.Analyze(ctx, "shop-nodb")
	require.NoError(t, err)
	require.NotEmpty(t, insights)
}

func TestAnalyze_ElectronicsCategory(t *testing.T) {
	db := setupTestDB(t,
		[]testOrder{
			{ShopID: "shop-elec", CreatedAt: "2025-04-01T00:00:00Z"},
		},
		[]testReturn{},
		[]testProduct{
			{ShopID: "shop-elec", ProductType: "electronics", Price: 199.99},
			{ShopID: "shop-elec", ProductType: "electronics", Price: 249.99},
			{ShopID: "shop-elec", ProductType: "electronics", Price: 179.99},
			{ShopID: "shop-elec", ProductType: "accessories", Price: 12.99},
		},
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	insights, err := engine.Analyze(ctx, "shop-elec")
	require.NoError(t, err)
	require.NotEmpty(t, insights)

	// Check pricing insight for electronics
	var pricingInsight *Insight
	for i := range insights {
		if insights[i].Type == InsightTypePricing && insights[i].Data != nil {
			if data, ok := insights[i].Data.(map[string]interface{}); ok {
				if cat, ok := data["category"]; ok && cat == "electronics" {
					pricingInsight = &insights[i]
					break
				}
			}
		}
	}
	require.NotNil(t, pricingInsight, "should have electronics pricing insight")
	assert.Contains(t, pricingInsight.Body, "$209.99") // avg of 199.99, 249.99, 179.99
}

// ---------------------------------------------------------------------------
// Tests: OnEvent
// ---------------------------------------------------------------------------

func TestOnEvent_OrderUpdated(t *testing.T) {
	db := setupTestDB(t,
		[]testOrder{
			{ShopID: "shop-ev", CreatedAt: "2025-05-01T00:00:00Z"},
		},
		[]testReturn{
			{ShopID: "shop-ev", ReturnReason: "defective", CreatedAt: "2025-05-01T00:00:00Z"},
		},
		nil,
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	ev, err := eventbus.NewEvent(eventbus.EventOrderUpdated, shopIDFromString("shop-ev"), nil, "test")
	require.NoError(t, err)

	// Should not error
	err = engine.OnEvent(ctx, ev)
	assert.NoError(t, err)
}

func TestOnEvent_ProductUpdated(t *testing.T) {
	db := setupTestDB(t,
		nil,
		nil,
		[]testProduct{
			{ShopID: "shop-pv", ProductType: "home_garden", Price: 29.99},
		},
	)
	engine := newTestEngine(db)
	ctx := context.Background()

	ev, err := eventbus.NewEvent(eventbus.EventProductUpdated, shopIDFromString("shop-pv"), nil, "test")
	require.NoError(t, err)

	err = engine.OnEvent(ctx, ev)
	assert.NoError(t, err)
}

func TestOnEvent_UnhandledType(t *testing.T) {
	engine := newTestEngine(setupTestDB(t, nil, nil, nil))
	ctx := context.Background()

	ev, err := eventbus.NewEvent(eventbus.EventCustomerCreated, shopIDFromString("shop-unhandled"), nil, "test")
	require.NoError(t, err)

	// Should not error for unhandled events
	err = engine.OnEvent(ctx, ev)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Tests: Insight types and helpers
// ---------------------------------------------------------------------------

func TestInsightTypes(t *testing.T) {
	assert.Equal(t, InsightType("pricing"), InsightTypePricing)
	assert.Equal(t, InsightType("trend"), InsightTypeTrend)
	assert.Equal(t, InsightType("return_rate"), InsightTypeReturnRate)
	assert.Equal(t, InsightType("category_shift"), InsightTypeCategoryShift)
	assert.Equal(t, InsightType("competitor"), InsightTypeCompetitor)
	assert.Equal(t, InsightType("inventory"), InsightTypeInventory)
}

func TestSeverityLevels(t *testing.T) {
	assert.Equal(t, Severity("low"), SeverityLow)
	assert.Equal(t, Severity("medium"), SeverityMedium)
	assert.Equal(t, Severity("high"), SeverityHigh)
	assert.Equal(t, Severity("critical"), SeverityCritical)
}

func TestMarshalUnmarshalInsights(t *testing.T) {
	insights := []Insight{
		{
			Type:      InsightTypeReturnRate,
			Severity:  SeverityHigh,
			Title:     "Test insight",
			Body:      "This is a test body",
			ActionURL: "/api/insights/1",
			Data:      map[string]interface{}{"rate": 12.5},
		},
	}

	data, err := MarshalInsights(insights)
	require.NoError(t, err)
	require.NotEmpty(t, data)

	var parsed []Insight
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)
	require.Len(t, parsed, 1)

	assert.Equal(t, InsightTypeReturnRate, parsed[0].Type)
	assert.Equal(t, SeverityHigh, parsed[0].Severity)
	assert.Equal(t, "Test insight", parsed[0].Title)
	assert.Equal(t, "This is a test body", parsed[0].Body)
	assert.Equal(t, "/api/insights/1", parsed[0].ActionURL)
}

func TestNewInsightEngine(t *testing.T) {
	db, _ := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	trends := externaldata.NewGoogleTrendsClient()
	eccompass := externaldata.NewECCompassClient("key2")

	engine := NewInsightEngine(db, trends, eccompass, nil)
	assert.NotNil(t, engine)
	assert.Equal(t, db, engine.db)
	assert.Equal(t, trends, engine.trends)
	assert.Equal(t, eccompass, engine.eccompass)
}

func TestAnalyzeReturnRate_EdgeCases(t *testing.T) {
	engine := newTestEngine(setupTestDB(t, nil, nil, nil))
	now := time.Now()

	t.Run("zero rate", func(t *testing.T) {
		insights := engine.analyzeReturnRate(0, 10, 0, []string{}, &now)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityLow, insights[0].Severity)
		assert.Contains(t, insights[0].Body, "0.0")
	})

	t.Run("very high rate", func(t *testing.T) {
		insights := engine.analyzeReturnRate(35.5, 20, 7, []string{"defective", "damaged"}, &now)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityCritical, insights[0].Severity)
		assert.Contains(t, insights[0].Body, "35.5")
	})

	t.Run("with reasons", func(t *testing.T) {
		insights := engine.analyzeReturnRate(18.2, 15, 3, []string{"size_too_small", "not_as_described"}, &now)
		require.Len(t, insights, 1)
		if data, ok := insights[0].Data.(map[string]interface{}); ok {
			assert.Equal(t, 18.2, data["return_rate"])
			reasons, ok := data["top_reasons"].([]string)
			assert.True(t, ok)
			assert.Contains(t, reasons, "size_too_small")
		}
	})
}

func TestAnalyzePricing_EdgeCases(t *testing.T) {
	engine := newTestEngine(setupTestDB(t, nil, nil, nil))
	ctx := context.Background()

	t.Run("nil pricing data", func(t *testing.T) {
		insights := engine.analyzePricing("test", 50.00, nil)
		assert.Nil(t, insights)
	})

	t.Run("shop price much higher than market", func(t *testing.T) {
		pricing, err := engine.eccompass.GetCategoryPricing(ctx, "electronics")
		require.NoError(t, err)
		insights := engine.analyzePricing("electronics", 250.00, pricing)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityHigh, insights[0].Severity)
		assert.Contains(t, insights[0].Title, "significantly above")
	})

	t.Run("shop price much lower than market", func(t *testing.T) {
		pricing, err := engine.eccompass.GetCategoryPricing(ctx, "electronics")
		require.NoError(t, err)
		insights := engine.analyzePricing("electronics", 15.00, pricing)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityMedium, insights[0].Severity)
		assert.Contains(t, insights[0].Title, "well below")
	})

	t.Run("prices aligned", func(t *testing.T) {
		pricing, err := engine.eccompass.GetCategoryPricing(ctx, "clothing")
		require.NoError(t, err)
		insights := engine.analyzePricing("clothing", 39.99, pricing)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityLow, insights[0].Severity)
		assert.Contains(t, insights[0].Title, "Competitive")
	})
}

func TestAnalyzeTrend_EdgeCases(t *testing.T) {
	engine := newTestEngine(setupTestDB(t, nil, nil, nil))

	t.Run("nil trends data", func(t *testing.T) {
		insights := engine.analyzeTrend("test", nil)
		assert.Nil(t, insights)
	})

	t.Run("rising trend", func(t *testing.T) {
		data := &externaldata.TrendsData{
			Keyword: "wireless earbuds", Trend: "rising", PeakScore: 90,
		}
		insights := engine.analyzeTrend("electronics", data)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityHigh, insights[0].Severity)
		assert.Contains(t, insights[0].Title, "Rising")
	})

	t.Run("falling trend", func(t *testing.T) {
		data := &externaldata.TrendsData{
			Keyword: "desktop pc", Trend: "falling", PeakScore: 40,
		}
		insights := engine.analyzeTrend("electronics", data)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityMedium, insights[0].Severity)
		assert.Contains(t, insights[0].Title, "Declining")
	})

	t.Run("stable trend", func(t *testing.T) {
		data := &externaldata.TrendsData{
			Keyword: "t-shirts", Trend: "stable", PeakScore: 50,
		}
		insights := engine.analyzeTrend("clothing", data)
		require.Len(t, insights, 1)
		assert.Equal(t, SeverityLow, insights[0].Severity)
		assert.Contains(t, insights[0].Title, "Stable")
	})
}

// ---------------------------------------------------------------------------
// Helper: parse shop ID string to uuid.UUID
// ---------------------------------------------------------------------------

func shopIDFromString(s string) [16]byte {
	var id [16]byte
	copy(id[:], s)
	return id
}
