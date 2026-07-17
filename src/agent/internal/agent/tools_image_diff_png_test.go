package agent

import (
	"context"
	"strings"
	"testing"
)

func TestImageDiffNonJPEGDetection(t *testing.T) {
	tool := &ImageDiffTool{}

	t.Run("before is PNG", func(t *testing.T) {
		// PNG data (1x1 red and blue pixels)
		pngInput := `{"before":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==","after":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="}`

		result, err := tool.Call(context.Background(), pngInput)
		if err != nil {
			t.Fatalf("Call returned error: %v", err)
		}

		// Should contain error message indicating non-JPEG format
		if !strings.Contains(result, "before is not JPEG format") {
			t.Errorf("Expected 'before is not JPEG format' error message, got: %s", result)
		}

		if !strings.Contains(result, "only supports JPEG") {
			t.Errorf("Expected 'only supports JPEG' in error message, got: %s", result)
		}

		if !strings.Contains(result, "screenshot tool results") {
			t.Errorf("Expected guidance about screenshot tool, got: %s", result)
		}

		t.Logf("Non-JPEG detection working correctly. Error message: %s", result)
	})

	t.Run("after is PNG", func(t *testing.T) {
		// 1x1 red JPEG (valid before) + PNG (invalid after)
		mixedInput := `{"before":"/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAAgGBgcGBQgHBwcJCQgKDBQNDAsLDBkSEw8UHRofHh0aHBwgJC4nICIsIxwcKDcpLDAxNDQ0Hyc5PTgyPC4zNDL/2wBDAQkJCQwLDBgNDRgyIRwhMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjIyMjL/wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAn/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/8QAFQEBAQAAAAAAAAAAAAAAAAAAAAX/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIRAxEAPwCwABmQ/9k=","after":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8/5+hHgAHggJ/PchI7wAAAABJRU5ErkJggg=="}`

		result, err := tool.Call(context.Background(), mixedInput)
		if err != nil {
			t.Fatalf("Call returned error: %v", err)
		}

		// Should contain error message indicating non-JPEG format for "after"
		if !strings.Contains(result, "after is not JPEG format") {
			t.Errorf("Expected 'after is not JPEG format' error message, got: %s", result)
		}

		if !strings.Contains(result, "only supports JPEG") {
			t.Errorf("Expected 'only supports JPEG' in error message, got: %s", result)
		}

		if !strings.Contains(result, "screenshot tool results") {
			t.Errorf("Expected guidance about screenshot tool, got: %s", result)
		}

		t.Logf("Non-JPEG detection working correctly for 'after'. Error message: %s", result)
	})
}
