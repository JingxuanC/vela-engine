package tryon

import (
	"image"
	"image/color"
	"math"
)

// QualityResult holds image quality assessment metrics.
type QualityResult struct {
	PSNR        float64 `json:"psnr"`
	SSIM        float64 `json:"ssim"`
	BlurScore   float64 `json:"blur_score"`
	ArtifactPct float64 `json:"artifact_pct"`
	Passes      bool    `json:"passes"`
}

// QualityThresholds defines the minimum acceptable quality levels.
type QualityThresholds struct {
	MinPSNR        float64
	MinSSIM        float64
	MaxBlurScore   float64 // Lower = sharper
	MaxArtifactPct float64
}

// DefaultQualityThresholds are sensible defaults for try-on output.
var DefaultQualityThresholds = QualityThresholds{
	MinPSNR:        25.0,
	MinSSIM:        0.85,
	MaxBlurScore:   150.0,
	MaxArtifactPct: 0.05,
}

// CheckQuality runs all quality checks on an image and returns the results.
func CheckQuality(img image.Image) QualityResult {
	bounds := img.Bounds()
	if bounds.Dx() == 0 || bounds.Dy() == 0 {
		return QualityResult{Passes: false}
	}

	blurScore := detectBlur(img)
	artifactPct := detectArtifacts(img)

	result := QualityResult{
		PSNR:        30.0, // Default; requires reference image
		SSIM:        0.92, // Default; requires reference image
		BlurScore:   blurScore,
		ArtifactPct: artifactPct,
	}

	t := DefaultQualityThresholds
	result.Passes = result.BlurScore <= t.MaxBlurScore &&
		result.ArtifactPct <= t.MaxArtifactPct

	return result
}

// CheckQualityWithReference runs full quality checks including PSNR and SSIM
// against a reference image.
func CheckQualityWithReference(img, reference image.Image) QualityResult {
	result := CheckQuality(img)
	result.PSNR = computePSNR(img, reference)
	result.SSIM = computeSSIM(img, reference)

	t := DefaultQualityThresholds
	result.Passes = result.PSNR >= t.MinPSNR &&
		result.SSIM >= t.MinSSIM &&
		result.BlurScore <= t.MaxBlurScore &&
		result.ArtifactPct <= t.MaxArtifactPct

	return result
}

// detectBlur uses Laplacian variance to estimate image sharpness.
// Lower variance = more blurry.
func detectBlur(img image.Image) float64 {
	bounds := img.Bounds()
	if bounds.Dx() < 3 || bounds.Dy() < 3 {
		return 0
	}

	// Sample at stride for performance (check every 4th pixel)
	stride := 4
	var sum, sumSq float64
	var count int

	laplacian := [3][3]float64{
		{0, 1, 0},
		{1, -4, 1},
		{0, 1, 0},
	}

	for y := bounds.Min.Y + 1; y < bounds.Max.Y-1; y += stride {
		for x := bounds.Min.X + 1; x < bounds.Max.X-1; x += stride {
			var lapVal float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					c := color.GrayModel.Convert(img.At(x+dx, y+dy)).(color.Gray)
					lapVal += float64(c.Y) * laplacian[dy+1][dx+1]
				}
			}
			sum += lapVal
			sumSq += lapVal * lapVal
			count++
		}
	}

	if count == 0 {
		return 0
	}

	mean := sum / float64(count)
	variance := sumSq/float64(count) - mean*mean
	return variance
}

// detectArtifacts estimates the percentage of pixels that may contain artifacts
// by detecting unusually high local variance.
func detectArtifacts(img image.Image) float64 {
	bounds := img.Bounds()
	if bounds.Dx() < 5 || bounds.Dy() < 5 {
		return 0
	}

	stride := 5
	var artifactCount, totalCount int

	for y := bounds.Min.Y + 2; y < bounds.Max.Y-2; y += stride {
		for x := bounds.Min.X + 2; x < bounds.Max.X-2; x += stride {
			localVar := computeLocalVariance(img, x, y, 2)
			if localVar > 2000 { // High variance = potential artifact
				artifactCount++
			}
			totalCount++
		}
	}

	if totalCount == 0 {
		return 0
	}
	return float64(artifactCount) / float64(totalCount)
}

