package insight

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// setupInjectorTestDB creates an in-memory SQLite database with all relevant tables
// and inserts a known shopID. Returns (db, shopID, cleanup).
func setupInjectorTestDB(t *testing.T) (*gorm.DB, string, func()) {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}

	// Create tables matching the production schema (minimal columns for fetchers)
	db.Exec(`CREATE TABLE synced_orders (
		id TEXT PRIMARY KEY,
		shop_id TEXT NOT NULL,
		platform_id TEXT NOT NULL,
		order_number INTEGER,
		total_price REAL DEFAULT 0,
		currency TEXT DEFAULT 'USD',
		financial_status TEXT DEFAULT '',
		fulfillment_status TEXT DEFAULT '',
		line_items JSON,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE synced_products (
		id TEXT PRIMARY KEY,
		shop_id TEXT NOT NULL,
		platform_id TEXT NOT NULL,
		title TEXT DEFAULT '',
		product_type TEXT DEFAULT '',
		tags TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE returns (
		id TEXT PRIMARY KEY,
		shop_id TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	db.Exec(`CREATE TABLE return_items (
		id TEXT PRIMARY KEY,
		return_id TEXT NOT NULL,
		size TEXT DEFAULT '',
		reason TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		FOREIGN KEY (return_id) REFERENCES returns(id)
	)`)
	db.Exec(`CREATE TABLE try_on_records (
		id TEXT PRIMARY KEY,
		shop_id TEXT NOT NULL,
		task_id TEXT NOT NULL,
		product_id TEXT DEFAULT '',
		status TEXT DEFAULT 'pending',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)

	shopID := uuid.New().String()
	now := time.Now().UTC()

	// Helper: insert returns with items
	type retRow struct {
		id        string
		shopID    string
		createdAt time.Time
	}
	type retItemRow struct {
		id       string
		returnID string
		size     string
		reason   string
	}

	insertReturn := func(r retRow, items []retItemRow) {
		db.Exec("INSERT INTO returns (id, shop_id, created_at) VALUES (?, ?, ?)",
			r.id, r.shopID, r.createdAt)
		for _, it := range items {
			db.Exec("INSERT INTO return_items (id, return_id, size, reason) VALUES (?, ?, ?, ?)",
				it.id, it.returnID, it.size, it.reason)
		}
	}

	// Returns in the last 30 days
	insertReturn(retRow{"r1", shopID, now.Add(-5 * 24 * time.Hour)}, []retItemRow{
		{"ri1", "r1", "M", "偏小"},
		{"ri2", "r1", "L", "偏大"},
	})
	insertReturn(retRow{"r2", shopID, now.Add(-10 * 24 * time.Hour)}, []retItemRow{
		{"ri3", "r2", "M", "偏小"},
	})
	insertReturn(retRow{"r3", shopID, now.Add(-2 * 24 * time.Hour)}, []retItemRow{
		{"ri4", "r3", "S", "与描述不符"},
	})
	// Return older than 30 days — should NOT appear in results
	insertReturn(retRow{"r_old", shopID, now.Add(-60 * 24 * time.Hour)}, []retItemRow{
		{"ri_old", "r_old", "XL", "偏大"},
	})
	// Return for a different shop — should NOT appear
	insertReturn(retRow{"r_other", uuid.New().String(), now.Add(-3 * 24 * time.Hour)}, []retItemRow{
		{"ri_other", "r_other", "M", "偏小"},
	})

	// Insert orders with line_items (JSONB)
	db.Exec(`INSERT INTO synced_orders (id, shop_id, platform_id, order_number, total_price, financial_status, fulfillment_status, line_items, created_at) VALUES
		(?, ?, 'ord1', 1001, 59.99, 'paid', 'fulfilled', ?, ?),
		(?, ?, 'ord2', 1002, 120.00, 'paid', 'unfulfilled', ?, ?),
		(?, ?, 'ord3', 1003, 45.50, 'pending', 'unfulfilled', ?, ?)`,
		uuid.New().String(), shopID,
		datatypes.JSON([]byte(`[{"id":1,"title":"夏季连衣裙","quantity":2,"price":"29.99","sku":"DR001"}]`)),
		now.Add(-1*24*time.Hour),
		uuid.New().String(), shopID,
		datatypes.JSON([]byte(`[{"id":2,"title":"牛仔裤","quantity":1,"price":"120.00","sku":"JE001"}]`)),
		now.Add(-3*24*time.Hour),
		uuid.New().String(), shopID,
		datatypes.JSON([]byte(`[{"id":3,"title":"T恤","quantity":3,"price":"15.00","sku":"TS001"}]`)),
		now.Add(-7*24*time.Hour),
	)

	// Insert products
	db.Exec(`INSERT INTO synced_products (id, shop_id, platform_id, title, product_type, tags) VALUES
		(?, ?, 'prod1', '夏季连衣裙', 'Dresses', 'summer, floral'),
		(?, ?, 'prod2', '牛仔裤', 'Pants', 'denim, casual'),
		(?, ?, 'prod3', '纯棉T恤', 'Tops', 'cotton, basic')`,
		uuid.New().String(), shopID,
		uuid.New().String(), shopID,
		uuid.New().String(), shopID,
	)

	// Insert try-on records
	db.Exec(`INSERT INTO try_on_records (id, shop_id, task_id, product_id, status, created_at) VALUES
		(?, ?, 'task1', 'prod1', 'completed', ?),
		(?, ?, 'task2', 'prod2', 'completed', ?),
		(?, ?, 'task3', 'prod3', 'failed', ?)`,
		uuid.New().String(), shopID, now.Add(-2*24*time.Hour),
		uuid.New().String(), shopID, now.Add(-4*24*time.Hour),
		uuid.New().String(), shopID, now.Add(-6*24*time.Hour),
	)

	cleanup := func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
	}

	return db, shopID, cleanup
}

