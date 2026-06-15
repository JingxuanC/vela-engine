// Package vertical provides industry vertical detection and feature gating.
// Shops self-select a vertical (e.g., "fashion"), which unlocks curated AI features
// like virtual try-on for fashion or 3D preview for furniture.
package vertical

import (
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

// Vertical represents an industry vertical with its features.
type Vertical struct {
	Name     string   // "fashion", "furniture", "wine"
	Label    string   // "Fashion & Apparel"
	Features []string // feature keys unlocked by this vertical
}

// registry holds all registered verticals.
var registry = map[string]*Vertical{}

// Register adds a vertical to the registry.
func Register(v *Vertical) {
	registry[v.Name] = v
}

// All returns all registered verticals.
func All() []*Vertical {
	out := make([]*Vertical, 0, len(registry))
	for _, v := range registry {
		out = append(out, v)
	}
	return out
}

// ForShop returns the vertical for a shop. Reads from Shop.Vertical field.
// Returns nil if the shop hasn't selected a vertical or is general-purpose.
func ForShop(db *gorm.DB, shopID uuid.UUID) *Vertical {
	if db == nil {
		return nil
	}
	var shop model.Shop
	if err := db.Where("id = ?", shopID).First(&shop).Error; err != nil {
		return nil
	}
	if shop.Vertical == "" {
		return nil
	}
	return registry[shop.Vertical]
}

// HasFeature checks whether a shop has access to a given feature key.
func HasFeature(db *gorm.DB, shopID uuid.UUID, feature string) bool {
	v := ForShop(db, shopID)
	if v == nil {
		return false
	}
	for _, f := range v.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// SetVertical updates the shop's vertical selection.
func SetVertical(db *gorm.DB, shopID uuid.UUID, name string) error {
	if name != "" && registry[name] == nil {
		return nil // unknown vertical, ignore
	}
	return db.Model(&model.Shop{}).Where("id = ?", shopID).Update("vertical", name).Error
}
