package agent

import (
	"image"
	"math"
	"strings"
	"testing"
)

func TestImageDiffRegionRejectsInvalidValues(t *testing.T) {
	full := image.Rect(0, 0, 100, 200)
	tests := []imageDiffRegion{
		{X: -100, Y: 0, W: 500, H: 500},
		{X: 0, Y: 1100, W: 500, H: 500},
		{X: 0, Y: 0, W: 0, H: 500},
		{X: 0, Y: 0, W: 500, H: -100},
	}

	for _, tt := range tests {
		_, err := tt.toPixelRect(full)
		if err == nil {
			t.Fatalf("toPixelRect(%+v) succeeded, want error", tt)
		}
	}
}

func TestImageDiffRegionClampsToImageBounds(t *testing.T) {
	full := image.Rect(10, 20, 110, 220)
	got, err := (&imageDiffRegion{X: 800, Y: 750, W: 500, H: 500}).toPixelRect(full)
	if err != nil {
		t.Fatalf("toPixelRect returned error: %v", err)
	}
	want := image.Rect(90, 170, 110, 220)
	if got != want {
		t.Fatalf("toPixelRect = %v, want %v", got, want)
	}
}

func TestImageDiffRegionRejectsNonFiniteValues(t *testing.T) {
	full := image.Rect(0, 0, 100, 100)
	_, err := (&imageDiffRegion{X: 0, Y: 0, W: 500, H: math.NaN()}).toPixelRect(full)
	if err == nil {
		t.Fatal("expected error for non-finite region")
	}
	if !strings.Contains(err.Error(), "finite normalized") {
		t.Fatalf("unexpected error: %v", err)
	}
}
