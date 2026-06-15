package tryon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildTryonPrompt_MatchesPreset(t *testing.T) {
	tests := []struct {
		name        string
		garmentType string
		style       string
		gender      string
		resolution  int
		wantModel   string
		wantClothes string
	}{
		{"dress formal female", "dress", "formal", "female", 0, "aitryon-plus", "dress"},
		{"top casual male", "upper", "casual", "male", 0, "aitryon-plus", "upper"},
		{"jacket formal unisex", "jacket", "business", "unisex", 0, "aitryon-plus", "upper"},
		{"bottom sporty", "lower", "sporty", "female", 0, "aitryon-plus", "lower"},
		{"custom resolution", "dress", "casual", "unisex", 512, "aitryon-plus", "dress"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := BuildTryonPrompt(tt.garmentType, tt.style, tt.gender, tt.resolution)
			assert.Equal(t, tt.wantModel, params.Model)
			assert.Equal(t, tt.wantClothes, params.ClothesType)
			assert.True(t, params.RestoreFace)
		})
	}
}

func TestBuildTryonPrompt_UnknownFallback(t *testing.T) {
	params := BuildTryonPrompt("unknown_type", "unknown_style", "unisex", 0)
	assert.Equal(t, "aitryon-plus", params.Model)
	assert.NotEmpty(t, params.PromptPositive)
}

func TestBuildTryonPrompt_GenderOverride(t *testing.T) {
	male := BuildTryonPrompt("upper", "casual", "male", 0)
	female := BuildTryonPrompt("upper", "casual", "female", 0)

	// Male and female should have different prompts due to gender override
	assert.Contains(t, male.PromptPositive, "masculine")
	assert.Contains(t, female.PromptPositive, "feminine")

	// Refiner strength should differ
	assert.Equal(t, 0.4, male.RefinerStrength)
	assert.Equal(t, 0.55, female.RefinerStrength)
}

func TestBuildTryonPrompt_ResolutionOverride(t *testing.T) {
	params := BuildTryonPrompt("dress", "formal", "unisex", 512)
	assert.Equal(t, 512, params.Resolution)
}

func TestMapGarmentType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"upper", "top"},
		{"jacket", "outerwear"},
		{"coat", "outerwear"},
		{"dress", "dress"},
		{"lower", "bottom"},
		{"jumpsuit", "full_body"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapGarmentType(tt.input))
		})
	}
}

func TestMapStyle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"casual", "casual"},
		{"formal", "formal"},
		{"business", "formal"},
		{"sports", "sporty"},
		{"evening", "formal"},
		{"party", "elegant"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapStyle(tt.input))
		})
	}
}

func TestMapClothesType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"upper", "upper"},
		{"top", "upper"},
		{"jacket", "upper"},
		{"lower", "lower"},
		{"bottom", "lower"},
		{"dress", "dress"},
		{"jumpsuit", "dress"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			assert.Equal(t, tt.expected, mapClothesType(tt.input))
		})
	}
}

func TestEstimateCost(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		refiner  bool
		quantity int
		wantCost float64
	}{
		{"tryon plus with refiner x1", "aitryon-plus", true, 1, 0.804},
		{"tryon plus no refiner x1", "aitryon-plus", false, 1, 0.504},
		{"tryon fast x1", "aitryon", false, 1, 0.204},
		{"tryon plus x5", "aitryon-plus", true, 5, 4.02},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := TryonParams{Model: tt.model, RefinerPass: tt.refiner}
			cost := EstimateCost(params, tt.quantity)
			assert.Equal(t, tt.wantCost, cost)
		})
	}
}

func TestListAvailablePresets(t *testing.T) {
	presets := ListAvailablePresets()
	assert.GreaterOrEqual(t, len(presets), 5)
	assert.Contains(t, presets, "dress_formal")
	assert.Contains(t, presets, "top_casual")
}

func TestGetPreset(t *testing.T) {
	p, ok := GetPreset("dress_formal")
	assert.True(t, ok)
	assert.Equal(t, "aitryon-plus", p.Model)
	assert.Equal(t, 2048, p.Resolution)

	_, ok = GetPreset("nonexistent")
	assert.False(t, ok)
}

func TestDefaultPreset(t *testing.T) {
	assert.Equal(t, "aitryon-plus", defaultPreset.Model)
	assert.Equal(t, 1024, defaultPreset.Resolution)
	assert.True(t, defaultPreset.RestoreFace)
}