// --- Fetcher Tests ---

func TestReturnFetcher(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	f := &ReturnFetcher{}
	ctx := context.Background()
	out := f.Fetch(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected size return data, got empty string")
	}
	if !strings.Contains(out, "M") {
		t.Errorf("expected size M in output, got: %s", out)
	}
	if !strings.Contains(out, "偏小") {
		t.Errorf("expected '偏小' reason in output, got: %s", out)
	}
	if strings.Contains(out, "[尺码退货") {
		t.Logf("PASS: ReturnFetcher output = %s", out)
	} else {
		t.Errorf("output missing expected prefix, got: %s", out)
	}

	// Empty result for unknown shop
	empty := f.Fetch(ctx, db, uuid.New().String())
	if empty != "" {
		t.Errorf("expected empty for unknown shop, got: %s", empty)
	}

	// Nil DB
	if f.Fetch(ctx, nil, shopID) != "" {
		t.Error("expected empty for nil db")
	}
}

func TestOrderStatusFetcher(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	f := &OrderStatusFetcher{}
	ctx := context.Background()
	out := f.Fetch(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected order data, got empty")
	}
	if !strings.Contains(out, "#1001") && !strings.Contains(out, "#1002") && !strings.Contains(out, "#1003") {
		t.Errorf("expected order numbers in output, got: %s", out)
	}
	if !strings.Contains(out, "[最近订单]") {
		t.Errorf("output missing prefix, got: %s", out)
	}

	// Unknown shop
	if f.Fetch(ctx, db, uuid.New().String()) != "" {
		t.Error("expected empty for unknown shop")
	}
}

func TestProductDataFetcher(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	f := &ProductDataFetcher{}
	ctx := context.Background()
	out := f.Fetch(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected product data, got empty")
	}
	if !strings.Contains(out, "夏季连衣裙") {
		t.Errorf("expected '夏季连衣裙' in output, got: %s", out)
	}
	if !strings.Contains(out, "[产品列表]") {
		t.Errorf("output missing prefix, got: %s", out)
	}
}

