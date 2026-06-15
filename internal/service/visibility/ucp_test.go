package visibility

import (
	"encoding/json"
	"testing"

	"gorm.io/datatypes"

	"github.com/JingxuanC/vela-engine/internal/model"
)

func makeProduct(title, desc, productType, vendor string, options, variants, images interface{}) *model.SyncedProduct {
	opts, _ := json.Marshal(options)
	vars, _ := json.Marshal(variants)
	imgs, _ := json.Marshal(images)
	return &model.SyncedProduct{
		Title:       title,
		Description: desc,
		ProductType: productType,
		Vendor:      vendor,
		Options:     datatypes.JSON(opts),
		Variants:    datatypes.JSON(vars),
		Images:      datatypes.JSON(imgs),
		Status:      "active",
		PlatformID:  "test-prod-1",
	}
}

func TestUCPCheckTitle_Good(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("Nike Air Zoom Pegasus 40 — Mesh Upper, Zoom Air Cushion", "", "", "", nil, nil, nil)
	c := e.checkTitle(p)
	if !c.Passed {
		t.Errorf("good title should pass: %s", c.Message)
	}
}

func TestUCPCheckTitle_Short(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("T-shirt", "", "", "", nil, nil, nil)
	c := e.checkTitle(p)
	if c.Passed {
		t.Error("short title should fail")
	}
}

func TestUCPCheckTitle_Hype(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("🔥 爆款限时抢购跑步鞋！", "", "", "", nil, nil, nil)
	c := e.checkTitle(p)
	if c.Passed {
		t.Error("hype title should fail")
	}
}

func TestUCPCheckTitle_Empty(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("", "", "", "", nil, nil, nil)
	c := e.checkTitle(p)
	if c.Passed || c.Score != 0 {
		t.Errorf("empty title should score 0")
	}
}

func TestUCPCheckTaxonomy(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("Test", "", "Clothing", "", nil, nil, nil)
	c := e.checkTaxonomy(p)
	if !c.Passed {
		t.Error("product with taxonomy should pass")
	}

	p2 := makeProduct("Test", "", "", "", nil, nil, nil)
	c2 := e.checkTaxonomy(p2)
	if c2.Passed {
		t.Error("product without taxonomy should fail")
	}
}

func TestUCPCheckVariants_Complete(t *testing.T) {
	e := &UCPEngine{}
	options := []map[string]interface{}{
		{"name": "Size", "values": []string{"S", "M", "L"}},
		{"name": "Color", "values": []string{"Red", "Blue"}},
	}
	p := makeProduct("Test", "", "", "", options,
		[]map[string]interface{}{{"price": "29.99"}},
		[]map[string]string{{"src": "img.jpg"}})
	c := e.checkVariants(p)
	if !c.Passed {
		t.Errorf("product with size+color variants should pass: %s", c.Message)
	}
}

func TestUCPCheckVariants_NoVariants(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("Test", "", "", "", nil, nil, nil)
	c := e.checkVariants(p)
	if c.Passed || c.Score != 0 {
		t.Error("product with no variants should fail")
	}
}

func TestUCPCheckVariants_MissingColor(t *testing.T) {
	e := &UCPEngine{}
	options := []map[string]interface{}{
		{"name": "Size", "values": []string{"S", "M", "L"}},
	}
	p := makeProduct("Test", "", "", "", options,
		[]map[string]interface{}{{"price": "29.99"}},
		[]map[string]string{{"src": "img.jpg"}})
	c := e.checkVariants(p)
	if c.Passed {
		t.Error("product missing color variant should fail")
	}
}

func TestUCPCheckInventory_Positive(t *testing.T) {
	e := &UCPEngine{}
	variants := []map[string]interface{}{
		{"inventory_quantity": 10},
		{"inventory_quantity": 5},
	}
	p := makeProduct("Test", "", "", "", nil, variants, nil)
	c := e.checkInventory(p)
	if !c.Passed {
		t.Error("products with inventory > 0 should pass")
	}
}

func TestUCPCheckInventory_Negative(t *testing.T) {
	e := &UCPEngine{}
	variants := []map[string]interface{}{
		{"inventory_quantity": -1},
	}
	p := makeProduct("Test", "", "", "", nil, variants, nil)
	c := e.checkInventory(p)
	if c.Passed || c.Score != 20 {
		t.Errorf("negative inventory should score 20, got %.0f", c.Score)
	}
}

