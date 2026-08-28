package agent

import (
	"context"
	"strings"
	"testing"
)

func TestImageDiffUnsupportedImageData(t *testing.T) {
	tool := &ImageDiffTool{}

	t.Run("unsupported before image", func(t *testing.T) {
		// PNG data (1x1 red and blue pixels)
		pngInput := `{"before":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==","after":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="}`

		result, err := tool.Call(context.Background(), pngInput)
		if err != nil {
			t.Fatalf("Call returned error: %v", err)
		}

		if !strings.Contains(result, "before contains unsupported image data") {
			t.Errorf("Expected unsupported before image error message, got: %s", result)
		}

		if !strings.Contains(result, "'data' field from screenshot tool results") {
			t.Errorf("Expected specific guidance about 'data' field, got: %s", result)
		}

		t.Logf("Unsupported image detection working correctly. Error message: %s", result)
	})

	t.Run("unsupported after image", func(t *testing.T) {
		// 1x1 red JPEG (valid before) + PNG (invalid after)
		mixedInput := `{"before":"/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAn/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCwABmQ/9k=","after":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="}`

		result, err := tool.Call(context.Background(), mixedInput)
		if err != nil {
			t.Fatalf("Call returned error: %v", err)
		}

		if !strings.Contains(result, "after contains unsupported image data") {
			t.Errorf("Expected unsupported after image error message, got: %s", result)
		}

		if !strings.Contains(result, "'data' field from screenshot tool results") {
			t.Errorf("Expected specific guidance about 'data' field, got: %s", result)
		}

		t.Logf("Unsupported image detection working correctly for 'after'. Error message: %s", result)
	})
}
