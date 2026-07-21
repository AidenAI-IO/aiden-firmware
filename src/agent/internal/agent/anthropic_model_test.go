package agent

import "testing"

func TestIsAnthropicModel(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"anthropic", "claude-sonnet-4", true},
		{"Anthropic", "anything", true},
		{"openrouter", "anthropic/claude-sonnet-4", true},
		{"openrouter", "openai/gpt-4o", false},
		{"openai", "gpt-4o", false},
		{"", "", false},
		{"  anthropic  ", "", true},
		{"openrouter", "  Anthropic/Claude  ", true},
	}
	for _, tc := range cases {
		got := IsAnthropicModel(tc.provider, tc.model)
		if got != tc.want {
			t.Fatalf("IsAnthropicModel(%q, %q) = %v, want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}
