package agent

import "strings"

func IsAnthropicModel(provider, model string) bool {
	if strings.EqualFold(strings.TrimSpace(provider), "anthropic") {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "anthropic/")
}
