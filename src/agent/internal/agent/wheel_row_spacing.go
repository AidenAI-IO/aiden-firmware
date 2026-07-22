package agent

import (
	"bytes"
	"image"
	"image/jpeg"
	"math"
	"sort"
	"time"
)

const (
	wheelRowSpacingMinNormalized  = 24.0
	wheelRowSpacingMaxNormalized  = 80.0
	wheelRowSpacingMinCorrelation = 0.28
	wheelRowSpacingMinProminence  = 0.12
	wheelRowSpacingMinConfidence  = 0.50
)

type wheelRowSpacingMeasurement struct {
	Normalized  float64
	Pixels      float64
	Confidence  float64
	Correlation float64
}

func (s *screenState) UpdateScreenshot(jpegData []byte, width, height int) {
	if s == nil || len(jpegData) == 0 || width <= 1 || height <= 1 {
		return
	}
	copyData := append([]byte(nil), jpegData...)
	s.mu.Lock()
	s.screenshotJPEG = copyData
	s.screenshotWidth = width
	s.screenshotHeight = height
	s.screenshotUpdatedAt = time.Now()
	s.mu.Unlock()
}

func (s *screenState) LatestScreenshot(maxAge time.Duration) (jpegData []byte, width, height int, age time.Duration, ok bool) {
	if s == nil {
		return nil, 0, 0, 0, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.screenshotJPEG) == 0 || s.screenshotWidth <= 1 || s.screenshotHeight <= 1 || s.screenshotUpdatedAt.IsZero() {
		return nil, 0, 0, 0, false
	}
	age = time.Since(s.screenshotUpdatedAt)
	if maxAge > 0 && age >= maxAge {
		return nil, 0, 0, age, false
	}
	return append([]byte(nil), s.screenshotJPEG...), s.screenshotWidth, s.screenshotHeight, age, true
}

func measureWheelRowSpacingJPEG(jpegData []byte, columnX, centerY float64) (wheelRowSpacingMeasurement, bool) {
	if len(jpegData) == 0 || columnX < 0 || columnX > 1000 || centerY < 0 || centerY > 1000 {
		return wheelRowSpacingMeasurement{}, false
	}
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return wheelRowSpacingMeasurement{}, false
	}
	return measureWheelRowSpacingImage(img, columnX, centerY)
}

func measureWheelRowSpacingImage(img image.Image, columnX, centerY float64) (wheelRowSpacingMeasurement, bool) {
	if img == nil {
		return wheelRowSpacingMeasurement{}, false
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width < 80 || height < 200 {
		return wheelRowSpacingMeasurement{}, false
	}

	centerXPixel := bounds.Min.X + int(math.Round(columnX*float64(width-1)/1000.0))
	centerYPixel := bounds.Min.Y + int(math.Round(centerY*float64(height-1)/1000.0))
	halfWidth := clampInt(int(math.Round(float64(width)*0.075)), 24, 48)
	halfHeight := clampInt(int(math.Round(float64(height)*0.14)), 100, 180)
	x0 := max(bounds.Min.X+1, centerXPixel-halfWidth)
	x1 := min(bounds.Max.X-2, centerXPixel+halfWidth)
	y0 := max(bounds.Min.Y, centerYPixel-halfHeight)
	y1 := min(bounds.Max.Y-1, centerYPixel+halfHeight)
	if x1-x0 < 20 || y1-y0 < 100 {
		return wheelRowSpacingMeasurement{}, false
	}

	rowEnergy := make([]float64, y1-y0+1)
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			left := wheelPixelLuminance(img.At(x-1, y))
			right := wheelPixelLuminance(img.At(x+1, y))
			rowEnergy[y-y0] += math.Abs(right - left)
		}
	}

	smoothRadius := clampInt(int(math.Round(float64(height)/350.0)), 2, 4)
	smoothed := smoothWheelRowEnergy(rowEnergy, smoothRadius)
	mean := 0.0
	for _, value := range smoothed {
		mean += value
	}
	mean /= float64(len(smoothed))
	variance := 0.0
	for index := range smoothed {
		smoothed[index] -= mean
		variance += smoothed[index] * smoothed[index]
	}
	if variance <= 1e-6 {
		return wheelRowSpacingMeasurement{}, false
	}

	minLag := max(12, int(math.Round(wheelRowSpacingMinNormalized*float64(height-1)/1000.0)))
	maxLag := min(len(smoothed)/3, int(math.Round(wheelRowSpacingMaxNormalized*float64(height-1)/1000.0)))
	if maxLag <= minLag+2 {
		return wheelRowSpacingMeasurement{}, false
	}

	correlations := make([]float64, maxLag-minLag+1)
	for lag := minLag; lag <= maxLag; lag++ {
		correlation := normalizedWheelAutocorrelation(smoothed, lag)
		index := lag - minLag
		correlations[index] = correlation
	}

	chosenIndex, confidence, ok := selectWheelRowSpacingPeak(correlations)
	if !ok {
		return wheelRowSpacingMeasurement{}, false
	}
	chosenLag := minLag + chosenIndex
	chosenCorrelation := correlations[chosenIndex]

	return wheelRowSpacingMeasurement{
		Normalized:  float64(chosenLag) / float64(height-1) * 1000.0,
		Pixels:      float64(chosenLag),
		Confidence:  confidence,
		Correlation: chosenCorrelation,
	}, true
}

