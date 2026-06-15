package sync

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type SyncResult struct {
	TotalRecords, NewRecords, UpdatedRecords, Errors int
	FailedPages                                      int
	DurationMs                                       int64
}
type SyncOptions struct {
	ShopID   uuid.UUID
	Since    time.Time
	Limit    int
	FullSync bool
}
type Syncer interface {
	Sync(ctx context.Context, opts SyncOptions) (*SyncResult, error)
	EntityType() string
}
