package notify

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/JingxuanC/vela-engine/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var testShopID = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
var testShopID2 = uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")

// --- mockEmailSender for testing ---

type mockEmailSender struct {
	sendErr error
	calls   int
}

func (m *mockEmailSender) SendContactNotification(name, email, message string) error {
	m.calls++
	return m.sendErr
}

func (m *mockEmailSender) SendNotification(to, subject, body string) (string, error) {
	m.calls++
	return "mock-msg-id", m.sendErr
}

// --- Notification struct tests ---

func TestNotificationDefaults(t *testing.T) {
	n := Notification{
		ShopID: testShopID,
		Type:   "order_shipped",
		Title:  "Order Shipped",
		Body:   "Your order #123 has shipped.",
	}
	assert.Equal(t, "", n.Priority, "priority should default to empty")
	assert.Equal(t, "", n.Channel, "channel should default to empty")
}

// --- EmailNotifier tests ---

func TestEmailNotifier_Send_Success(t *testing.T) {
	mock := &mockEmailSender{}
	en := NewEmailNotifier(mock, nil)

	n := &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Test Notification",
		Body:   "This is a test.",
	}
	err := en.Send(context.Background(), n)
	assert.NoError(t, err)
	assert.Equal(t, 1, mock.calls)
}

func TestEmailNotifier_Send_NilClient(t *testing.T) {
	en := NewEmailNotifier(nil, nil)
	err := en.Send(context.Background(), &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no email client configured")
}

func TestEmailNotifier_Send_ClientError(t *testing.T) {
	mock := &mockEmailSender{sendErr: errors.New("network error")}
	en := NewEmailNotifier(mock, nil)

	err := en.Send(context.Background(), &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "network error")
}

func TestEmailNotifier_Send_ActionURLInBody(t *testing.T) {
	mock := &mockEmailSender{}
	en := NewEmailNotifier(mock, nil)

	n := &Notification{
		ShopID:    testShopID,
		Type:      "action_required",
		Title:     "Action Required",
		Body:      "Please review the report.",
		ActionURL: "https://example.com/review",
		Priority:  PriorityHigh,
	}
	err := en.Send(context.Background(), n)
	assert.NoError(t, err)
	assert.Equal(t, 1, mock.calls)
}

func TestEmailNotifier_Send_PrioritySubject(t *testing.T) {
	mock := &mockEmailSender{}
	en := NewEmailNotifier(mock, nil)

	n := &Notification{
		ShopID:   testShopID,
		Type:     "urgent",
		Title:    "Urgent Alert",
		Body:     "Something urgent happened.",
		Priority: PriorityUrgent,
	}
	err := en.Send(context.Background(), n)
	assert.NoError(t, err)
	assert.Equal(t, 1, mock.calls)
}

func TestEmailNotifier_Send_EmptyPrioritySubject(t *testing.T) {
	mock := &mockEmailSender{}
	en := NewEmailNotifier(mock, nil)

	n := &Notification{
		ShopID:   testShopID,
		Type:     "info",
		Title:    "Info Update",
		Body:     "Just an info update.",
		Priority: PriorityLow,
	}
	err := en.Send(context.Background(), n)
	assert.NoError(t, err)
	assert.Equal(t, 1, mock.calls)
}

// --- InAppNotifier tests ---

func setupInMemoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.InAppNotification{}))
	return db
}

func TestInAppNotifier_Send_Success(t *testing.T) {
	db := setupInMemoryDB(t)
	n := NewInAppNotifier(db)

	err := n.Send(context.Background(), &Notification{
		ShopID:   testShopID,
		Type:     "order_shipped",
		Title:    "Order Shipped",
		Body:     "Your order #123 has shipped.",
		Channel:  "in_app",
		Priority: PriorityNormal,
	})
	assert.NoError(t, err)

	var count int64
	db.Model(&model.InAppNotification{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestInAppNotifier_Send_EmptyPriorityDefaultsToNormal(t *testing.T) {
	db := setupInMemoryDB(t)
	n := NewInAppNotifier(db)

	err := n.Send(context.Background(), &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Test",
		Body:   "Test body.",
	})
	assert.NoError(t, err)

	var record model.InAppNotification
	db.First(&record)
	assert.Equal(t, PriorityNormal, record.Priority)
}

func TestInAppNotifier_Send_NilDB(t *testing.T) {
	n := NewInAppNotifier(nil)
	err := n.Send(context.Background(), &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Test",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no database configured")
}

// --- Center tests ---

func TestCenter_Send_Success(t *testing.T) {
	db := setupInMemoryDB(t)
	center := NewCenter(nil)
	center.Register(ChannelInApp, NewInAppNotifier(db))

	n := &Notification{
		ShopID:  testShopID,
		Type:    "test",
		Channel: ChannelInApp,
		Title:   "Test",
	}
	err := center.Send(context.Background(), n)
	assert.NoError(t, err)
}

func TestCenter_Send_NoNotifier(t *testing.T) {
	center := NewCenter(nil)
	n := &Notification{
		ShopID:  testShopID,
		Type:    "test",
		Channel: "slack",
		Title:   "Test",
	}
	err := center.Send(context.Background(), n)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), `no notifier registered for channel "slack"`)
}

