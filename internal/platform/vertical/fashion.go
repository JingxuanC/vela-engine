package vertical

import (
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/model"
)

func init() {
	Register(&Vertical{
		Name:     "fashion",
		Label:    "Fashion & Apparel",
		Features: []string{"tryon", "style"},
	})
}

// fashionTypes are product type keywords that indicate a fashion/apparel shop.
var fashionTypes = []string{
	"clothing", "apparel", "dress", "shirt", "t-shirt", "top", "blouse",
	"pants", "jeans", "denim", "shorts", "skirt", "sweater", "hoodie",
	"jacket", "coat", "outerwear", "activewear", "sportswear", "swimwear",
	"underwear", "lingerie", "shoes", "sneakers", "boots", "heels", "sandals",
	"accessories", "jewelry", "necklace", "bracelet", "ring", "earrings",
	"bag", "handbag", "purse", "backpack", "belt", "scarf", "hat", "cap",
	"socks", "hosiery", "fashion", "suit", "blazer", "jumpsuit", "romper",
	"kimono", "kaftan", "tunic", "leggings", "yoga",
}

// DetectFashion checks if a shop primarily sells fashion/apparel products.
// Returns true if >40% of active products have fashion-related types.
func DetectFashion(db *gorm.DB, shopID uuid.UUID) bool {
	if db == nil {
		return false
	}

	var total int64
	db.Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Count(&total)
	if total == 0 {
		return false
	}

	var fashionCount int64
	db.Model(&model.SyncedProduct{}).
		Where("shop_id = ? AND status = ?", shopID, "active").
		Where("LOWER(product_type) IN (?)", fashionTypes).
		Count(&fashionCount)

	if fashionCount == 0 {
		for _, kw := range fashionTypes {
			var c int64
			db.Model(&model.SyncedProduct{}).
				Where("shop_id = ? AND status = ?", shopID, "active").
				Where("LOWER(product_type) LIKE ?", "%"+kw+"%").
				Count(&c)
			fashionCount += c
			if float64(fashionCount)/float64(total) > 0.4 {
				break
			}
		}
	}

	return float64(fashionCount)/float64(total) > 0.4
}

// DetectAndSet detects the vertical and writes it to Shop.Vertical if not already set.
func DetectAndSet(db *gorm.DB, shopID uuid.UUID) {
	if db == nil {
		return
	}
	var shop model.Shop
	if err := db.Where("id = ?", shopID).First(&shop).Error; err != nil {
		return
	}
	if shop.Vertical != "" {
		return // already set, skip
	}
	if DetectFashion(db, shopID) {
		_ = SetVertical(db, shopID, "fashion")
	}
}
