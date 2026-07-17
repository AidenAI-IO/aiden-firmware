package agent

import (
	"context"
	"strings"
	"testing"
)

func TestImageDiffPNGDetection(t *testing.T) {
	tool := &ImageDiffTool{}
	
	// PNG data (1x1 red and blue pixels)
	pngInput := `{"before":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==","after":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="}`
	
	result, err := tool.Call(context.Background(), pngInput)
	if err != nil {
		t.Fatalf("Call returned error: %v", err)
	}
	
	// Should contain our custom PNG error message
	if !strings.Contains(result, "PNG format") {
		t.Errorf("Expected PNG format error message, got: %s", result)
	}
	
	if !strings.Contains(result, "requires JPEG") {
		t.Errorf("Expected 'requires JPEG' in error message, got: %s", result)
	}
	
	if !strings.Contains(result, "screenshot tool results") {
		t.Errorf("Expected guidance about screenshot tool, got: %s", result)
	}
	
	t.Logf("PNG detection working correctly. Error message: %s", result)
}
