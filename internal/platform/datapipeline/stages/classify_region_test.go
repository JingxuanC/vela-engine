package stages

import (
	"context"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

func TestClassifyRegionStage_Name(t *testing.T) {
	s := &ClassifyRegionStage{}
	if got := s.Name(); got != "classify_region" {
		t.Errorf("Name() = %q, want %q", got, "classify_region")
	}
}

func TestClassifyRegionStage_IsCritical(t *testing.T) {
	s := &ClassifyRegionStage{}
	if s.IsCritical() {
		t.Error("IsCritical() = true, want false")
	}
}

func TestClassifyRegionStage_Process_ShippingAddress(t *testing.T) {
	payload := []byte(`{"shipping_address": {"country": "US"}}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["region"], "NA"; got != want {
		t.Errorf("region = %v, want %v", got, want)
	}
	if got, want := data.Fields["country"], "US"; got != want {
		t.Errorf("country = %v, want %v", got, want)
	}
}

func TestClassifyRegionStage_Process_CountryCodeField(t *testing.T) {
	payload := []byte(`{"shipping_address": {"country_code": "de"}}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["region"], "EU"; got != want {
		t.Errorf("region = %v, want %v", got, want)
	}
}

func TestClassifyRegionStage_Process_TopLevelCountry(t *testing.T) {
	payload := []byte(`{"country": "JP"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	// JP is now in APAC region.
	if got, want := data.Fields["region"], "APAC"; got != want {
		t.Errorf("region = %v, want %v", got, want)
	}
}

func TestClassifyRegionStage_Process_SEA(t *testing.T) {
	payload := []byte(`{"shipping_address": {"country": "SG"}}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["region"], "SEA"; got != want {
		t.Errorf("region = %v, want %v", got, want)
	}
}

func TestClassifyRegionStage_Process_ME(t *testing.T) {
	payload := []byte(`{"shipping_address": {"country": "AE"}}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["region"], "ME"; got != want {
		t.Errorf("region = %v, want %v", got, want)
	}
}

func TestClassifyRegionStage_Process_LATAM(t *testing.T) {
	payload := []byte(`{"shipping_address": {"country": "BR"}}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["region"], "LATAM"; got != want {
		t.Errorf("region = %v, want %v", got, want)
	}
}

func TestClassifyRegionStage_Process_NoCountry(t *testing.T) {
	payload := []byte(`{"product_type": "shoes"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for missing country")
	}
}

func TestClassifyRegionStage_Process_InvalidJSON(t *testing.T) {
	payload := []byte(`{bad`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &ClassifyRegionStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for invalid JSON")
	}
}