// TestReturnFetcher_ReasonOnly verifies ReturnFetcher also produces reason aggregation.
func TestReturnFetcher_ReasonAggregation(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	f := &ReturnFetcher{}
	ctx := context.Background()
	out := f.Fetch(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected return analysis data, got empty")
	}
	if !strings.Contains(out, "偏小") {
		t.Errorf("expected '偏小' in output, got: %s", out)
	}
	// Merged fetcher outputs both size+reason detail AND reason aggregation
	if !strings.Contains(out, "[退货原因 近30天]") {
		t.Errorf("output missing [退货原因] prefix, got: %s", out)
	}
}

func TestInventoryVelocityFetcher(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	f := &InventoryVelocityFetcher{}
	ctx := context.Background()
	out := f.Fetch(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected inventory velocity data, got empty")
	}
	if !strings.Contains(out, "×") {
		t.Errorf("expected quantity indicator in output, got: %s", out)
	}
	if !strings.Contains(out, "[销量 近30天]") {
		t.Errorf("output missing prefix, got: %s", out)
	}
}

func TestTryOnHistoryFetcher(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	f := &TryOnHistoryFetcher{}
	ctx := context.Background()
	out := f.Fetch(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected try-on history data, got empty")
	}
	if !strings.Contains(out, "completed") {
		t.Errorf("expected 'completed' status in output, got: %s", out)
	}
	if !strings.Contains(out, "[试穿记录]") {
		t.Errorf("output missing prefix, got: %s", out)
	}
}

func TestContextInjector(t *testing.T) {
	db, shopID, cleanup := setupInjectorTestDB(t)
	defer cleanup()

	inj := NewContextInjector()
	inj.Register(&ReturnFetcher{})
	inj.Register(&OrderStatusFetcher{})
	inj.Register(&ProductDataFetcher{})
	inj.Register(&InventoryVelocityFetcher{})
	inj.Register(&TryOnHistoryFetcher{})

	ctx := context.Background()
	out := inj.Inject(ctx, db, shopID)

	if out == "" {
		t.Fatal("expected injected context, got empty")
	}
	// Should contain all 6 fetcher prefixes
	for _, prefix := range []string{
		"[尺码退货", "[最近订单]", "[产品列表]", "[退货原因", "[销量", "[试穿记录]",
	} {
		if !strings.Contains(out, prefix) {
			t.Errorf("missing fetcher output for prefix: %s\nfull output:\n%s", prefix, out)
		}
	}

	// InjectOne should find named fetcher
	one := inj.InjectOne(ctx, db, shopID, "return_insight")
	if one == "" {
		t.Error("InjectOne(return_insight) returned empty")
	}
	if !strings.Contains(one, "[尺码退货") {
		t.Errorf("InjectOne unexpected output: %s", one)
	}

	// InjectOne unknown name
	unknown := inj.InjectOne(ctx, db, shopID, "nonexistent")
	if unknown != "" {
		t.Errorf("expected empty for unknown fetcher, got: %s", unknown)
	}

	// Empty shopID
	if inj.Inject(ctx, db, "") != "" {
		t.Error("expected empty for empty shopID")
	}

	// Nil DB
	if inj.Inject(ctx, nil, shopID) != "" {
		t.Error("expected empty for nil db")
	}
}

func TestFetcherNilDB(t *testing.T) {
	// All fetchers should handle nil db gracefully
	ctx := context.Background()
	fetchers := []DataFetcher{
		&ReturnFetcher{},
		&OrderStatusFetcher{},
		&ProductDataFetcher{},
		&InventoryVelocityFetcher{},
		&TryOnHistoryFetcher{},
	}
	for _, f := range fetchers {
		t.Run(f.Name(), func(t *testing.T) {
			if out := f.Fetch(ctx, nil, "some-shop"); out != "" {
				t.Errorf("expected empty for nil db, got: %s", out)
			}
		})
	}
}
