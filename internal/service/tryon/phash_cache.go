package tryon

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"time"

	"log/slog"
)

const (
	phashKeyPrefix = "phash:"
	phashCacheTTL  = 7 * 24 * time.Hour // 7 days
)

// getPhashCache checks Redis for a cached try-on result matching the given
// perceptual hash. Returns the result URL and true on cache hit.
func (p *PipelineOrchestrator) getPhashCache(ctx context.Context, hash string) (string, bool) {
	key := phashKeyPrefix + hash
	url, err := p.cache.Get(ctx, key)
	if err != nil {
		slog.Warn("phash cache: redis lookup failed", "hash", hash, "error", err)
		return "", false
	}
	if url == "" {
		return "", false
	}
	slog.Info("phash cache: hit", "hash", hash)
	return url, true
}

// setPhashCache stores a result URL in Redis keyed by perceptual hash.
// TTL is 7 days to balance cache freshness with hit rate.
func (p *PipelineOrchestrator) setPhashCache(ctx context.Context, hash, url string) {
	if hash == "" || url == "" {
		return
	}
	key := phashKeyPrefix + hash
	if err := p.cache.Set(ctx, key, url, phashCacheTTL); err != nil {
		slog.Warn("phash cache: redis write failed", "hash", hash, "error", err)
		return
	}
	slog.Info("phash cache: stored", "hash", hash)
}

// computePhash decodes raw image bytes and returns the perceptual hash string.
// This wraps ImageProcessor.ComputePHash with automatic image decoding.
func (p *PipelineOrchestrator) computePhash(imgBytes []byte) (string, error) {
	img, _, err := image.Decode(bytes.NewReader(imgBytes))
	if err != nil {
		return "", fmt.Errorf("compute phash: decode: %w", err)
	}
	return p.imgProc.ComputePHash(img), nil
}
