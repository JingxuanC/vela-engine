package tryon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"log/slog"
)

// ---------------------------------------------------------------------------
// Constants (mirror Python image_processor.py)
// ---------------------------------------------------------------------------

const (
	maxImageSizeBytes = 5 * 1024 * 1024 // 5 MB
	minImageDim       = 64
	maxImageDim       = 4096
	targetWidth       = 768
	targetHeight      = 1024
	compressQuality   = 85              // JPEG fallback quality
	maxCompressedSize = 1 * 1024 * 1024 // 1 MB target
	phashSize         = 8               // 8x8 average hash -> 64-bit
)

var allowedFormats = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
}

var allowedExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".webp": true,
}

// allowedImageHosts defines hosts permitted for image fetching (SSRF prevention).
var allowedImageHosts = map[string]bool{
	"localhost":        true,
	"127.0.0.1":        true,
	"images.vela.dev": true,
	"cdn.shopify.com":  true,
	"shopify.com":      true,
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ImageValidationError is returned when an image fails validation.
type ImageValidationError struct {
	Reason string
}

func (e *ImageValidationError) Error() string {
	return fmt.Sprintf("image validation failed: %s", e.Reason)
}

// ImageProcessingError is returned when image processing fails.
type ImageProcessingError struct {
	Reason string
}

func (e *ImageProcessingError) Error() string {
	return fmt.Sprintf("image processing failed: %s", e.Reason)
}

// ---------------------------------------------------------------------------
// ImageProcessor handles image validation, preprocessing, and hashing.
// ---------------------------------------------------------------------------

// ImageProcessor provides image validation, preprocessing, and perceptual hashing.
type ImageProcessor struct {
	httpClient *http.Client
	maxSize    int64
}

// NewImageProcessor creates a new ImageProcessor with sensible defaults.
func NewImageProcessor() *ImageProcessor {
	return &ImageProcessor{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:    20,
				IdleConnTimeout: 60 * time.Second,
			},
		},
		maxSize: maxImageSizeBytes,
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// ValidatedImage holds the result of image validation and preprocessing.
type ValidatedImage struct {
	RawBytes        []byte
	ContentType     string
	Image           image.Image
	PreprocessedURL string // R2 URL of preprocessed image (set after upload)
	Phash           string // perceptual hash hex string
	RawSize         int    // original size in bytes
}

// ValidateImage downloads and validates an image from a URL.
// Returns the raw bytes, content type, and decoded image.
// Mirrors Python: validate_image() in image_processor.py
func (p *ImageProcessor) ValidateImage(ctx context.Context, imageURL string) ([]byte, string, image.Image, error) {
	// 1. Validate URL (SSRF prevention)
	if err := validateImageURL(imageURL); err != nil {
		return nil, "", nil, err
	}

	// 2. Download
	raw, contentType, err := p.fetchImage(ctx, imageURL)
	if err != nil {
		return nil, "", nil, err
	}

	// 3. Size check
	if len(raw) > int(p.maxSize) {
		return nil, "", nil, &ImageValidationError{
			Reason: fmt.Sprintf("image too large: %.1f MB (max %d MB)",
				float64(len(raw))/1024/1024, p.maxSize/(1024*1024)),
		}
	}

	// 4. Format check via content-type
	if !allowedFormats[contentType] {
		ext := getExtension(imageURL)
		if !allowedExtensions[ext] {
			return nil, "", nil, &ImageValidationError{
				Reason: fmt.Sprintf("unsupported image format: %s (allowed: jpeg, png, webp)", contentType),
			}
		}
	}

	// 5. Decode and integrity check
	img, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", nil, &ImageValidationError{
			Reason: fmt.Sprintf("corrupted or unreadable image: %v", err),
		}
	}

	// 6. Dimension sanity
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < minImageDim || h < minImageDim {
		return nil, "", nil, &ImageValidationError{
			Reason: fmt.Sprintf("image too small (%dx%d), minimum %dx%d pixels", w, h, minImageDim, minImageDim),
		}
	}
	if w > maxImageDim || h > maxImageDim {
		return nil, "", nil, &ImageValidationError{
			Reason: fmt.Sprintf("image too large (%dx%d), maximum %dx%d pixels", w, h, maxImageDim, maxImageDim),
		}
	}

	slog.Info("image validated",
		"url", imageURL,
		"format", format,
		"dimensions", fmt.Sprintf("%dx%d", w, h),
		"size_bytes", len(raw),
	)

	return raw, contentType, img, nil
}

