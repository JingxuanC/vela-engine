// Package taskqueue provides an Asynq-based task queue framework.
package taskqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/JingxuanC/vela-engine/internal/config"
)

// Task represents a unit of work to be enqueued in the task queue.
type Task struct {
	ID       string
	Type     TaskType
	TenantID string
	Payload  interface{} // serialised to JSON before enqueue
	MaxRetry int
}

// Client defines the interface for enqueuing and monitoring tasks.
type Client interface {
	// Enqueue adds a task to the queue with default priority.
	Enqueue(ctx context.Context, task *Task) (string, error)
	// EnqueueAt schedules a task to be enqueued at a specific time.
	EnqueueAt(ctx context.Context, task *Task, t time.Time) (string, error)
	// GetStatus returns the current state of a task by its ID.
	GetStatus(ctx context.Context, taskID string) (*asynq.TaskInfo, error)
}

// AsynqClient implements Client using the Asynq task queue library.
type AsynqClient struct {
	client     *asynq.Client
	cfg        *config.Config
}

// NewAsynqClient creates a new AsynqClient connected to the configured Redis.
func NewAsynqClient(cfg *config.Config) (*AsynqClient, error) {
	redisAddr := cfg.AsynqRedisAddr()
	client := asynq.NewClient(asynq.RedisClientOpt{
		Addr: redisAddr,
		DB:   cfg.AsynqRedisDB,
	})
	return &AsynqClient{client: client, cfg: cfg}, nil
}

// Close shuts down the underlying Asynq client.
func (c *AsynqClient) Close() error {
	return c.client.Close()
}

// Enqueue adds a task to the default queue.
func (c *AsynqClient) Enqueue(ctx context.Context, task *Task) (string, error) {
	payload, err := marshalPayload(task.Payload)
	if err != nil {
		return "", fmt.Errorf("taskqueue: marshal payload: %w", err)
	}

	maxRetry := task.MaxRetry
	if maxRetry <= 0 {
		maxRetry = c.cfg.MaxRetries
	}

	opts := []asynq.Option{
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(c.cfg.TaskTimeout()),
	}
	if task.ID != "" {
		opts = append(opts, asynq.TaskID(task.ID))
	}
	aTask := asynq.NewTask(string(task.Type), payload, opts...)
	info, err := c.client.EnqueueContext(ctx, aTask)
	if err != nil {
		return "", fmt.Errorf("taskqueue: enqueue: %w", err)
	}
	return info.ID, nil
}

// EnqueueAt schedules a task to be processed at a specific time.
func (c *AsynqClient) EnqueueAt(ctx context.Context, task *Task, t time.Time) (string, error) {
	payload, err := marshalPayload(task.Payload)
	if err != nil {
		return "", fmt.Errorf("taskqueue: marshal payload: %w", err)
	}

	maxRetry := task.MaxRetry
	if maxRetry <= 0 {
		maxRetry = c.cfg.MaxRetries
	}

	delay := time.Until(t)
	if delay < 0 {
		delay = 0 // process immediately if the scheduled time is in the past
	}

	opts := []asynq.Option{
		asynq.MaxRetry(maxRetry),
		asynq.Timeout(c.cfg.TaskTimeout()),
		asynq.ProcessIn(delay),
	}
	if task.ID != "" {
		opts = append(opts, asynq.TaskID(task.ID))
	}
	aTask := asynq.NewTask(string(task.Type), payload, opts...)
	info, err := c.client.EnqueueContext(ctx, aTask)
	if err != nil {
		return "", fmt.Errorf("taskqueue: enqueue at: %w", err)
	}
	return info.ID, nil
}

// GetStatus returns task status info from the queue.
// It queries all known queues and returns the first match.
func (c *AsynqClient) GetStatus(ctx context.Context, taskID string) (*asynq.TaskInfo, error) {
	inspector := asynq.NewInspector(asynq.RedisClientOpt{
		Addr: c.cfg.AsynqRedisAddr(),
		DB:   c.cfg.AsynqRedisDB,
	})
	// Try all known queues; tasks are enqueued to "default" by default.
	for _, q := range []string{"default", "critical", "low"} {
		info, err := inspector.GetTaskInfo(q, taskID)
		if err == nil {
			return info, nil
		}
		// "queue not found" or "task not found" — try next queue
	}
	return nil, fmt.Errorf("taskqueue: task %q not found in any queue", taskID)
}

// NewInspector creates an Asynq Inspector for the same Redis connection.
func NewInspector(cfg *config.Config) *asynq.Inspector {
	return asynq.NewInspector(asynq.RedisClientOpt{
		Addr: cfg.AsynqRedisAddr(),
		DB:   cfg.AsynqRedisDB,
	})
}

// marshalPayload serialises an arbitrary payload to JSON bytes.
func marshalPayload(v interface{}) ([]byte, error) {
	if data, ok := v.([]byte); ok {
		return data, nil
	}
	return json.Marshal(v)
}