func TestUCPCheckInventory_Zero(t *testing.T) {
	e := &UCPEngine{}
	variants := []map[string]interface{}{
		{"inventory_quantity": 0},
	}
	p := makeProduct("Test", "", "", "", nil, variants, nil)
	c := e.checkInventory(p)
	if c.Passed || c.Score != 40 {
		t.Errorf("zero inventory should score 40, got %.0f", c.Score)
	}
}

func TestUCPCheckPrice(t *testing.T) {
	e := &UCPEngine{}
	variants := []map[string]interface{}{
		{"price": "29.99"},
	}
	p := makeProduct("Test", "", "", "", nil, variants, nil)
	c := e.checkPrice(p)
	if !c.Passed {
		t.Error("product with price should pass")
	}

	p2 := makeProduct("Test", "", "", "", nil,
		[]map[string]interface{}{{"price": "0.00"}},
		nil)
	c2 := e.checkPrice(p2)
	if c2.Passed {
		t.Error("product with zero price should fail")
	}
}

func TestUCPCheckImages(t *testing.T) {
	e := &UCPEngine{}

	// 0 images
	p := makeProduct("Test", "", "", "", nil, nil, nil)
	c := e.checkImages(p)
	if c.Passed || c.Score != 0 {
		t.Error("no images should fail")
	}

	// 1 image
	p1 := makeProduct("Test", "", "", "", nil, nil,
		[]map[string]string{{"src": "img1.jpg"}})
	c1 := e.checkImages(p1)
	if c1.Passed || c1.Score != 50 {
		t.Errorf("1 image should score 50, got %.0f", c1.Score)
	}

	// 3 images
	p3 := makeProduct("Test", "", "", "", nil, nil,
		[]map[string]string{{"src": "a.jpg"}, {"src": "b.jpg"}, {"src": "c.jpg"}})
	c3 := e.checkImages(p3)
	if !c3.Passed {
		t.Error("3 images should pass")
	}
}

func TestUCPCheckDescription(t *testing.T) {
	e := &UCPEngine{}

	// Empty
	p := makeProduct("Test", "", "", "", nil, nil, nil)
	c := e.checkDescription(p)
	if c.Passed || c.Score != 0 {
		t.Error("empty description should fail")
	}

	// Short
	p2 := makeProduct("Test", "Too short", "", "", nil, nil, nil)
	c2 := e.checkDescription(p2)
	if c2.Passed || c2.Score != 40 {
		t.Errorf("short description should score 40, got %.0f", c2.Score)
	}

	// Good
	p3 := makeProduct("Test", "A comprehensive product description that is long enough for AI agents to understand what this product is about and its key features", "", "", nil, nil, nil)
	c3 := e.checkDescription(p3)
	if !c3.Passed {
		t.Error("long description should pass")
	}
}

func TestUCPCheckGlobalCatalog(t *testing.T) {
	e := &UCPEngine{}

	p := makeProduct("Test", "", "", "", nil, nil, nil)
	p.Status = "active"
	c := e.checkGlobalCatalog(p)
	if !c.Passed {
		t.Error("active product should pass Global Catalog check")
	}

	p2 := makeProduct("Test", "", "", "", nil, nil, nil)
	p2.Status = "draft"
	c2 := e.checkGlobalCatalog(p2)
	if c2.Passed {
		t.Error("draft product should fail Global Catalog check")
	}
}

func TestUCPCheckProduct_WeightsSum(t *testing.T) {
	e := &UCPEngine{}
	p := makeProduct("Nike Shoes", "A nice pair of running shoes for daily training and marathon preparation with breathable mesh upper", "Footwear", "Nike",
		[]map[string]interface{}{
			{"name": "Size", "values": []string{"8", "9", "10"}},
			{"name": "Color", "values": []string{"Black", "White"}},
		},
		[]map[string]interface{}{{"price": "129.99", "inventory_quantity": 25}},
		[]map[string]string{{"src": "a.jpg"}, {"src": "b.jpg"}, {"src": "c.jpg"}},
	)
	p.Status = "active"

	score := e.CheckProduct(nil, p)
	if len(score.Checks) != 8 {
		t.Errorf("expected 8 checks, got %d", len(score.Checks))
	}

	// Verify weights sum to 1.0
	var total float64
	for _, c := range score.Checks {
		total += c.Weight
	}
	if total != 1.0 {
		t.Errorf("weights should sum to 1.0, got %.2f", total)
	}

	// All checks should pass for this well-formed product
	if score.Score != 100 {
		t.Errorf("well-formed product should score 100, got %.1f — check individual scores", score.Score)
		for _, c := range score.Checks {
			t.Logf("  %s: %.0f (passed=%v) — %s", c.Name, c.Score, c.Passed, c.Message)
		}
	}
}
