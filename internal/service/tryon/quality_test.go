package tryon

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
)

// createTestImage creates a simple test image of the given size with a uniform color.
func createTestImage(w, h int, c color.Color) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	return img
}

func TestCheckQuality_Passes(t *testing.T) {
	img := createTestImage(100, 100, color.Gray{Y: 128})
	result := CheckQuality(img)
	assert.True(t, result.Passes, "uniform image should pass quality check")
	assert.GreaterOrEqual(t, result.BlurScore, 0.0)
}

func TestCheckQuality_EmptyImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 0, 0))
	result := CheckQuality(img)
	assert.False(t, result.Passes)
}

func TestCheckQualityWithReference_Identical(t *testing.T) {
	img := createTestImage(100, 100, color.Gray{Y: 100})
	ref := createTestImage(100, 100, color.Gray{Y: 100})

	result := CheckQualityWithReference(img, ref)
	assert.True(t, result.Passes)
	assert.Greater(t, result.PSNR, 50.0, "identical images should have high PSNR")
	assert.Greater(t, result.SSIM, 0.99, "identical images should have high SSIM")
}

func TestCheckQualityWithReference_Different(t *testing.T) {
	img := createTestImage(100, 100, color.Gray{Y: 200})
	ref := createTestImage(100, 100, color.Gray{Y: 50})

	result := CheckQualityWithReference(img, ref)
	assert.Less(t, result.PSNR, 30.0, "different images should have low PSNR")
}

func TestCheckQualityWithReference_DifferentSizes(t *testing.T) {
	img := createTestImage(50, 50, color.Gray{Y: 128})
	ref := createTestImage(100, 100, color.Gray{Y: 128})

	result := CheckQualityWithReference(img, ref)
	assert.Greater(t, result.PSNR, 50.0, "same color different sizes: PSNR should be high")
}

func TestQualityThresholds(t *testing.T) {
	assert.Greater(t, DefaultQualityThresholds.MinPSNR, 0.0)
	assert.Greater(t, DefaultQualityThresholds.MinSSIM, 0.0)
	assert.Greater(t, DefaultQualityThresholds.MaxBlurScore, 0.0)
}
