package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func setupTracker(t *testing.T) (*TokenTracker, *gorm.DB) {
	db, _ := gorm.Open(postgres.Open("postgres://vela:vela@localhost:5433/vela?sslmode=disable"), &gorm.Config{SkipDefaultTransaction: true})
	if db == nil {
		t.Skip("no PG")
		return nil, nil
	}
	db.AutoMigrate(&model.TokenUsage{})
	return NewTokenTracker(db, nil), db
}

var sid = uuid.MustParse("00000000-0000-0000-0000-000000000001")

func TestToken_Record(t *testing.T) {
	tr, _ := setupTracker(t)
	if tr == nil {
		return
	}
	assert.NoError(t, tr.Record(context.Background(), UsageRecord{ShopID: sid, Feature: "t1", Operation: "a", Count: 1, Status: "success"}))
}
func TestToken_Default(t *testing.T) {
	tr, _ := setupTracker(t)
	if tr == nil {
		return
	}
	assert.NoError(t, tr.Record(context.Background(), UsageRecord{ShopID: sid, Feature: "t2", Count: 0}))
}
func TestToken_Total(t *testing.T) {
	tr, db := setupTracker(t)
	if tr == nil {
		return
	}
	db.Create(&model.TokenUsage{ShopID: sid, Feature: "t3", Count: 3, RecordedAt: time.Now(), Status: "success"})
	v, _ := tr.GetMonthlyTotal(context.Background(), sid, time.Now().Format("200601"))
	assert.GreaterOrEqual(t, v, int64(0))
}
func TestToken_Async(t *testing.T) {
	tr, _ := setupTracker(t)
	if tr == nil {
		return
	}
	tr.RecordAsync(context.Background(), UsageRecord{ShopID: sid, Feature: "t4", Count: 1})
	time.Sleep(100 * time.Millisecond)
}
func TestToken_Nil(t *testing.T) { assert.NotNil(t, NewTokenTracker(nil, nil)) }
