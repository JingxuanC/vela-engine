package stages

import (
	"context"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

func TestNormalizeCurrencyStage_Name(t *testing.T) {
	s := &NormalizeCurrencyStage{}
	if got := s.Name(); got != "normalize_currency" {
		t.Errorf("Name() = %q, want %q", got, "normalize_currency")
	}
}

func TestNormalizeCurrencyStage_IsCritical(t *testing.T) {
	s := &NormalizeCurrencyStage{}
	if s.IsCritical() {
		t.Error("IsCritical() = true, want false")
	}
}

func TestNormalizeCurrencyStage_Process_TopLevelPrice(t *testing.T) {
	payload := []byte(`{"price": 29.99, "currency": "EUR"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	if got, want := data.Fields["currency_normalized"], "USD"; got != want {
		t.Errorf("currency_normalized = %v, want %v", got, want)
	}
	// 29.99 EUR * 1.08 ≈ 32.39
	expectedUSD := 29.99 * 1.08
	gotUSD, _ := data.Fields["price_usd"].(float64)
	if gotUSD < expectedUSD-0.01 || gotUSD > expectedUSD+0.01 {
		t.Errorf("price_usd = %v, want ~%v", gotUSD, expectedUSD)
	}
}

func TestNormalizeCurrencyStage_Process_VariantPrice(t *testing.T) {
	payload := []byte(`{"variants": [{"price": 49.99}], "currency": "GBP"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	// 49.99 GBP * 1.26 ≈ 62.99
	expectedUSD := 49.99 * 1.26
	gotUSD, _ := data.Fields["price_usd"].(float64)
	if gotUSD < expectedUSD-0.01 || gotUSD > expectedUSD+0.01 {
		t.Errorf("price_usd = %v, want ~%v", gotUSD, expectedUSD)
	}
}

func TestNormalizeCurrencyStage_Process_DefaultCurrency(t *testing.T) {
	payload := []byte(`{"price": 15.00}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	gotUSD, _ := data.Fields["price_usd"].(float64)
	if gotUSD != 15.00 {
		t.Errorf("price_usd = %v, want 15.00", gotUSD)
	}
}

func TestNormalizeCurrencyStage_Process_UnknownCurrency(t *testing.T) {
	payload := []byte(`{"price": 100, "currency": "XYZ"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for unknown currency")
	}
}

func TestNormalizeCurrencyStage_Process_NoPrice(t *testing.T) {
	payload := []byte(`{"currency": "USD"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error when no price found")
	}
}

func TestNormalizeCurrencyStage_Process_InvalidJSON(t *testing.T) {
	payload := []byte(`{invalid}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) == 0 {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNormalizeCurrencyStage_Process_StringPrice(t *testing.T) {
	payload := []byte(`{"price": "39.99", "currency": "EUR"}`)
	raw := &datapipeline.RawData{Source: "shopify", Payload: payload}
	data := &datapipeline.CleanData{Raw: *raw, Fields: make(map[string]any)}

	s := &NormalizeCurrencyStage{}
	s.Process(context.Background(), data)

	if len(data.Errors) > 0 {
		t.Fatalf("unexpected errors: %v", data.Errors)
	}
	// 39.99 EUR * 1.08 ≈ 43.19
	expectedUSD := 39.99 * 1.08
	gotUSD, _ := data.Fields["price_usd"].(float64)
	if gotUSD < expectedUSD-0.01 || gotUSD > expectedUSD+0.01 {
		t.Errorf("price_usd = %v, want ~%v", gotUSD, expectedUSD)
	}
}
