package taskqueue

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hibiken/asynq"

	"github.com/JingxuanC/vela-engine/internal/config"
)

// NewServer creates a new Asynq server configured with the given Redis address and DB.
func NewServer(cfg *config.Config) *asynq.Server {
	srv := asynq.NewServer(
		asynq.RedisClientOpt{
			Addr: cfg.AsynqRedisAddr(),
			DB:   cfg.AsynqRedisDB,
		},
		asynq.Config{
			Concurrency: 10,
			Queues: map[string]int{
				"critical": 6,
				"default":  3,
				"low":      1,
			},
			// ErrorHandler logs all task failures, including unmatched task types
			// that would otherwise be silently retried and archived.
			ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
				retried, _ := asynq.GetRetryCount(ctx)
				maxRetry, _ := asynq.GetMaxRetry(ctx)
				slog.Error("taskqueue: task failed",
					"type", task.Type(),
					"task_id", task.ResultWriter().TaskID(),
					"retried", retried,
					"max_retry", maxRetry,
					"error", err,
				)
			}),
		},
	)
	return srv
}

// RegisterHandler registers a handler function for a given task type on the mux.
func RegisterHandler(mux *asynq.ServeMux, taskType TaskType, handler func(context.Context, *asynq.Task) error) {
	mux.HandleFunc(string(taskType), handler)
}

// Start starts the Asynq server with the provided mux and blocks until shutdown.
func Start(srv *asynq.Server, mux *asynq.ServeMux) error {
	slog.Info("taskqueue: starting asynq server")
	if err := srv.Start(mux); err != nil {
		return fmt.Errorf("taskqueue: start server: %w", err)
	}
	return nil
}

// Shutdown gracefully stops the Asynq server.
func Shutdown(srv *asynq.Server, ctx context.Context) error {
	slog.Info("taskqueue: shutting down asynq server")
	// Use srv.Stop() for graceful shutdown
	srv.Shutdown()
	slog.Info("taskqueue: server stopped")
	return nil
}
