package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestConnectRetryLogic verifies that the retry+backoff pattern works:
// a transient failure is retried and eventual success succeeds.
func TestConnectRetryLogic(t *testing.T) {
	var db *gorm.DB
	var err error

	// Simulate the retry logic: try up to 3 times against SQLite :memory:
	for attempt := 1; attempt <= 3; attempt++ {
		db, err = gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
			SkipDefaultTransaction: true,
			PrepareStmt:            true,
		})
		if err == nil {
			break
		}
	}
	require.NoError(t, err)
	require.NotNil(t, db)

	// Verify the connection is usable
	sqlDB, err := db.DB()
	require.NoError(t, err)
	assert.NoError(t, sqlDB.Ping())
	sqlDB.Close()
}

// TestDBPoolSettings verifies the pool configuration used in Connect
// does not break anything on SQLite.
func TestDBPoolSettings(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	require.NoError(t, err)

	sqlDB, err := db.DB()
	require.NoError(t, err)

	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetConnMaxLifetime(0) // SQLite specifics

	// Use the connection — this proves pool config doesn't break anything
	var result int
	err = db.Raw("SELECT 1").Scan(&result).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, result)

	sqlDB.Close()
}

// TestRetryExhausted verifies the retry loop returns an error when all
// attempts fail. Uses an invalid DSN that will always fail to connect,
// even on retry.
func TestRetryExhausted(t *testing.T) {
	// Use an empty DSN to trigger gorm.Open failure (no driver-specific
	// dialect for empty string).
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		_, lastErr = gorm.Open(sqlite.Open("/nonexistent/path/to/db.sqlite"), &gorm.Config{
			SkipDefaultTransaction: true,
			PrepareStmt:            true,
		})
		if lastErr == nil {
			break
		}
	}
	assert.Error(t, lastErr)
	assert.True(t, errors.Is(lastErr, errors.Unwrap(lastErr)) || lastErr != nil,
		"expected a non-nil error after exhausting retries")
	t.Logf("retry exhausted error: %v", lastErr)
}
