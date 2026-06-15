package stages

import (
	"context"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

func TestTagCategoryStage_Name(t *testing.T) {
	s := &TagCategoryStage{}
	if got := s.Name(); got != "tag_category" {
		t.Errorf("Name() = %q, want %q", got, "tag_category")
	}
}

func TestTagCategoryStage_IsCritical(t *testing.T) {
	s := &TagCategoryStage{}
	if s.IsCritical() {
		t.Error("IsCritical() = true, want false")
	}
}

func TestTagCategoryStage_Process_Tops(t *testing.T) {
	payload := []byte(`{"product_type": "tops"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &TagCategoryStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["category_path"], "Apparel>Women>Tops"; got != want {
		t.Errorf("category_path = %v, want %v", got, want)
	}
}

func TestTagCategoryStage_Process_Dress(t *testing.T) {
	payload := []byte(`{"product_type": "dress"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &TagCategoryStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["category_path"], "Apparel>Women>Dresses"; got != want {
		t.Errorf("category_path = %v, want %v", got, want)
	}
}

func TestTagCategoryStage_Process_CaseInsensitive(t *testing.T) {
	payload := []byte(`{"product_type": "T-SHIRT"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &TagCategoryStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["category_path"], "Apparel>Unisex>Tops>T-Shirts"; got != want {
		t.Errorf("category_path = %v, want %v", got, want)
	}
}

func TestTagCategoryStage_Process_UnknownType(t *testing.T) {
	payload := []byte(`{"product_type": "gadget"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &TagCategoryStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["category_path"], "Other>gadget"; got != want {
		t.Errorf("category_path = %v, want %v", got, want)
	}
}

func TestTagCategoryStage_Process_NoProductType(t *testing.T) {
	payload := []byte(`{"title": "Some product"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &TagCategoryStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for missing product_type")
	}
}

func TestTagCategoryStage_Process_InvalidJSON(t *testing.T) {
	payload := []byte(`{bad`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &TagCategoryStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for invalid JSON")
	}
}
