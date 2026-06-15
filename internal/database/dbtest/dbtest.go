package dbtest

import (
	"github.com/JingxuanC/vela-engine/internal/model"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func NewInMemoryDB() (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&model.Shop{}, &model.SyncedProduct{}, &model.SyncedOrder{}, &model.SyncedCustomer{}, &model.TokenUsage{}); err != nil {
		return nil, err
	}
	return db, nil
}
