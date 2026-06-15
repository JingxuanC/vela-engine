package stages

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/platform/datapipeline"
)

// tagSynonyms maps common variations of product type names to their canonical
// form for categoryTree lookup. This handles singular/plural mismatches
// ("sneaker" → "sneakers"), possessive forms ("women's dress" → "dress"),
// and common whitespace/alternate spellings.
var tagSynonyms = map[string]string{
	"dresses":           "dress",
	"sneaker":           "sneakers",
	"shirts":            "shirt",
	"mens shirt":        "shirt",
	"men's shirt":       "shirt",
	"women's dress":     "dress",
	"womens dress":      "dress",
	"jackets":           "jacket",
	"coats":             "coat",
	"boots":             "boots",
	"hats":              "hat",
	"belts":             "belt",
	"bags":              "bag",
	"wallets":           "wallet",
	"rings":             "ring",
	"watches":           "watch",
	"gloves":            "gloves",
	"heels":             "heels",
	"flats":             "flats",
	"loafers":           "loafers",
	"skirts":            "skirt",
	"bras":              "bra",
	"sweaters":          "sweater",
	"hoodies":           "hoodie",
	"blouses":           "blouse",
	"sandals":           "sandals",
	"slippers":          "slippers",
	"socks":             "socks",
	"jeans":             "jeans",
	"shorts":            "shorts",
	"trousers":          "trousers",
	"leggings":          "leggings",
	"joggers":           "joggers",
	"pajamas":           "pajamas",
	"earrings":          "earrings",
	"bracelets":         "bracelet",
	"necklaces":         "necklace",
	"sunglasses":        "sunglasses",
	"scarves":           "scarf",
	"cardigans":         "cardigan",
	"blazers":           "blazer",
	"vests":             "vest",
	"overalls":          "overalls",
	"jumpsuits":         "jumpsuit",
	"rompers":           "romper",
	"bikinis":           "bikini",
	"swim trunks":       "swim trunks",
}

