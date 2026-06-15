// Vela Engine — standalone AI analytics server.
// Runs independently of Shopify. Connects to PostgreSQL, registers core API routes.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/JingxuanC/vela-engine/internal/database"
	"github.com/JingxuanC/vela-engine/internal/handler"
	"github.com/JingxuanC/vela-engine/internal/service"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// ── Database ──────────────────────────────────────────────
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://vela:***@localhost:5432/vela?sslmode=disable"
	}

	db, err := database.Connect(dbURL)
	if err != nil {
		logger.Error("database connect failed", "error", err)
		os.Exit(1)
	}

	// ── Services ──────────────────────────────────────────────
	recEngine := service.NewRecommendationEngine(db)
	attrService := service.NewUnifiedAttributionService(db)

	// ── Handlers ──────────────────────────────────────────────
	recHandler := handler.NewRecommendationsHandler(db, recEngine)
	attrHandler := handler.NewUnifiedAttributionHandler(db, attrService)

	// ── Router ────────────────────────────────────────────────
	r := chi.NewRouter()
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(chiMiddleware.Logger)
	r.Use(chiMiddleware.Recoverer)

	// Health
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"vela-engine"}`))
	})

	// API
	r.Route("/api", func(r chi.Router) {
		r.Route("/recommendations", func(r chi.Router) {
			r.Get("/fbt", recHandler.FBT)
			r.Get("/trending", recHandler.Trending)
			r.Get("/stats", recHandler.Stats)
		})
		r.Route("/analytics", func(r chi.Router) {
			r.Route("/attribution", func(r chi.Router) {
				r.Get("/unified", attrHandler.GetUnified)
			})
		})
	})

	// ── Start ─────────────────────────────────────────────────
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	server := &http.Server{Addr: ":" + port, Handler: r}

	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		logger.Info("shutting down...")
		server.Close()
	}()

	logger.Info("vela-engine starting", "port", port)
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