func TestCenter_Send_NilNotification(t *testing.T) {
	center := NewCenter(nil)
	err := center.Send(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nil notification")
}

func TestCenter_Send_DefaultChannel(t *testing.T) {
	db := setupInMemoryDB(t)
	center := NewCenter(nil)
	center.Register(ChannelInApp, NewInAppNotifier(db))

	n := &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Test",
		// No Channel set — should default to in_app
	}
	err := center.Send(context.Background(), n)
	assert.NoError(t, err)

	var count int64
	db.Model(&model.InAppNotification{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestCenter_SendMulti(t *testing.T) {
	db := setupInMemoryDB(t)
	mockEmail := &mockEmailSender{}
	center := NewCenter(nil)
	center.Register(ChannelEmail, NewEmailNotifier(mockEmail, nil))
	center.Register(ChannelInApp, NewInAppNotifier(db))

	n := &Notification{
		ShopID: testShopID,
		Type:   "test",
		Title:  "Multi-channel test",
		Body:   "Testing multi-channel delivery.",
	}
	errs := center.SendMulti(context.Background(), n, []string{ChannelEmail, ChannelInApp})
	assert.Empty(t, errs, "expected no errors from multi-channel send")
	assert.Equal(t, 1, mockEmail.calls)

	var count int64
	db.Model(&model.InAppNotification{}).Count(&count)
	assert.Equal(t, int64(1), count)
}

func TestCenter_RegisterAndSend(t *testing.T) {
	db := setupInMemoryDB(t)
	center := NewCenter(nil)

	// Should fail before registration
	err := center.Send(context.Background(), &Notification{
		ShopID:  testShopID,
		Type:    "test",
		Channel: ChannelInApp,
		Title:   "Test",
	})
	assert.Error(t, err)

	// Register and retry
	center.Register(ChannelInApp, NewInAppNotifier(db))
	err = center.Send(context.Background(), &Notification{
		ShopID:  testShopID,
		Type:    "test",
		Channel: ChannelInApp,
		Title:   "Test",
	})
	assert.NoError(t, err)
}

// --- Trigger business-rule tests (mirrors server.go registration) ---

type mockEvent struct {
	ShopID uuid.UUID
}

func TestTrigger_OrderSynced_BusinessRule(t *testing.T) {
	center := NewCenter(nil)
	fakeShopID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")

	center.RegisterTrigger(Trigger{
		Event: "pipeline.order.synced",
		Condition: func(ctx context.Context, data interface{}) (bool, *Notification) {
			e, ok := data.(*mockEvent)
			if !ok || e == nil {
				return false, nil
			}
			return true, &Notification{
				ShopID:   e.ShopID,
				Type:     "order.synced",
				Title:    "新订单已同步",
				Body:     "订单数据已同步至本地。",
				Channel:  ChannelInApp,
				Priority: PriorityNormal,
			}
		},
	})

	t.Run("matching event produces notification", func(t *testing.T) {
		results := center.Check(context.Background(), "pipeline.order.synced", &mockEvent{ShopID: fakeShopID})
		assert.Len(t, results, 1)
		assert.Equal(t, "order.synced", results[0].Type)
		assert.Equal(t, "新订单已同步", results[0].Title)
		assert.Equal(t, fakeShopID, results[0].ShopID)
	})

	t.Run("non-matching event produces nothing", func(t *testing.T) {
		results := center.Check(context.Background(), "pipeline.customer.synced", &mockEvent{ShopID: fakeShopID})
		assert.Len(t, results, 0)
	})

	t.Run("nil data should not panic", func(t *testing.T) {
		results := center.Check(context.Background(), "pipeline.order.synced", nil)
		assert.Len(t, results, 0)
	})

	t.Run("empty shop ID passes through", func(t *testing.T) {
		results := center.Check(context.Background(), "pipeline.order.synced", &mockEvent{ShopID: uuid.Nil})
		assert.Len(t, results, 1)
		assert.Equal(t, uuid.Nil, results[0].ShopID)
	})

	t.Run("wrong type data should not panic", func(t *testing.T) {
		results := center.Check(context.Background(), "pipeline.order.synced", "not a pointer")
		assert.Len(t, results, 0)
	})
}
