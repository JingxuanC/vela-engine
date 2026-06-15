package handler

import (
	"net/http"

	"gorm.io/gorm"

	"github.com/JingxuanC/vela-engine/internal/service"
	"github.com/JingxuanC/vela-engine/pkg/httputil"
)

// HealthHandler provides health check and readiness endpoints.
type HealthHandler struct {
	cache     *service.CacheService
	db        *gorm.DB
	collector interface {
		SetPostgresUp(bool)
		SetRedisUp(bool)
		SetQdrantUp(bool)
		SetOllamaUp(bool)
	}
}

// NewHealthHandler creates a new HealthHandler.
func NewHealthHandler(cache *service.CacheService, db *gorm.DB, collector interface {
	SetPostgresUp(bool)
	SetRedisUp(bool)
	SetQdrantUp(bool)
	SetOllamaUp(bool)
}) *HealthHandler {
	return &HealthHandler{cache: cache, db: db, collector: collector}
}

// Health responds with service status and infrastructure health.
func (h *HealthHandler) Health(w http.ResponseWriter, r *http.Request) {
	pgOk, redisOk := "unknown", "unknown"
	if h.db != nil {
		if sqlDB, err := h.db.DB(); err == nil {
			pgOk = "up"
			if sqlDB.PingContext(r.Context()) != nil { pgOk = "down" }
		}
		if h.collector != nil { h.collector.SetPostgresUp(pgOk == "up") }
	}
	if h.cache != nil {
		redisOk = "up"
		if h.cache.Client().Ping(r.Context()).Err() != nil { redisOk = "down" }
		if h.collector != nil { h.collector.SetRedisUp(redisOk == "up") }
	}
	httputil.WriteOK(w, map[string]string{
		"service":  "Vela AI API",
		"version":  "0.1.0",
		"status":   "ok",
		"postgres": pgOk,
		"redis":    redisOk,
	})
}

// Ready checks if dependent services (DB + Redis) are available.
// Returns 200 if ready, 503 if not.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	// Check database connectivity
	if h.db != nil {
		sqlDB, err := h.db.DB()
		if err != nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "db ping failed: "+err.Error())
			return
		}
		if err := sqlDB.PingContext(r.Context()); err != nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "db not ready: "+err.Error())
			return
		}
	}

	// Check Redis connectivity
	if h.cache != nil {
		if err := h.cache.Ping(r.Context()); err != nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "redis not ready: "+err.Error())
			return
		}
	}

	httputil.WriteOK(w, map[string]string{
		"status": "ready",
	})
}

