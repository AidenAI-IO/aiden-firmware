package messages

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
)

// ApplyScreenshotDisplayMarker returns a JPEG copy with the requested-coordinate
// marker rendered for model display. Callers should persist jpegData separately.
func ApplyScreenshotDisplayMarker(jpegData []byte, marker ScreenshotDisplayMarker) ([]byte, error) {
	if math.IsNaN(marker.X) || math.IsInf(marker.X, 0) || marker.X < 0 || marker.X > 1000 ||
		math.IsNaN(marker.Y) || math.IsInf(marker.Y, 0) || marker.Y < 0 || marker.Y > 1000 {
		return nil, fmt.Errorf("invalid screenshot marker coordinate (%.3f, %.3f)", marker.X, marker.Y)
	}
	img, err := jpeg.Decode(bytes.NewReader(jpegData))
	if err != nil {
		return nil, fmt.Errorf("decode screenshot jpeg: %w", err)
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid screenshot dimensions %dx%d", width, height)
	}

	marked := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(marked, marked.Bounds(), img, bounds.Min, draw.Src)
	x := int(math.Round(marker.X / 1000 * float64(max(width-1, 0))))
	y := int(math.Round(marker.Y / 1000 * float64(max(height-1, 0))))
	drawTouchMarker(marked, x, y)

	var output bytes.Buffer
	if err := jpeg.Encode(&output, marked, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("encode marked screenshot jpeg: %w", err)
	}
	return output.Bytes(), nil
}

func drawTouchMarker(img *image.RGBA, x, y int) {
	if img == nil {
		return
	}
	minDimension := min(img.Bounds().Dx(), img.Bounds().Dy())
	radius := max(10, min(24, minDimension/28))
	outer := radius + 3
	drawMarkerRing(img, x, y, outer, 3, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	drawMarkerRing(img, x, y, radius, 3, color.RGBA{R: 255, G: 32, B: 32, A: 255})
}

func drawMarkerRing(img *image.RGBA, centerX, centerY, radius, thickness int, c color.Color) {
	outerSquared := radius * radius
	innerRadius := max(radius-thickness, 0)
	innerSquared := innerRadius * innerRadius
	for y := centerY - radius; y <= centerY+radius; y++ {
		for x := centerX - radius; x <= centerX+radius; x++ {
			dx := x - centerX
			dy := y - centerY
			distanceSquared := dx*dx + dy*dy
			if distanceSquared <= outerSquared && distanceSquared >= innerSquared && image.Pt(x, y).In(img.Bounds()) {
				img.Set(x, y, c)
			}
		}
	}
}
