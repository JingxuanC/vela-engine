package stages

import (
	"context"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

func TestNormalizeSizeStage_Name(t *testing.T) {
	s := &NormalizeSizeStage{}
	if got := s.Name(); got != "normalize_size" {
		t.Errorf("Name() = %q, want %q", got, "normalize_size")
	}
}

func TestNormalizeSizeStage_IsCritical(t *testing.T) {
	s := &NormalizeSizeStage{}
	if s.IsCritical() {
		t.Error("IsCritical() = true, want false")
	}
}

func TestNormalizeSizeStage_Process_LetterSizes(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"M", "Medium"},
		{"S", "Small"},
		{"L", "Large"},
		{"XL", "Extra Large"},
		{"XXL", "Double Extra Large"},
		{"xs", "Extra Small"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			payload := []byte(`{"size": "` + tt.input + `"}`)
			raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
			data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

			s := &NormalizeSizeStage{}
			s.Process(context.Background(), data)

			if len(data.Errors) > 0 {
				t.Fatalf("unexpected errors: %v", data.Errors)
			}
			if got := data.Fields["size_normalized"]; got != tt.want {
				t.Errorf("size_normalized = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeSizeStage_Process_USShoeSize(t *testing.T) {
	payload := []byte(`{"size": "US 8"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeSizeStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["size_normalized"], "US 8"; got != want {
		t.Errorf("size_normalized = %v, want %v", got, want)
	}
}

func TestNormalizeSizeStage_Process_EUShoeSize(t *testing.T) {
	payload := []byte(`{"size": "EU 38"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeSizeStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["size_normalized"], "US 8"; got != want {
		t.Errorf("size_normalized = %v, want %v", got, want)
	}
}

func TestNormalizeSizeStage_Process_VariantSize(t *testing.T) {
	payload := []byte(`{"variants": [{"size": "L"}]}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeSizeStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["size_normalized"], "Large"; got != want {
		t.Errorf("size_normalized = %v, want %v", got, want)
	}
}

func TestNormalizeSizeStage_Process_VariantTitle(t *testing.T) {
	payload := []byte(`{"variants": [{"title": "M / Black"}]}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeSizeStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	// "M / Black" title is now split on " / "; "M" matches sizeMapping -> "Medium".
	if got := data.Fields["size_normalized"]; got != "Medium" {
		t.Errorf("size_normalized = %v, want %v", got, "Medium")
	}
}

func TestNormalizeSizeStage_Process_NoSize(t *testing.T) {
	payload := []byte(`{"product_type": "shoes"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeSizeStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for missing size")
	}
}

func TestNormalizeSizeStage_Process_InvalidJSON(t *testing.T) {
	payload := []byte(`{bad`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeSizeStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for invalid JSON")
	}
}
