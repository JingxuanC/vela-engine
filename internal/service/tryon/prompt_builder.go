package tryon

import (
	"encoding/json"
	"fmt"
	"strings"
)

// TryonParams holds all parameters for a virtual try-on API call.
type TryonParams struct {
	Model           string  `json:"model"`
	Resolution      int     `json:"resolution"`
	RestoreFace     bool    `json:"restore_face"`
	RefinerPass     bool    `json:"refiner_pass"`
	RefinerStrength float64 `json:"refiner_strength"`
	ClothesType     string  `json:"clothes_type"`
	PromptPositive  string  `json:"prompt_positive"`
	PromptNegative  string  `json:"prompt_negative"`
	EdgeBlendRadius int     `json:"edge_blend_radius"`
	DenoiseStrength float64 `json:"denoise_strength"`
	MinFaceIQA      float64 `json:"min_face_iqa"`
}

// BuildTryonPrompt constructs optimal DashScope API parameters based on
// garment type, style, and gender. Falls back to sensible defaults when
// no specific preset matches.
func BuildTryonPrompt(garmentType, style, gender string, resolution int) TryonParams {
	cat := mapGarmentType(garmentType)
	styleKey := mapStyle(style)

	// Try to find exact {cat}_{style} preset
	presetKey := cat + "_" + styleKey
	preset, ok := presets[presetKey]
	if !ok {
		// Fall back to any preset for this category
		for key, p := range presets {
			if strings.HasPrefix(key, cat+"_") {
				preset = p
				break
			}
		}
	}
	if preset.Model == "" {
		preset = defaultPreset
	}

	// Apply gender overrides
	override, hasOverride := genderOverrides[strings.ToLower(gender)]
	if hasOverride {
		if override.PromptPositive != "" {
			preset.PromptPositive = strings.TrimRight(preset.PromptPositive, ".") + ". " + override.PromptPositive + "."
		}
		if override.PromptNegative != "" {
			preset.PromptNegative = strings.TrimRight(preset.PromptNegative, ".") + ". " + override.PromptNegative + "."
		}
		if override.RefinerStrength > 0 {
			preset.RefinerStrength = override.RefinerStrength
		}
		if override.DenoiseStrength > 0 {
			preset.DenoiseStrength = override.DenoiseStrength
		}
	}

	// Override resolution if specified
	if resolution > 0 {
		preset.Resolution = resolution
	}

	// Set clothes_type based on garment type
	preset.ClothesType = mapClothesType(garmentType)

	return preset
}

// EstimateCost calculates the estimated CNY cost for a try-on request.
// Costs: aitryon=0.20, aitryon-plus=0.50, parsing=0.004, refiner=0.30 per image.
func EstimateCost(params TryonParams, quantity int) float64 {
	perImage := 0.0
	switch params.Model {
	case "aitryon":
		perImage = 0.20
	case "aitryon-plus":
		perImage = 0.50
	default:
		perImage = 0.50
	}
	if params.RefinerPass {
		perImage += 0.30
	}
	perImage += 0.004 // parsing cost
	return roundTo(perImage*float64(quantity), 4)
}

// ListAvailablePresets returns all available {category}_{style} preset keys.
func ListAvailablePresets() []string {
	keys := make([]string, 0, len(presets))
	for k := range presets {
		keys = append(keys, k)
	}
	return keys
}

// GetPreset returns a specific preset by key (e.g., "dress_formal").
func GetPreset(key string) (TryonParams, bool) {
	p, ok := presets[key]
	return p, ok
}

// MarshalJSON serializes the TryonParams to JSON.
func (p TryonParams) MarshalJSON() ([]byte, error) {
	type alias TryonParams
	return json.Marshal(alias(p))
}

// --- Mapping helpers ---

var garmentTypeMap = map[string]string{
	"upper":     "top",
	"lower":     "bottom",
	"bottom":    "bottom",
	"dress":     "dress",
	"top":       "top",
	"outerwear": "outerwear",
	"jacket":    "outerwear",
	"coat":      "outerwear",
	"full_body": "full_body",
	"jumpsuit":  "full_body",
	"bodysuit":  "full_body",
}

func mapGarmentType(gt string) string {
	if mapped, ok := garmentTypeMap[strings.ToLower(gt)]; ok {
		return mapped
	}
	return strings.ToLower(gt)
}

var styleMap = map[string]string{
	"casual":   "casual",
	"formal":   "formal",
	"sporty":   "sporty",
	"sports":   "sporty",
	"athletic": "sporty",
	"elegant":  "elegant",
	"evening":  "formal",
	"business": "formal",
	"party":    "elegant",
	"cocktail": "elegant",
}