// PreprocessPerson preprocesses a person image for try-on:
//  1. Smart crop to upper-body region (3:4 aspect ratio)
//  2. Resize to 768x1024 with padding
//
// Mirrors Python: preprocess_person() in image_processor.py
// Note: Background removal (rembg) is Python-only ML; skipped in Go.
func (p *ImageProcessor) PreprocessPerson(img image.Image) image.Image {
	// 1. Smart crop to upper body portrait (3:4 aspect ratio)
	img = cropToUpperBody(img)

	// 2. Resize to target dimensions with padding
	img = resizeWithPadding(img, targetWidth, targetHeight)

	return img
}

// PreprocessGarment preprocesses a garment image for try-on:
//  1. Auto-crop to content bounding box (non-white/non-transparent)
//  2. Resize to 768x1024 with padding
//
// Mirrors Python: preprocess_garment() in image_processor.py
// Note: Background removal (rembg) is Python-only ML; skipped in Go.
func (p *ImageProcessor) PreprocessGarment(img image.Image) image.Image {
	// 1. Auto-crop to garment bounding box
	img = cropToContent(img)

	// 2. Resize with padding
	img = resizeWithPadding(img, targetWidth, targetHeight)

	return img
}

// CompressForWeb compresses an image for web delivery.
// Uses PNG (lossless) as Go stdlib doesn't include a WebP encoder.
// Falls back to JPEG for smaller sizes when appropriate.
// Returns compressed bytes.
//
// Mirrors Python: compress_for_web() in image_processor.py
func (p *ImageProcessor) CompressForWeb(img image.Image) ([]byte, error) {
	// Try PNG first (lossless, widely supported)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("compress: png encode: %w", err)
	}
	if buf.Len() <= maxCompressedSize {
		return buf.Bytes(), nil
	}

	// Try JPEG with reducing quality for smaller output
	for quality := 85; quality >= 10; quality -= 15 {
		buf.Reset()
		if err := encodeJPEG(&buf, img, quality); err != nil {
			continue
		}
		if buf.Len() <= maxCompressedSize {
			return buf.Bytes(), nil
		}
	}

	// Final attempt at minimum quality
	buf.Reset()
	if err := encodeJPEG(&buf, img, 10); err != nil {
		return nil, fmt.Errorf("compress: jpeg encode: %w", err)
	}
	return buf.Bytes(), nil
}

// ComputePHash computes a perceptual hash of an image for duplicate detection.
// Uses Average Hash (aHash): resize to 8x8 grayscale, compare each pixel to mean.
// Returns the hash as a 16-char hex string.
//
// Mirrors Python: compute_phash() in image_processor.py
func (p *ImageProcessor) ComputePHash(img image.Image) string {
	// Average Hash (aHash):
	// 1. Resize to 8x8
	// 2. Convert to grayscale
	// 3. Compute mean pixel value
	// 4. Each bit = 1 if pixel > mean, 0 otherwise

	small := resizeGrayscale(img, phashSize, phashSize)

	var pixels [phashSize * phashSize]uint8
	var sum float64
	for y := 0; y < phashSize; y++ {
		for x := 0; x < phashSize; x++ {
			g := color.GrayModel.Convert(small.At(x, y)).(color.Gray)
			pixels[y*phashSize+x] = g.Y
			sum += float64(g.Y)
		}
	}

	mean := sum / float64(phashSize*phashSize)

	// Build 64-bit hash
	var hash uint64
	for i := 0; i < phashSize*phashSize; i++ {
		if float64(pixels[i]) > mean {
			hash |= 1 << uint(i)
		}
	}

	return fmt.Sprintf("%016x", hash)
}

