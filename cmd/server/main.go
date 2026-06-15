// Vela Engine — AI analytics server. Full-featured entry point.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/JingxuanC/vela-engine/internal/config"
	"github.com/JingxuanC/vela-engine/internal/server"
)

// loadDotEnv reads .env files and sets environment variables.
// Searches: .env (local), ../.env (project root). Does NOT overwrite existing vars.
func loadDotEnv() {
	candidates := []string{".env"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".env"))
	}

	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
			}
		}
		f.Close()
	}
}

func main() {
	loadDotEnv()

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Vela Engine — AI Analytics Server

Usage:
  vela-engine [flags]

Flags:
  --config <path>   Path to YAML config file (default: auto-detect)

Environment:
  GO_ENV            Environment: development | production (default: development)
  DATABASE_URL      PostgreSQL connection string
  REDIS_URL         Redis connection string
  DASHSCOPE_API_KEY Alibaba DashScope API key
  DEEPSEEK_API_KEY  DeepSeek API key
  OPENAI_API_KEY    OpenAI API key
  API_TOKEN         Service-to-service auth token (required in production)
  PORT              Server port (default: 8000)
`)
	}

	flag.Parse()

	// Setup structured logger
	logLevel := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})))

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("configuration loaded",
		"env", cfg.GoEnv,
		"log_level", cfg.LogLevel,
	)

	// Create and start server
	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	if err := srv.Serve(); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