// computeLocalVariance calculates local pixel intensity variance in a window.
func computeLocalVariance(img image.Image, cx, cy, radius int) float64 {
	var sum, sumSq float64
	var count int

	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			c := color.GrayModel.Convert(img.At(cx+dx, cy+dy)).(color.Gray)
			val := float64(c.Y)
			sum += val
			sumSq += val * val
			count++
		}
	}

	if count == 0 {
		return 0
	}

	mean := sum / float64(count)
	variance := sumSq/float64(count) - mean*mean
	return variance
}

// computePSNR calculates Peak Signal-to-Noise Ratio between two images.
func computePSNR(img1, img2 image.Image) float64 {
	b1 := img1.Bounds()
	b2 := img2.Bounds()

	w := minInt(b1.Dx(), b2.Dx())
	h := minInt(b1.Dy(), b2.Dy())

	if w == 0 || h == 0 {
		return 0
	}

	var mse float64
	var count int

	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c1 := color.GrayModel.Convert(img1.At(b1.Min.X+x, b1.Min.Y+y)).(color.Gray)
			c2 := color.GrayModel.Convert(img2.At(b2.Min.X+x, b2.Min.Y+y)).(color.Gray)
			diff := float64(c1.Y) - float64(c2.Y)
			mse += diff * diff
			count++
		}
	}

	if count == 0 || mse == 0 {
		return 100.0 // Identical images
	}

	mse /= float64(count)

	// PSNR = 20 * log10(MAX) - 10 * log10(MSE)
	const maxVal = 255.0
	psnr := 20*math.Log10(maxVal) - 10*math.Log10(mse)
	return psnr
}

// computeSSIM calculates Structural Similarity Index between two images.
// Simplified single-scale implementation on luminance channel.
func computeSSIM(img1, img2 image.Image) float64 {
	b1 := img1.Bounds()
	b2 := img2.Bounds()

	w := minInt(b1.Dx(), b2.Dx())
	h := minInt(b1.Dy(), b2.Dy())

	if w < 8 || h < 8 {
		return 0
	}

	// Constants to stabilize division
	const (
		K1 = 0.01
		K2 = 0.03
		L  = 255.0
	)
	C1 := (K1 * L) * (K1 * L)
	C2 := (K2 * L) * (K2 * L)

	// Use 8x8 blocks for SSIM calculation
	blockSize := 8
	var totalSSIM float64
	var blockCount int

	for by := 0; by+blockSize <= h; by += blockSize {
		for bx := 0; bx+blockSize <= w; bx += blockSize {
			// Extract pixel intensities from both images for this block
			n := blockSize * blockSize
			var sum1, sum2 float64
			var sumsq1, sumsq2, sum12 float64

			for dy := 0; dy < blockSize; dy++ {
				for dx := 0; dx < blockSize; dx++ {
					v1 := float64(color.GrayModel.Convert(img1.At(b1.Min.X+bx+dx, b1.Min.Y+by+dy)).(color.Gray).Y)
					v2 := float64(color.GrayModel.Convert(img2.At(b2.Min.X+bx+dx, b2.Min.Y+by+dy)).(color.Gray).Y)
					sum1 += v1
					sum2 += v2
					sumsq1 += v1 * v1
					sumsq2 += v2 * v2
					sum12 += v1 * v2
				}
			}

			// Means
			mu1 := sum1 / float64(n)
			mu2 := sum2 / float64(n)

			// Variances and covariance
			sigma1sq := sumsq1/float64(n) - mu1*mu1
			sigma2sq := sumsq2/float64(n) - mu2*mu2
			sigma12 := sum12/float64(n) - mu1*mu2

			if sigma1sq < 0 {
				sigma1sq = 0
			}
			if sigma2sq < 0 {
				sigma2sq = 0
			}

			// SSIM for this block
			numerator := (2*mu1*mu2 + C1) * (2*sigma12 + C2)
			denominator := (mu1*mu1 + mu2*mu2 + C1) * (sigma1sq + sigma2sq + C2)

			if denominator > 0 {
				totalSSIM += numerator / denominator
			} else {
				totalSSIM += 1.0
			}
			blockCount++
		}
	}

	if blockCount == 0 {
		return 0
	}

	return totalSSIM / float64(blockCount)
}
