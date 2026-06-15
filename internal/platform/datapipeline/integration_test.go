package datapipeline_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
	_ "github.com/JingxuanC/vela-engine/internal/platform/datapipeline/stages"
)

// TestPipelineIntegration_RunWithAllStages runs a full pipeline with all real
// stage implementations.
func TestPipelineIntegration_RunWithAllStages(t *testing.T) {
	tests := []struct {
		name    string
		raw     *datapipeline.RawData
		wantErr bool
		checkFn func(t *testing.T, data *datapipeline.CleanData)
	}{
		{
			name: "complete_product",
			raw: &datapipeline.RawData{
				Source: "shopify",
				Payload: json.RawMessage(`{
					"title": "100% Cotton Classic Tee",
					"body_html": "Made from 100% Cotton",
					"product_type": "tops",
					"price": 29.99,
					"currency": "EUR",
					"size": "M",
					"shipping_address": {"country": "US"}
				}`),
			},
			checkFn: func(t *testing.T, data *datapipeline.CleanData) {
				if data.Fields["currency_normalized"] != "USD" {
					t.Errorf("currency_normalized = %v", data.Fields["currency_normalized"])
				}
				if data.Fields["region"] != "NA" {
					t.Errorf("region = %v", data.Fields["region"])
				}
				if data.Fields["size_normalized"] != "Medium" {
					t.Errorf("size_normalized = %v", data.Fields["size_normalized"])
				}
				if data.Fields["category_path"] != "Apparel>Women>Tops" {
					t.Errorf("category_path = %v", data.Fields["category_path"])
				}
				materials, ok := data.Fields["materials"].([]map[string]any)
				if !ok || len(materials) == 0 {
					t.Errorf("materials = %v (type %T)", data.Fields["materials"], data.Fields["materials"])
				}
			},
		},
		{
			name: "non_critical_errors_collected",
			raw: &datapipeline.RawData{
				Source: "shopify",
				Payload: json.RawMessage(`{
					"title": "Product without details",
					"product_type": "electronics"
				}`),
			},
			checkFn: func(t *testing.T, data *datapipeline.CleanData) {
				// We expect errors from stages that couldn't find required fields:
				// - normalize_currency: no price
				// - classify_region: no country
				// - normalize_size: no size
				// - extract_material: no body_html/title with material info
				if len(data.Errors) == 0 {
					t.Error("expected some non-critical errors but got none")
				}
				// category_path should still be populated as a fallback.
				if data.Fields["category_path"] != "Other>electronics" {
					t.Errorf("category_path = %v, want Other>electronics", data.Fields["category_path"])
				}
			},
		},
		{
			name: "invalid_json",
			raw: &datapipeline.RawData{
				Source:  "shopify",
				Payload: json.RawMessage(`{invalid`),
			},
			checkFn: func(t *testing.T, data *datapipeline.CleanData) {
				// All stages should log errors on invalid JSON.
				if len(data.Errors) == 0 {
					t.Error("expected errors for invalid JSON")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Build pipeline from registered stages.
			stages := buildPipelineStages(t)
			p := datapipeline.NewPipeline(stages...)

			data, err := p.Run(context.Background(), tt.raw)
			if tt.wantErr && err == nil {
				t.Fatal("expected error but got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.checkFn != nil {
				tt.checkFn(t, data)
			}
		})
	}
}

func TestPipelineIntegration_EmptyPipeline(t *testing.T) {
	p := datapipeline.NewPipeline()
	_, err := p.Run(context.Background(), &datapipeline.RawData{
		Source: "test", Payload: json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("expected error for empty pipeline")
	}
}

func TestPipelineIntegration_NilRaw(t *testing.T) {
	p := datapipeline.NewPipeline()
	_, err := p.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil raw")
	}
}

// buildPipelineStages constructs a pipeline with all registered stages.
func buildPipelineStages(t *testing.T) []datapipeline.Stage {
	stageNames := []string{
		"normalize_currency",
		"classify_region",
		"normalize_size",
		"tag_category",
		"extract_material",
	}
	var stages []datapipeline.Stage
	for _, name := range stageNames {
		s, ok := datapipeline.GetStage(name)
		if !ok {
			t.Fatalf("registered stage %q not found", name)
		}
		stages = append(stages, s)
	}
	return stages
}