func selectWheelRowSpacingPeak(correlations []float64) (chosenIndex int, confidence float64, ok bool) {
	if len(correlations) == 0 {
		return 0, 0, false
	}
	bestIndex := 0
	bestCorrelation := correlations[0]
	for index := 1; index < len(correlations); index++ {
		if correlations[index] > bestCorrelation {
			bestCorrelation = correlations[index]
			bestIndex = index
		}
	}

	medianValues := append([]float64(nil), correlations...)
	sort.Float64s(medianValues)
	medianCorrelation := medianValues[len(medianValues)/2]
	if bestCorrelation < wheelRowSpacingMinCorrelation || bestCorrelation-medianCorrelation < wheelRowSpacingMinProminence {
		return 0, 0, false
	}

	chosenIndex = bestIndex
	strongThreshold := bestCorrelation * 0.90
	for index := 1; index+1 < len(correlations); index++ {
		if correlations[index] >= strongThreshold && correlations[index] >= correlations[index-1] && correlations[index] >= correlations[index+1] {
			chosenIndex = index
			break
		}
	}

	chosenCorrelation := correlations[chosenIndex]
	prominence := chosenCorrelation - medianCorrelation
	confidence = 0.5*clampFloat(chosenCorrelation/0.50, 0, 1) +
		0.5*clampFloat(prominence/0.30, 0, 1)
	if chosenCorrelation < wheelRowSpacingMinCorrelation || prominence < wheelRowSpacingMinProminence || confidence < wheelRowSpacingMinConfidence {
		return 0, confidence, false
	}
	return chosenIndex, confidence, true
}

func smoothWheelRowEnergy(values []float64, radius int) []float64 {
	if len(values) == 0 || radius <= 0 {
		return append([]float64(nil), values...)
	}
	prefix := make([]float64, len(values)+1)
	for index, value := range values {
		prefix[index+1] = prefix[index] + value
	}
	smoothed := make([]float64, len(values))
	for index := range values {
		start := max(0, index-radius)
		end := min(len(values)-1, index+radius)
		smoothed[index] = (prefix[end+1] - prefix[start]) / float64(end-start+1)
	}
	return smoothed
}

func normalizedWheelAutocorrelation(values []float64, lag int) float64 {
	if lag <= 0 || lag >= len(values) {
		return 0
	}
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for index := 0; index+lag < len(values); index++ {
		left := values[index]
		right := values[index+lag]
		dot += left * right
		leftNorm += left * left
		rightNorm += right * right
	}
	if leftNorm <= 1e-9 || rightNorm <= 1e-9 {
		return 0
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func wheelPixelLuminance(pixel interface{ RGBA() (r, g, b, a uint32) }) float64 {
	r, g, b, _ := pixel.RGBA()
	return (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)) / 257.0
}
