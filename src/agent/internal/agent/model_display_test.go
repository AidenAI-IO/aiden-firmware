package agent

import (
	"testing"
)

func TestGetDisplayModelsForProvider(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		wantCount    int
		wantFirstID  string
		hasRecommend bool
	}{
		{
			name:         "openai has models",
			provider:     "openai",
			wantCount:    6,
			wantFirstID:  "gpt-5.5",
			hasRecommend: true,
		},
		{
			name:         "kimi has models",
			provider:     "kimi",
			wantCount:    1,
			wantFirstID:  "kimi-k3",
			hasRecommend: true,
		},
		{
			name:         "ollama has models",
			provider:     "ollama",
			wantCount:    4,
			wantFirstID:  "qwen2.5:14b",
			hasRecommend: true,
		},
		{
			name:      "unknown provider returns empty",
			provider:  "unknown-provider",
			wantCount: 0,
		},
		{
			name:      "empty provider returns empty",
			provider:  "",
			wantCount: 0,
		},
		{
			name:         "case insensitive",
			provider:     "OPENAI",
			wantCount:    6,
			wantFirstID:  "gpt-5.5",
			hasRecommend: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := GetDisplayModelsForProvider(tt.provider)

			if len(models) != tt.wantCount {
				t.Errorf("got %d models, want %d", len(models), tt.wantCount)
			}

			if tt.wantCount > 0 {
				if models[0].ID != tt.wantFirstID {
					t.Errorf("first model ID = %q, want %q", models[0].ID, tt.wantFirstID)
				}

				if tt.hasRecommend {
					foundRecommended := false
					for _, m := range models {
						if m.Recommended {
							foundRecommended = true
							break
						}
					}
					if !foundRecommended {
						t.Error("expected at least one recommended model, found none")
					}
				}
			}
		})
	}
}

func TestModelDisplayInfo_GetDescription(t *testing.T) {
	model := ModelDisplayInfo{
		ID: "test-model",
		Descriptions: map[string]string{
			localeEnglishUS:         "English description",
			localeSimplifiedChinese: "中文描述",
		},
	}

	tests := []struct {
		name   string
		locale string
		want   string
	}{
		{
			name:   "get English description",
			locale: localeEnglishUS,
			want:   "English description",
		},
		{
			name:   "get Chinese description",
			locale: localeSimplifiedChinese,
			want:   "中文描述",
		},
		{
			name:   "fallback to English for unknown locale",
			locale: "fr-FR",
			want:   "English description",
		},
		{
			name:   "fallback to English for empty locale",
			locale: "",
			want:   "English description",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := model.GetDescription(tt.locale)
			if got != tt.want {
				t.Errorf("GetDescription(%q) = %q, want %q", tt.locale, got, tt.want)
			}
		})
	}
}

func TestModelDisplayInfo_Localized(t *testing.T) {
	model := ModelDisplayInfo{
		ID: "test-model",
		Descriptions: map[string]string{
			localeEnglishUS:         "English description",
			localeSimplifiedChinese: "中文描述",
		},
		Recommended: true,
	}

	tests := []struct {
		name            string
		locale          string
		wantDescription string
	}{
		{
			name:            "localized to English",
			locale:          localeEnglishUS,
			wantDescription: "English description",
		},
		{
			name:            "localized to Chinese",
			locale:          localeSimplifiedChinese,
			wantDescription: "中文描述",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localized := model.Localized(tt.locale)

			if localized.ID != model.ID {
				t.Errorf("ID = %q, want %q", localized.ID, model.ID)
			}
			if localized.Description != tt.wantDescription {
				t.Errorf("Description = %q, want %q", localized.Description, tt.wantDescription)
			}
			if localized.Recommended != model.Recommended {
				t.Errorf("Recommended = %v, want %v", localized.Recommended, model.Recommended)
			}
		})
	}
}

func TestGetLocalizedModelsForProvider(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		locale    string
		wantCount int
		checkDesc bool
	}{
		{
			name:      "openai models in English",
			provider:  "openai",
			locale:    localeEnglishUS,
			wantCount: 6,
			checkDesc: true,
		},
		{
			name:      "openai models in Chinese",
			provider:  "openai",
			locale:    localeSimplifiedChinese,
			wantCount: 6,
			checkDesc: true,
		},
		{
			name:      "unknown provider returns empty",
			provider:  "unknown",
			locale:    localeEnglishUS,
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models := GetLocalizedModelsForProvider(tt.provider, tt.locale)

			if len(models) != tt.wantCount {
				t.Errorf("got %d models, want %d", len(models), tt.wantCount)
			}

			if tt.checkDesc && tt.wantCount > 0 {
				// Check that descriptions are present and localized
				for _, m := range models {
					if m.Description == "" {
						t.Errorf("model %q has empty description for locale %q", m.ID, tt.locale)
					}
				}
			}
		})
	}
}

func TestAllDisplayModelsHaveDescriptions(t *testing.T) {
	// Verify all models have at least English descriptions
	for provider, models := range displayModelsByProvider {
		for _, m := range models {
			if m.ID == "" {
				t.Errorf("provider %q has model with empty ID", provider)
			}

			englishDesc, hasEnglish := m.Descriptions[localeEnglishUS]
			if !hasEnglish || englishDesc == "" {
				t.Errorf("model %q in provider %q missing English description", m.ID, provider)
			}

			// Chinese description is recommended but not required
			if _, hasChinese := m.Descriptions[localeSimplifiedChinese]; !hasChinese {
				t.Logf("model %q in provider %q missing Chinese description (informational)", m.ID, provider)
			}
		}
	}
}

func TestEachProviderHasRecommendedModel(t *testing.T) {
	// Each provider should have at least one recommended model
	for provider, models := range displayModelsByProvider {
		hasRecommended := false
		for _, m := range models {
			if m.Recommended {
				hasRecommended = true
				break
			}
		}
		if !hasRecommended && len(models) > 0 {
			t.Errorf("provider %q has no recommended model", provider)
		}
	}
}
