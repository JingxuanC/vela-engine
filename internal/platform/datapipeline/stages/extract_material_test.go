package stages

import (
	"context"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

func TestExtractMaterialStage_Name(t *testing.T) {
	s := &ExtractMaterialStage{}
	if got := s.Name(); got != "extract_material" {
		t.Errorf("Name() = %q, want %q", got, "extract_material")
	}
}

func TestExtractMaterialStage_IsCritical(t *testing.T) {
	s := &ExtractMaterialStage{}
	if s.IsCritical() {
		t.Error("IsCritical() = true, want false")
	}
}

func TestExtractMaterialStage_Process_FromBodyHTML(t *testing.T) {
	payload := []byte(`{"body_html": "100% Cotton premium fabric", "title": "Classic Tee"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	materials, ok := data.Fields["materials"].([]map[string]any)
	if !ok {
		t.Fatal("materials is not []map[string]any")
	}
	if len(materials) != 1 {
		t.Fatalf("expected 1 material, got %d", len(materials))
	}
	if materials[0]["material"] != "cotton" {
		t.Errorf("material = %v, want cotton", materials[0]["material"])
	}
	if materials[0]["percentage"].(float64) != 100 {
		t.Errorf("percentage = %v, want 100", materials[0]["percentage"])
	}
}

func TestExtractMaterialStage_Process_MultipleMaterials(t *testing.T) {
	payload := []byte(`{"body_html": "65% polyester 35% cotton blend", "title": "T-Shirt"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	materials, ok := data.Fields["materials"].([]map[string]any)
	if !ok {
		t.Fatal("materials is not []map[string]any")
	}
	// Deduplication only happens on material name, so we should get both.
	if len(materials) != 2 {
		t.Fatalf("expected 2 materials, got %d: %v", len(materials), materials)
	}
}

func TestExtractMaterialStage_Process_FromTitle(t *testing.T) {
	payload := []byte(`{"title": "100% Silk Scarf"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	materials, _ := data.Fields["materials"].([]map[string]any)
	if len(materials) != 1 {
		t.Fatalf("expected 1 material, got %d", len(materials))
	}
	if materials[0]["material"] != "silk" {
		t.Errorf("material = %v, want silk", materials[0]["material"])
	}
}

func TestExtractMaterialStage_Process_NoMaterials(t *testing.T) {
	payload := []byte(`{"title": "Generic Product", "body_html": "<p>No material info here</p>"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error when no material found")
	}
}

func TestExtractMaterialStage_Process_EmptyPayload(t *testing.T) {
	payload := []byte(`{"title": "", "body_html": ""}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error when no material found")
	}
}

func TestExtractMaterialStage_Process_InvalidJSON(t *testing.T) {
	payload := []byte(`[bad`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractMaterialStage_Process_SpandexAlternative(t *testing.T) {
	payload := []byte(`{"body_html": "95% cotton 5% elastane"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	materials, _ := data.Fields["materials"].([]map[string]any)
	// Should have cotton + elastane (both listed in the pattern).
	if len(materials) != 2 {
		t.Fatalf("expected 2 materials, got %d: %v", len(materials), materials)
	}
}

func TestExtractMaterialStage_Process_Wool(t *testing.T) {
	payload := []byte(`{"body_html": "80% wool 20% nylon", "title": "Sweater"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ExtractMaterialStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	materials, _ := data.Fields["materials"].([]map[string]any)
	if len(materials) != 2 {
		t.Fatalf("expected 2 materials, got %d: %v", len(materials), materials)
	}
}
