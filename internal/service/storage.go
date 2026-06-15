package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// StorageService handles Cloudflare R2 object storage operations.
type StorageService struct {
	accountID       string
	accessKeyID     string
	secretAccessKey string
	bucketName      string
	publicURL       string
	http            *http.Client
}

// NewStorageService creates a new R2 storage service.
func NewStorageService(accountID, accessKeyID, secretAccessKey, bucketName, publicURL string) *StorageService {
	return &StorageService{
		accountID:       accountID,
		accessKeyID:     accessKeyID,
		secretAccessKey: secretAccessKey,
		bucketName:      bucketName,
		publicURL:       publicURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Upload uploads data to R2 and returns the public URL.
// key is the object path (e.g., "tryon/abc-123/result.png").
func (s *StorageService) Upload(ctx context.Context, key string, data []byte, contentType string) (string, error) {
	url := fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s", s.accountID, key)

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("storage: create upload request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	// R2 uses S3-compatible auth; for simplicity, use presigned URLs or direct with auth
	// In production, use AWS SDK v2 for S3-compatible storage
	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("storage: upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("storage: upload HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	publicURL := fmt.Sprintf("%s/%s", s.publicURL, key)
	return publicURL, nil
}

// Delete removes an object from R2.
func (s *StorageService) Delete(ctx context.Context, key string) error {
	url := fmt.Sprintf("https://%s.r2.cloudflarestorage.com/%s", s.accountID, key)

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return fmt.Errorf("storage: create delete request: %w", err)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("storage: delete failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("storage: delete HTTP %d: %s", resp.StatusCode, string(body[:min(len(body), 500)]))
	}

	return nil
}

// GetPresignedURL generates a presigned URL for temporary access to an object.
// In production, use AWS SDK v2 presigner. This is a placeholder.
func (s *StorageService) GetPresignedURL(key string, expiry time.Duration) string {
	return fmt.Sprintf("%s/%s?expires=%d", s.publicURL, key, time.Now().Add(expiry).Unix())
}

// PublicURL returns the public URL for an object.
func (s *StorageService) PublicURL(key string) string {
	return fmt.Sprintf("%s/%s", s.publicURL, key)
}
