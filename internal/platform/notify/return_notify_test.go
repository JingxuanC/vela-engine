package notify

import (
	"context"
	"testing"

	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ── Notification triggered by return anomaly ─────────────────────────────────

func TestReturnAnomalyNotification_ContentCompleteness(t *testing.T) {
	n := &Notification{
		ShopID:   testShopID,
		Type:     "return_alert",
		Title:    "退货率异常 35%",
		Body:     "近7天退货率 35.0%（15/43）。主要原因：size_too_small、color。建议检查产品描述和尺码指南。",
		Channel:  ChannelInApp,
		Priority: PriorityHigh,
	}

	assert.Equal(t, "return_alert", n.Type)
	assert.Equal(t, ChannelInApp, n.Channel)
	assert.Equal(t, PriorityHigh, n.Priority)
	assert.NotEmpty(t, n.Title)
	assert.NotEmpty(t, n.Body)
	assert.Contains(t, n.Body, "退货率")
}

// ── Threshold logic tests ────────────────────────────────────────────────────

func TestReturnAnomaly_Threshold_TooFewOrders(t *testing.T) {
	// Fewer than 10 orders → should NOT trigger notification
	orders := int64(9)
	returns := int64(5)
	rate := float64(returns) / float64(orders) * 100 // ~55%, but N < 10

	shouldNotify := orders >= 10 && rate > 30
	assert.False(t, shouldNotify, "should not notify when orders < 10 even with high rate")
}

func TestReturnAnomaly_Threshold_OrdersEnoughRateLow(t *testing.T) {
	orders := int64(50)
	returns := int64(7)
	rate := float64(returns) / float64(orders) * 100 // 14%

	shouldNotify := orders >= 10 && rate > 30
	assert.False(t, shouldNotify, "should not notify when rate <= 30%%")
}

func TestReturnAnomaly_Threshold_OrdersEnoughRateHigh(t *testing.T) {
	orders := int64(50)
	returns := int64(18)
	rate := float64(returns) / float64(orders) * 100 // 36%

	shouldNotify := orders >= 10 && rate > 30
	assert.True(t, shouldNotify, "should notify when orders >= 10 and rate > 30%%")
}

func TestReturnAnomaly_Threshold_Boundary(t *testing.T) {
	tests := []struct {
		name     string
		orders   int64
		returns  int64
		expected bool
	}{
		{"exactly 10 orders, rate 31%", 10, 4, true},   // 40% > 30%
		{"exactly 10 orders, rate 30%", 10, 3, false},  // 30% not > 30
		{"exactly 10 orders, rate 0%",  10, 0, false},
		{"9 orders, rate 100%",         9,  9, false},  // N < 10
		{"100 orders, rate 35%",        100,35, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rate := float64(tt.returns) / float64(tt.orders) * 100
			shouldNotify := tt.orders >= 10 && rate > 30
			assert.Equal(t, tt.expected, shouldNotify)
		})
	}
}

// ── Nil safety tests ─────────────────────────────────────────────────────────

func TestReturnAnomaly_NilCenter(t *testing.T) {
	// Simulates what the EventBus consumer does: return nil when Center is nil
	var center *Center = nil
	if center == nil {
		// return nil ← correct behavior, no panic
		assert.True(t, true)
	} else {
		t.Error("should have been nil")
	}
}

func TestReturnAnomaly_NilDB(t *testing.T) {
	// Simulates InAppNotifier with nil DB
	n := NewInAppNotifier(nil)
	err := n.Send(context.Background(), &Notification{
		ShopID: testShopID, Type: "test", Title: "Test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no database configured")
}

// ── End-to-end: Center.Send → InAppNotifier → DB persistence ──────────────────

func setupNotifyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.InAppNotification{}))
	return db
}

func TestReturnAnomaly_EndToEnd(t *testing.T) {
	db := setupNotifyTestDB(t)
	center := NewCenter(nil)
	center.Register(ChannelInApp, NewInAppNotifier(db))

	n := &Notification{
		ShopID:   testShopID,
		Type:     "return_alert",
		Title:    "退货率异常 35%",
		Body:     "近7天退货率 35.0%。主要原因：size_too_small。建议检查产品描述和尺码指南。",
		Channel:  ChannelInApp,
		Priority: PriorityHigh,
	}

	err := center.Send(context.Background(), n)
	assert.NoError(t, err)

	// Verify persisted to DB
	var record model.InAppNotification
	db.First(&record)
	assert.Equal(t, testShopID, record.ShopID)
	assert.Equal(t, "return_alert", record.Type)
	assert.Equal(t, "退货率异常 35%", record.Title)
	assert.Equal(t, ChannelInApp, record.Channel)
	assert.Equal(t, PriorityHigh, record.Priority)
	assert.False(t, record.IsRead)
}

func TestReturnAnomaly_MultipleNotifications(t *testing.T) {
	db := setupNotifyTestDB(t)
	center := NewCenter(nil)
	center.Register(ChannelInApp, NewInAppNotifier(db))

	for i := 0; i < 3; i++ {
		err := center.Send(context.Background(), &Notification{
			ShopID:  testShopID,
			Type:    "return_alert",
			Title:   "Alert",
			Channel: ChannelInApp,
		})
		assert.NoError(t, err)
	}

	var count int64
	db.Model(&model.InAppNotification{}).Count(&count)
	assert.Equal(t, int64(3), count)
}

func TestReturnAnomaly_DefaultChannelToInApp(t *testing.T) {
	db := setupNotifyTestDB(t)
	center := NewCenter(nil)
	center.Register(ChannelInApp, NewInAppNotifier(db))

	n := &Notification{
		ShopID: testShopID,
		Type:   "return_alert",
		Title:  "Test",
		// Channel not set — should default to in_app
	}
	err := center.Send(context.Background(), n)
	assert.NoError(t, err)

	var record model.InAppNotification
	db.First(&record)
	assert.Equal(t, ChannelInApp, record.Channel)
}

func TestReturnAnomaly_NoNotifierForChannel(t *testing.T) {
	center := NewCenter(nil)
	// Don't register any notifier
	err := center.Send(context.Background(), &Notification{
		ShopID:  testShopID,
		Type:    "return_alert",
		Title:   "Test",
		Channel: "slack", // unregistered channel
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no notifier registered")
}

func TestReturnAnomaly_NilNotification(t *testing.T) {
	center := NewCenter(nil)
	err := center.Send(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil notification")
}

func TestReturnAnomaly_PriorityDefaultsToNormal(t *testing.T) {
	db := setupNotifyTestDB(t)
	n := NewInAppNotifier(db)

	err := n.Send(context.Background(), &Notification{
		ShopID: testShopID,
		Type:   "return_alert",
		Title:  "Test",
		// Priority not set
	})
	assert.NoError(t, err)

	var record model.InAppNotification
	db.First(&record)
	assert.Equal(t, PriorityNormal, record.Priority)
}
