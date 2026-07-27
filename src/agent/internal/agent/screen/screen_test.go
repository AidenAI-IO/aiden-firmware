package screen

import (
	"bytes"
	"testing"
)

func TestLatestScreenshotPairTracksTwoMostRecentUpdates(t *testing.T) {
	state := &ScreenState{}
	first := []byte("first-jpeg")
	second := []byte("second-jpeg")

	state.UpdateScreenshotWithID(101, first, 10, 20)
	if _, _, ok := state.LatestScreenshotPair(); ok {
		t.Fatal("LatestScreenshotPair succeeded with only one screenshot")
	}

	state.UpdateScreenshotWithID(102, second, 10, 20)
	before, after, ok := state.LatestScreenshotPair()
	if !ok {
		t.Fatal("LatestScreenshotPair failed with two screenshots")
	}
	if before.ID != 101 || after.ID != 102 || !bytes.Equal(before.JPEG, first) || !bytes.Equal(after.JPEG, second) {
		t.Fatalf("pair = %#v -> %#v, want IDs 101 -> 102", before, after)
	}

	before.JPEG[0] = 'X'
	after.JPEG[0] = 'Y'
	storedBefore, storedAfter, ok := state.LatestScreenshotPair()
	if !ok || !bytes.Equal(storedBefore.JPEG, first) || !bytes.Equal(storedAfter.JPEG, second) {
		t.Fatal("LatestScreenshotPair did not return defensive copies")
	}
}

func TestLatestScreenshotPairKeepsSeparateIdenticalObservations(t *testing.T) {
	state := &ScreenState{}
	jpegData := []byte("same-jpeg")

	state.UpdateScreenshotWithID(201, jpegData, 10, 20)
	state.UpdateScreenshotWithID(202, jpegData, 10, 20)

	before, after, ok := state.LatestScreenshotPair()
	if !ok || before.ID != 201 || after.ID != 202 || !bytes.Equal(before.JPEG, jpegData) || !bytes.Equal(after.JPEG, jpegData) {
		t.Fatalf("identical observations were not retained: %#v -> %#v, ok=%v", before, after, ok)
	}
}