func mapStyle(s string) string {
	if mapped, ok := styleMap[strings.ToLower(s)]; ok {
		return mapped
	}
	return strings.ToLower(s)
}

func mapClothesType(gt string) string {
	switch strings.ToLower(gt) {
	case "upper", "top", "outerwear", "jacket", "coat":
		return "upper"
	case "lower", "bottom":
		return "lower"
	case "dress", "full_body", "jumpsuit", "bodysuit":
		return "dress"
	default:
		return "upper"
	}
}

// --- Preset definitions ---

var defaultPreset = TryonParams{
	Model:           "aitryon-plus",
	Resolution:      1024,
	RestoreFace:     true,
	RefinerPass:     true,
	RefinerStrength: 0.5,
	ClothesType:     "upper",
}

type genderOverride struct {
	PromptPositive  string
	PromptNegative  string
	RefinerStrength float64
	DenoiseStrength float64
}

var genderOverrides = map[string]genderOverride{
	"male": {
		PromptPositive:  "masculine build, broad shoulders, athletic frame",
		PromptNegative:  "feminine features, narrow shoulders, exaggerated curves",
		RefinerStrength: 0.4,
		DenoiseStrength: 0.3,
	},
	"female": {
		PromptPositive:  "feminine silhouette, natural curves, graceful posture",
		PromptNegative:  "unnatural proportions, distorted bust, deformed waist",
		RefinerStrength: 0.55,
		DenoiseStrength: 0.4,
	},
	"unisex": {
		PromptPositive:  "natural proportions, balanced silhouette",
		PromptNegative:  "distorted, unnatural, deformed",
		RefinerStrength: 0.5,
		DenoiseStrength: 0.35,
	},
}

var presets = map[string]TryonParams{
	"top_casual": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.4,
		ClothesType:     "upper",
		PromptPositive:  "casual t-shirt, natural folds, relaxed fit, soft fabric texture, realistic shading",
		PromptNegative:  "wrinkled, distorted, deformed, unnatural, floating fabric, mismatched lighting",
		EdgeBlendRadius: 12,
		DenoiseStrength: 0.3,
	},
	"top_formal": {
		Model:           "aitryon-plus",
		Resolution:      2048,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.6,
		ClothesType:     "upper",
		PromptPositive:  "formal button-up shirt, crisp collar, sharp creases, structured fit, professional look, high detail",
		PromptNegative:  "casual, wrinkled, untucked, baggy, distorted collar, mismatched pattern, deformed",
		EdgeBlendRadius: 15,
		DenoiseStrength: 0.5,
	},
	"top_sporty": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.3,
		ClothesType:     "upper",
		PromptPositive:  "athletic fit, moisture-wicking fabric, sporty style, dynamic pose, breathable texture",
		PromptNegative:  "loose fit, formal, wrinkled, unnatural folds, glossy plastic, deformed",
		EdgeBlendRadius: 10,
		DenoiseStrength: 0.2,
	},
	"bottom_casual": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.4,
		ClothesType:     "lower",
		PromptPositive:  "casual pants, natural drape, relaxed fit, denim or cotton texture, realistic legs",
		PromptNegative:  "distorted legs, unnatural bending, shiny, floating fabric, deformed waist",
		EdgeBlendRadius: 18,
		DenoiseStrength: 0.4,
	},
	"bottom_formal": {
		Model:           "aitryon-plus",
		Resolution:      2048,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.6,
		ClothesType:     "lower",
		PromptPositive:  "formal trousers, sharp crease, tailored fit, wool texture, professional, clean lines",
		PromptNegative:  "casual, baggy, wrinkled, distorted legs, uneven hem, shiny fabric",
		EdgeBlendRadius: 20,
		DenoiseStrength: 0.5,
	},
	"bottom_sporty": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.3,
		ClothesType:     "lower",
		PromptPositive:  "athletic shorts, flexible fabric, sporty, compression fit, breathable",
		PromptNegative:  "baggy, formal, wrinkled, distorted legs, plastic texture, deformed",
		EdgeBlendRadius: 12,
		DenoiseStrength: 0.2,
	},
	"dress_casual": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.5,
		ClothesType:     "dress",
		PromptPositive:  "casual dress, natural flow, soft fabric, flattering silhouette, comfortable fit, natural waistline",
		PromptNegative:  "wrinkled, distorted, stiff, unnatural draping, floating hem, deformed body",
		EdgeBlendRadius: 15,
		DenoiseStrength: 0.4,
	},
	"dress_formal": {
		Model:           "aitryon-plus",
		Resolution:      2048,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.7,
		ClothesType:     "dress",
		PromptPositive:  "elegant evening gown, luxurious fabric, graceful drape, flattering silhouette, high fashion, detailed texture, sophisticated, refined",
		PromptNegative:  "casual, wrinkled, baggy, distorted, floating, unnatural shine, deformed silhouette, cheap fabric look",
		EdgeBlendRadius: 20,
		DenoiseStrength: 0.5,
	},
	"dress_elegant": {
		Model:           "aitryon-plus",
		Resolution:      2048,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.65,
		ClothesType:     "dress",
		PromptPositive:  "elegant dress, flowing fabric, sophisticated silhouette, graceful lines, premium texture, refined look",
		PromptNegative:  "casual, wrinkled, stiff, distorted draping, unnatural, cheap, deformed",
		EdgeBlendRadius: 18,
		DenoiseStrength: 0.45,
	},
	"outerwear_casual": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.4,
		ClothesType:     "upper",
		PromptPositive:  "casual jacket, open front, relaxed fit, layered look, fabric texture, natural shoulders",
		PromptNegative:  "tight fit, distorted shoulders, unnatural collar, floating, deformed arms, stiff",
		EdgeBlendRadius: 18,
		DenoiseStrength: 0.4,
	},
	"outerwear_formal": {
		Model:           "aitryon-plus",
		Resolution:      2048,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.7,
		ClothesType:     "upper",
		PromptPositive:  "formal blazer, tailored fit, sharp lapels, structured shoulders, premium fabric, professional, crisp",
		PromptNegative:  "casual, baggy, wrinkled, distorted shoulders, uneven lapels, floating, deformed arms",
		EdgeBlendRadius: 20,
		DenoiseStrength: 0.5,
	},
	"full_body_casual": {
		Model:           "aitryon-plus",
		Resolution:      1024,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.5,
		ClothesType:     "dress",
		PromptPositive:  "jumpsuit, one-piece, natural fit, comfortable fabric, relaxed, modern",
		PromptNegative:  "wrinkled, distorted, unnatural waist, floating fabric, deformed body",
		EdgeBlendRadius: 15,
		DenoiseStrength: 0.4,
	},
	"full_body_formal": {
		Model:           "aitryon-plus",
		Resolution:      2048,
		RestoreFace:     true,
		RefinerPass:     true,
		RefinerStrength: 0.7,
		ClothesType:     "dress",
		PromptPositive:  "formal jumpsuit, elegant one-piece, tailored fit, luxurious fabric, sophisticated, clean lines",
		PromptNegative:  "casual, wrinkled, distorted, baggy, unnatural draping, deformed waistline",
		EdgeBlendRadius: 18,
		DenoiseStrength: 0.5,
	},
}

