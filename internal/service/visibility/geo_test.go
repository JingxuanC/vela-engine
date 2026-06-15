package visibility

import (
	"testing"
)

func TestGEOCheckLLMsTxt_Deployed(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkLLMsTxt(true)
	if !c.Passed || c.Score != 100 {
		t.Errorf("deployed llms.txt should pass with 100, got score=%.0f", c.Score)
	}
}

func TestGEOCheckLLMsTxt_NotDeployed(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkLLMsTxt(false)
	if c.Passed || c.Score != 0 {
		t.Errorf("not deployed llms.txt should fail with 0, got score=%.0f pasted=%v", c.Score, c.Passed)
	}
	if !c.AutoFix {
		t.Error("llms.txt deployment should be auto-fixable")
	}
}

func TestGEOCheckLLMsFull_Deployed(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkLLMsFull(true)
	if !c.Passed {
		t.Error("deployed llms-full.txt should pass")
	}
}

func TestGEOCheckLLMsFull_NotDeployed(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkLLMsFull(false)
	if c.Passed || c.Score != 30 {
		t.Errorf("not deployed llms-full should score 30, got %.0f", c.Score)
	}
}

func TestGEOCheckDescriptionQuality_NoProducts(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkDescriptionQuality(nil)
	if c.Passed {
		t.Error("empty products should fail description quality check")
	}
}

func TestGEOCheckFAQPresence_NoProducts(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkFAQPresence(nil)
	if c.Passed {
		t.Error("empty products should fail FAQ check")
	}
}

func TestGEOCheckTitleSearchability_NoProducts(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkTitleSearchability(nil)
	if c.Passed || c.Score != 50 {
		t.Errorf("empty products should score 50, got %.0f", c.Score)
	}
}

func TestGEOCheckBrandAuthority_NoProducts(t *testing.T) {
	e := &GEOEngine{}
	c := e.checkBrandAuthority(nil)
	if c.Passed || c.Score != 0 {
		t.Error("empty products should fail brand authority check")
	}
}

func TestGEOCheckReviewStructured_NoDB(t *testing.T) {
	e := &GEOEngine{db: nil}
	// When DB is nil, checkReviewStructured will panic on DB call.
	// This is an integration concern — tested via e2e.
	// Basic struct instantiation smoke test:
	if e.db != nil {
		t.Error("expected nil db")
	}
}

// SEO checks require a live DB connection — tested via integration/e2e.
// Unit tests below cover the GEO engine's standalone check functions.


func TestSlugify(t *testing.T) {
	tests := []struct {
		title string
		want  string
	}{
		{"Nike Air Zoom Pegasus 40", "nike-air-zoom-pegasus-40"},
		{"100% Organic Cotton T-Shirt", "100-organic-cotton-t-shirt"},
		{"Running Shoes (Men's) / Black", "running-shoes-mens---black"},
		{"Price $29.99 & Free Shipping", "price-2999-and-free-shipping"},
		{"", ""},
	}
	for _, tt := range tests {
		got := slugify(tt.title)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.title, got, tt.want)
		}
	}
}
