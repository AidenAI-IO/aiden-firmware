package context_manager

import "strings"

// PromptSection is one text block in a system message. CacheEphemeral marks
// stable prefix blocks that should receive provider cache_control breakpoints.
type PromptSection struct {
	Text           string `json:"text"`
	CacheEphemeral bool   `json:"cache_ephemeral,omitempty"`
}

func JoinPromptSections(sections []PromptSection) string {
	parts := make([]string, 0, len(sections))
	for _, section := range sections {
		if text := strings.TrimSpace(section.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}