// --- Helpers ---

func roundTo(val float64, decimals int) float64 {
	pow := 1.0
	for i := 0; i < decimals; i++ {
		pow *= 10
	}
	return float64(int(val*pow+0.5)) / pow
}

func init() {
	// Ensure all presets have a model set
	for k, p := range presets {
		if p.Model == "" {
			p.Model = "aitryon-plus"
			presets[k] = p
		}
	}

	// Log available presets
	_ = fmt.Sprintf("tryon: loaded %d presets", len(presets))
}

// --- Aesthetic Style Presets for image enhancement ---

// StylePreset defines an aesthetic enhancement style for transforming
// basic try-on output into professional fashion photography.
type StylePreset struct {
	Key            string  `json:"key"`
	Name           string  `json:"name"`
	Description    string  `json:"description"`
	PositivePrompt string  `json:"positive_prompt"`
	NegativePrompt string  `json:"negative_prompt"`
	Strength       float64 `json:"strength"`       // img2img strength: 0.3-0.8
	GuidanceScale  float64 `json:"guidance_scale"` // prompt adherence: 5-15
}

var stylePresets = map[string]StylePreset{
	"studio": {
		Key:         "studio",
		Name:        "Studio Photoshoot",
		Description: "Professional studio lighting, clean background, fashion editorial quality",
		PositivePrompt: "professional fashion photography, studio lighting with softbox, clean white background, " +
			"magazine editorial quality, sharp focus on clothing details, natural skin texture, " +
			"professional color grading, 8K resolution, high-end fashion look, elegant pose, " +
			"luxury brand campaign style, impeccable fabric detail, cinematic lighting",
		NegativePrompt: "blurry, low quality, distorted face, bad lighting, messy background, " +
			"amateur photo, overexposed, underexposed, watermark, text, cropped, " +
			"deformed body, extra limbs, ugly, lowres, jpeg artifacts",
		Strength:      0.4,
		GuidanceScale: 7.5,
	},
	"editorial": {
		Key:         "editorial",
		Name:        "Fashion Editorial",
		Description: "Magazine editorial style, dramatic lighting, artistic composition",
		PositivePrompt: "high fashion editorial photography, dramatic rim lighting, artistic composition, " +
			"Vogue magazine style, bold shadows and highlights, avant-garde fashion, " +
			"professional model pose, luxury aesthetic, striking visual impact, " +
			"high contrast, fashion week quality, museum-grade photography",
		NegativePrompt: "casual snapshot, flat lighting, boring composition, amateur, " +
			"overexposed, distorted, deformed, watermark, low quality, ugly",
		Strength:      0.45,
		GuidanceScale: 8.0,
	},
	"natural": {
		Key:         "natural",
		Name:        "Natural Outdoor",
		Description: "Golden hour sunlight, outdoor natural setting, lifestyle photography",
		PositivePrompt: "golden hour natural lighting, outdoor lifestyle photography, soft bokeh background, " +
			"sun-kissed glow, natural pose, candid fashion shot, beautiful depth of field, " +
			"warm color palette, organic feel, premium lifestyle brand aesthetic, " +
			"nature-inspired, relaxed elegance, sunlit fabric texture",
		NegativePrompt: "studio lighting, artificial background, dark, gloomy, overcast, " +
			"harsh shadows, distorted, deformed, watermark, low quality, ugly",
		Strength:      0.35,
		GuidanceScale: 6.5,
	},
	"street": {
		Key:         "street",
		Name:        "Street Style",
		Description: "Urban street photography, cool city backdrop, modern casual vibe",
		PositivePrompt: "street style fashion photography, urban city background, modern architecture, " +
			"cool casual aesthetic, natural street lighting, candid urban shot, " +
			"trendy outfit showcase, concrete and glass backdrop, influencer style, " +
			"vibrant city atmosphere, contemporary fashion, edgy yet polished",
		NegativePrompt: "studio lighting, rural, nature background, formal pose, " +
			"distorted, deformed, watermark, low quality, blurry, ugly",
		Strength:      0.4,
		GuidanceScale: 7.0,
	},
	"vintage": {
		Key:         "vintage",
		Name:        "Vintage Film",
		Description: "Film camera aesthetic, warm tones, nostalgic grain, retro feel",
		PositivePrompt: "vintage film photography, 35mm film grain, warm nostalgic tones, " +
			"retro fashion aesthetic, soft focus edges, analog camera look, " +
			"Kodak Portra color palette, timeless elegance, classic fashion, " +
			"film light leaks, dreamy atmosphere, 1970s editorial style",
		NegativePrompt: "digital sharpness, modern, HDR, ultra HD, sterile, plastic look, " +
			"distorted, deformed, watermark, ugly, oversaturated",
		Strength:      0.5,
		GuidanceScale: 6.0,
	},
	"minimal": {
		Key:         "minimal",
		Name:        "Minimalist Clean",
		Description: "Scandinavian minimalism, clean lines, soft neutral tones",
		PositivePrompt: "minimalist fashion photography, clean geometric composition, soft neutral tones, " +
			"Scandinavian design aesthetic, negative space, architectural simplicity, " +
			"gentle shadows, pure and refined, premium minimal brand look, " +
			"soft diffused lighting, elegant restraint, high-end simplicity",
		NegativePrompt: "cluttered, busy, colorful, chaotic, messy background, " +
			"distorted, deformed, watermark, low quality, ugly",
		Strength:      0.35,
		GuidanceScale: 7.0,
	},
	"luxe": {
		Key:         "luxe",
		Name:        "Luxury Campaign",
		Description: "Ultra-premium luxury brand look, rich colors, dramatic elegance",
		PositivePrompt: "ultra-luxury fashion campaign, rich jewel tones, dramatic elegance, " +
			"high-end designer brand aesthetic, gold accents, marble backdrop, " +
			"premium fabric texture visible, sophisticated lighting, Haute Couture quality, " +
			"exclusive boutique atmosphere, aspirational lifestyle, impeccable detail",
		NegativePrompt: "cheap, budget, casual, messy, cluttered, distorted, " +
			"deformed, watermark, low quality, ugly, plastic-looking",
		Strength:      0.5,
		GuidanceScale: 8.5,
	},
}

// GetStylePreset returns a style preset by key, falling back to studio.
func GetStylePreset(key string) StylePreset {
	if p, ok := stylePresets[key]; ok {
		return p
	}
	return stylePresets["studio"]
}

// ListStylePresets returns all available style preset keys and names.
func ListStylePresets() []StylePreset {
	keys := make([]StylePreset, 0, len(stylePresets))
	for _, p := range stylePresets {
		keys = append(keys, StylePreset{
			Key:         p.Key,
			Name:        p.Name,
			Description: p.Description,
		})
	}
	return keys
}
