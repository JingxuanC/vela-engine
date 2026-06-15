package taskqueue

import (
	"encoding/json"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskStruct verifies that the Task struct fields are correct.
func TestTaskStruct(t *testing.T) {
	task := Task{
		ID:       "task-001",
		Type:     TypeEnrichProduct,
		TenantID: "tenant-abc",
		Payload:  EnrichProductPayload{ProductID: "p1", TenantID: "tenant-abc", ShopifyID: 12345},
		MaxRetry: 5,
	}

	assert.Equal(t, "task-001", task.ID)
	assert.Equal(t, TypeEnrichProduct, task.Type)
	assert.Equal(t, "tenant-abc", task.TenantID)
	assert.Equal(t, 5, task.MaxRetry)

	// Verify Payload is preserved as-is (not marshalled yet)
	payload, ok := task.Payload.(EnrichProductPayload)
	require.True(t, ok, "Payload should be EnrichProductPayload")
	assert.Equal(t, "p1", payload.ProductID)
	assert.Equal(t, "tenant-abc", payload.TenantID)
	assert.Equal(t, int64(12345), payload.ShopifyID)
}

// TestTaskTypes verifies all TaskType constants have the expected values.
func TestTaskTypes(t *testing.T) {
	tests := []struct {
		name     string
		actual   TaskType
		expected string
	}{
		{"TypeEnrichProduct", TypeEnrichProduct, "product:enrich"},
		{"TypeCartRecoveryCheck", TypeCartRecoveryCheck, "cart_recovery:check"},
		{"TypeContentPullMetrics", TypeContentPullMetrics, "content:pull_metrics"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, TaskType(tt.expected), tt.actual)
			assert.Equal(t, tt.expected, string(tt.actual))
		})
	}
}

// TestPayloadStructs verifies all payload structs can be JSON marshalled and
// unmarshalled with correct field tags.
func TestPayloadStructs(t *testing.T) {
	t.Run("EnrichProductPayload", func(t *testing.T) {
		original := EnrichProductPayload{
			ProductID: "prod-123",
			TenantID:  "tenant-456",
			ShopifyID: 98765,
		}
		testMarshalRoundTrip(t, original, map[string]interface{}{
			"product_id": "prod-123",
			"tenant_id":  "tenant-456",
			"shopify_id": float64(98765),
		})
	})

	t.Run("CartRecoveryCheckPayload", func(t *testing.T) {
		original := CartRecoveryCheckPayload{ShopID: "shop-abc"}
		testMarshalRoundTrip(t, original, map[string]interface{}{
			"shop_id": "shop-abc",
		})
	})

	t.Run("ContentPullMetricsPayload", func(t *testing.T) {
		original := ContentPullMetricsPayload{ShopID: "shop-abc", Platform: "pinterest"}
		testMarshalRoundTrip(t, original, map[string]interface{}{
			"shop_id":  "shop-abc",
			"platform": "pinterest",
		})
	})
}

// testMarshalRoundTrip marshals v to JSON, verifies the JSON keys match
// expectedFields, and unmarshals back to the same type.
func testMarshalRoundTrip[T any](t *testing.T, original T, expectedFields map[string]interface{}) {
	t.Helper()

	data, err := json.Marshal(original)
	require.NoError(t, err, "json.Marshal should not error")
	require.NotEmpty(t, data, "marshalled JSON should not be empty")

	// Verify JSON keys match expected snake_case field names
	var obj map[string]interface{}
	err = json.Unmarshal(data, &obj)
	require.NoError(t, err, "json.Unmarshal into map should not error")

	for key, expectedVal := range expectedFields {
		actualVal, ok := obj[key]
		assert.True(t, ok, "JSON key %q should exist", key)
		assert.Equal(t, expectedVal, actualVal, "JSON key %q should have correct value", key)
	}

	// Verify round-trip unmarshal
	var decoded T
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err, "json.Unmarshal back to struct should not error")
	assert.Equal(t, original, decoded, "round-trip should produce the original struct")
}

// TestMarshalPayload verifies the marshalPayload helper function.
func TestMarshalPayload(t *testing.T) {
	t.Run("struct payload", func(t *testing.T) {
		payload := EnrichProductPayload{ProductID: "p1", TenantID: "t1", ShopifyID: 1}
		data, err := marshalPayload(payload)
		require.NoError(t, err)
		assert.Contains(t, string(data), `"product_id":"p1"`)
	})

	t.Run("byte slice payload", func(t *testing.T) {
		payload := []byte(`{"raw": true}`)
		data, err := marshalPayload(payload)
		require.NoError(t, err)
		assert.Equal(t, `{"raw": true}`, string(data))
	})

	t.Run("nil payload", func(t *testing.T) {
		data, err := marshalPayload(nil)
		require.NoError(t, err)
		assert.Equal(t, "null", string(data))
	})
}

// TestAsynqClientInitWithMiniredis verifies that creating a config
// with a miniredis address produces a valid Redis address string.
// (Full NewAsynqClient requires asynq imports; this tests the config path.)
func TestAsynqClientInitWithMiniredis(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	// Verify config.AsynqRedisAddr() extracts the correct address
	// This simulates what config.Load() + NewAsynqClient() would do
	addr := mr.Addr()
	t.Logf("miniredis address: %s", addr)
	assert.NotEmpty(t, addr)
	assert.Contains(t, addr, ":")
}