// ComputeFileHash computes a SHA-256 hash of raw bytes.
// Used as fallback when phash is not possible.
func (p *ImageProcessor) ComputeFileHash(raw []byte) string {
	h := sha256.Sum256(raw)
	return fmt.Sprintf("%x", h[:8]) // first 16 hex chars
}

// ---------------------------------------------------------------------------
// Internal: URL validation (SSRF prevention)
// ---------------------------------------------------------------------------

func validateImageURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return &ImageValidationError{Reason: fmt.Sprintf("invalid URL: %v", err)}
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return &ImageValidationError{
			Reason: fmt.Sprintf("disallowed URL scheme: %s (only http/https allowed)", parsed.Scheme),
		}
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return &ImageValidationError{Reason: "missing hostname in URL"}
	}

	// Check against allowed hosts
	allowed := false
	for host := range allowedImageHosts {
		if hostname == host || strings.HasSuffix(hostname, "."+host) {
			allowed = true
			break
		}
	}

	if !allowed {
		return &ImageValidationError{
			Reason: fmt.Sprintf("disallowed image host: %s", hostname),
		}
	}

	return nil
}

// ---------------------------------------------------------------------------
// Internal: Image fetching
// ---------------------------------------------------------------------------

func (p *ImageProcessor) fetchImage(ctx context.Context, imageURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", imageURL, nil)
	if err != nil {
		return nil, "", &ImageValidationError{
			Reason: fmt.Sprintf("failed to create request: %v", err),
		}
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", &ImageValidationError{
			Reason: fmt.Sprintf("network error fetching image: %v", err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &ImageValidationError{
			Reason: fmt.Sprintf("HTTP %d fetching image", resp.StatusCode),
		}
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, p.maxSize+1024)) // allow a bit of overflow for size check
	if err != nil {
		return nil, "", &ImageValidationError{
			Reason: fmt.Sprintf("failed to read response body: %v", err),
		}
	}

	contentType := strings.ToLower(resp.Header.Get("content-type"))
	// Strip parameters (e.g., "image/jpeg; charset=utf-8")
	if idx := strings.Index(contentType, ";"); idx != -1 {
		contentType = strings.TrimSpace(contentType[:idx])
	}

	return raw, contentType, nil
}

// ---------------------------------------------------------------------------
// Internal: Preprocessing helpers
// ---------------------------------------------------------------------------

// cropToUpperBody crops the image to focus on the upper body (top ~60%).
// Mirrors Python: _crop_to_upper_body()
func cropToUpperBody(img image.Image) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w == 0 || h == 0 {
		return img
	}

	targetRatio := 3.0 / 4.0
	currentRatio := float64(w) / float64(h)

	// If already close to 3:4, return as-is
	if math.Abs(currentRatio-targetRatio) < 0.05 {
		return img
	}

	// Crop to top 60% (upper body focus)
	cropH := int(float64(h) * 0.6)
	if cropH > h {
		cropH = h
	}

	cropped := cropImage(img, 0, 0, w, cropH)

	return resizeWithPadding(cropped, targetWidth, targetHeight)
}

// cropToContent auto-crops to the non-white bounding box of an image.
// Mirrors Python: _crop_to_content()
func cropToContent(img image.Image) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w == 0 || h == 0 {
		return img
	}

	// Find the bounding box of non-white content
	minX, minY := w, h
	maxX, maxY := 0, 0
	found := false

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.GrayModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.Gray)
			if c.Y < 240 { // non-white pixel
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
				found = true
			}
		}
	}

	if !found {
		return img
	}

	// Add 5% padding around content
	contentW := maxX - minX + 1
	contentH := maxY - minY + 1
	padX := int(float64(contentW) * 0.05)
	padY := int(float64(contentH) * 0.05)

	minX = maxInt(0, minX-padX)
	minY = maxInt(0, minY-padY)
	maxX = minInt(w-1, maxX+padX)
	maxY = minInt(h-1, maxY+padY)

	return cropImage(img, minX, minY, maxX-minX+1, maxY-minY+1)
}

