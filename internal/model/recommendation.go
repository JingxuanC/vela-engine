package model

import (
	"time"

	"github.com/google/uuid"
)

// ProductAffinity stores precomputed product relationship scores for
// recommendation strategies (FBT = Frequently Bought Together, trending).
//
// For FBT strategy: product_id_a and product_id_b are the paired products.
// For trending strategy: product_id_a is the product and product_id_b is 0.
type ProductAffinity struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ProductID_A  int64     `gorm:"column:product_id_a;not null;uniqueIndex:idx_affinities_key"`
	ProductID_B  int64     `gorm:"column:product_id_b;not null;uniqueIndex:idx_affinities_key"`
	ShopID       uuid.UUID `gorm:"column:shop_id;index;not null;uniqueIndex:idx_affinities_key"`
	Score        float64   `gorm:"column:score;not null"`
	CoCount      int       `gorm:"column:co_count;not null"`
	Strategy     string    `gorm:"column:strategy;not null;uniqueIndex:idx_affinities_key"`
	ProductTitle string    `gorm:"column:product_title;default:''"`
	ProductImage string    `gorm:"column:product_image;default:''"`
	ProductPrice string    `gorm:"column:product_price;default:''"`
	ComputedAt   time.Time `gorm:"column:computed_at;autoCreateTime"`
}

// TableName overrides the table name for ProductAffinity.
func (ProductAffinity) TableName() string {
	return "product_affinities"
}