// lookupCategoryKey tries to find a canonical category key from a raw product
// type string. It attempts an exact match first, then checks the synonym map
// for common variations.
func lookupCategoryKey(raw string) (string, bool) {
	if path, ok := categoryTree[raw]; ok {
		return path, true
	}
	if canonical, ok := tagSynonyms[raw]; ok {
		if path, ok := categoryTree[canonical]; ok {
			return path, true
		}
	}
	return "", false
}
// The format is "Department>Subcategory>Type" for use in analytics and
// merchandising.
var categoryTree = map[string]string{
	// Tops & shirts
	"tops":       "Apparel>Women>Tops",
	"top":        "Apparel>Women>Tops",
	"blouse":     "Apparel>Women>Tops>Blouses",
	"shirt":      "Apparel>Men>Tops>Shirts",
	"t-shirt":    "Apparel>Unisex>Tops>T-Shirts",
	"tshirt":     "Apparel>Unisex>Tops>T-Shirts",
	"tank top":   "Apparel>Women>Tops>Tank Tops",
	"bodysuit":   "Apparel>Women>Tops>Bodysuits",
	"sweater":    "Apparel>Unisex>Outerwear>Sweaters",
	"hoodie":     "Apparel>Unisex>Outerwear>Hoodies",
	"sweatshirt": "Apparel>Unisex>Outerwear>Sweatshirts",
	"cardigan":   "Apparel>Women>Outerwear>Cardigans",
	"jacket":     "Apparel>Unisex>Outerwear>Jackets",
	"coat":       "Apparel>Unisex>Outerwear>Coats",
	"blazer":     "Apparel>Men>Suits>Blazers",
	"vest":       "Apparel>Unisex>Outerwear>Vests",

	// Bottoms
	"pants":       "Apparel>Women>Bottoms>Pants",
	"trousers":    "Apparel>Men>Bottoms>Trousers",
	"jeans":       "Apparel>Unisex>Bottoms>Jeans",
	"shorts":      "Apparel>Unisex>Bottoms>Shorts",
	"skirt":       "Apparel>Women>Bottoms>Skirts",
	"leggings":    "Apparel>Women>Bottoms>Leggings",
	"joggers":     "Apparel>Men>Bottoms>Joggers",
	"cargo pants": "Apparel>Men>Bottoms>Cargo Pants",

	// Dresses & jumpsuits
	"dress":    "Apparel>Women>Dresses",
	"jumpsuit": "Apparel>Women>Jumpsuits",
	"romper":   "Apparel>Women>Rompers",
	"overalls": "Apparel>Unisex>Overalls",

	// Footwear
	"shoes":    "Footwear>Shoes",
	"sneakers": "Footwear>Athletic>Sneakers",
	"boots":    "Footwear>Boots",
	"sandals":  "Footwear>Sandals",
	"slippers": "Footwear>Slippers",
	"heels":    "Footwear>Women>Heels",
	"flats":    "Footwear>Women>Flats",
	"loafers":  "Footwear>Men>Loafers",

	// Accessories
	"accessories": "Accessories",
	"bag":         "Accessories>Bags",
	"backpack":    "Accessories>Bags>Backpacks",
	"wallet":      "Accessories>Wallets",
	"belt":        "Accessories>Belts",
	"hat":         "Accessories>Headwear>Hats",
	"cap":         "Accessories>Headwear>Caps",
	"scarf":       "Accessories>Scarves",
	"gloves":      "Accessories>Gloves",
	"sunglasses":  "Accessories>Eyewear>Sunglasses",
	"watch":       "Accessories>Watches",
	"jewelry":     "Accessories>Jewelry",
	"necklace":    "Accessories>Jewelry>Necklaces",
	"bracelet":    "Accessories>Jewelry>Bracelets",
	"earrings":    "Accessories>Jewelry>Earrings",
	"ring":        "Accessories>Jewelry>Rings",

	// Intimates
	"underwear":   "Apparel>Intimates>Underwear",
	"bra":         "Apparel>Intimates>Bras",
	"lingerie":    "Apparel>Intimates>Lingerie",
	"socks":       "Apparel>Intimates>Socks",
	"pajamas":     "Apparel>Sleepwear>Pajamas",
	"robe":        "Apparel>Sleepwear>Robes",
	"swimsuit":    "Apparel>Swimwear",
	"bikini":      "Apparel>Swimwear>Bikinis",
	"swim trunks": "Apparel>Swimwear>Swim Trunks",
}

// TagCategoryStage maps a product_type value to a hierarchical category path
// and stores it in data.Fields["category_path"].
//
// Expected payload field: "product_type" (string)
type TagCategoryStage struct{}

func init() {
	datapipeline.RegisterStage("tag_category",
		"Maps product type to a hierarchical category tree path",
		func() datapipeline.Stage { return &TagCategoryStage{} },
	)
}

func (s *TagCategoryStage) Name() string { return "tag_category" }

func (s *TagCategoryStage) IsCritical() bool { return false }

func (s *TagCategoryStage) Process(ctx context.Context, data *datapipeline.CleanData) {
	var rawMap map[string]any
	if err := json.Unmarshal(data.Raw.Payload, &rawMap); err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("tag_category: unmarshal payload: %v", err))
		return
	}

	productType, _ := rawMap["product_type"].(string)
	if productType == "" {
		data.Errors = append(data.Errors, "tag_category: no product_type in payload")
		return
	}

	key := strings.ToLower(strings.TrimSpace(productType))
	path, ok := lookupCategoryKey(key)
	if !ok {
		// Fallback: use a generic path derived from the product type.
		data.Fields["category_path"] = fmt.Sprintf("Other>%s", productType)
		data.Fields["product_type_raw"] = productType
		return
	}

	data.Fields["category_path"] = path
	data.Fields["product_type_raw"] = productType
}