// resizeWithPadding resizes an image to fit within target dimensions while
// preserving aspect ratio, then center-pads to exactly the target size.
// Mirrors Python: _resize_with_padding()
func resizeWithPadding(img image.Image, targetW, targetH int) image.Image {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	if w == 0 || h == 0 {
		return img
	}

	// Calculate scale to fit
	scaleW := float64(targetW) / float64(w)
	scaleH := float64(targetH) / float64(h)
	scale := math.Min(scaleW, scaleH)

	newW := int(float64(w) * scale)
	newH := int(float64(h) * scale)

	// Resize using nearest neighbor (simple, acceptable for preprocessing)
	resized := resizeImage(img, newW, newH)

	// Create padded canvas
	canvas := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	// Fill with white background
	for y := 0; y < targetH; y++ {
		for x := 0; x < targetW; x++ {
			canvas.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}

	// Center paste
	pasteX := (targetW - newW) / 2
	pasteY := (targetH - newH) / 2

	for y := 0; y < newH; y++ {
		for x := 0; x < newW; x++ {
			canvas.Set(pasteX+x, pasteY+y, resized.At(x, y))
		}
	}

	return canvas
}

// resizeGrayscale resizes an image to the given dimensions and converts to grayscale.
func resizeGrayscale(img image.Image, targetW, targetH int) image.Image {
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()

	if srcW == 0 || srcH == 0 {
		return img
	}

	result := image.NewGray(image.Rect(0, 0, targetW, targetH))

	for y := 0; y < targetH; y++ {
		for x := 0; x < targetW; x++ {
			srcX := bounds.Min.X + x*srcW/targetW
			srcY := bounds.Min.Y + y*srcH/targetH
			result.Set(x, y, color.GrayModel.Convert(img.At(srcX, srcY)))
		}
	}

	return result
}

// resizeImage resizes an image to the given dimensions using bilinear interpolation.
func resizeImage(img image.Image, targetW, targetH int) image.Image {
	bounds := img.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()

	if srcW == 0 || srcH == 0 || targetW == 0 || targetH == 0 {
		return img
	}

	result := image.NewRGBA(image.Rect(0, 0, targetW, targetH))

	for y := 0; y < targetH; y++ {
		for x := 0; x < targetW; x++ {
			srcX := bounds.Min.X + x*srcW/targetW
			srcY := bounds.Min.Y + y*srcH/targetH
			result.Set(x, y, img.At(srcX, srcY))
		}
	}

	return result
}

// cropImage extracts a sub-image from the given coordinates.
func cropImage(img image.Image, x, y, w, h int) image.Image {
	bounds := img.Bounds()
	return img.(interface {
		SubImage(r image.Rectangle) image.Image
	}).SubImage(image.Rect(
		bounds.Min.X+x, bounds.Min.Y+y,
		bounds.Min.X+x+w, bounds.Min.Y+y+h,
	))
}

// ---------------------------------------------------------------------------
// Internal: PNG/JPEG encoding
// ---------------------------------------------------------------------------

func encodeJPEG(w io.Writer, img image.Image, quality int) error {
	return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
}

// ---------------------------------------------------------------------------
// Internal: URL helpers
// ---------------------------------------------------------------------------

func getExtension(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	path := parsed.Path
	// Find last dot
	idx := strings.LastIndex(path, ".")
	if idx < 0 {
		return ""
	}
	// Find extension end (before query params)
	ext := path[idx:]
	if qi := strings.Index(ext, "?"); qi >= 0 {
		ext = ext[:qi]
	}
	return strings.ToLower(ext)
}

// ---------------------------------------------------------------------------
// Internal: Math helpers
// ---------------------------------------------------------------------------

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
