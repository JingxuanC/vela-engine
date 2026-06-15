package notify

import (
	"context"
)

// Trigger defines a condition-based notification trigger.
type Trigger struct {
	Event     string
	Condition func(ctx context.Context, data interface{}) (bool, *Notification)
}
