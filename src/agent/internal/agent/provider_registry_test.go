package agent

import (
	"reflect"
	"strings"
	"testing"

	ttsmodule "aiden-agent/internal/agent/tts"
)

func TestModelProviderRegistryIsCanonicalSource(t *testing.T) {
	want := modelProviderTypes()
	if len(want) == 0 {
		t.Fatal("model provider registry is empty")
	}

	seen := make(map[string]bool, len(modelProviderDefinitions))
	for _, definition := range modelProviderDefinitions {
		if definition.providerType == "" || definition.providerType != strings.ToLower(strings.TrimSpace(definition.providerType)) {
			t.Errorf("model provider type is not canonical: %q", definition.providerType)
		}
		if seen[definition.providerType] {
			t.Errorf("duplicate model provider type %q", definition.providerType)
		}
		seen[definition.providerType] = true
		if definition.build == nil {
			t.Errorf("model provider %q has no factory", definition.providerType)
		}
		if !isKnownProviderType(definition.providerType) {
			t.Errorf("registered model provider %q is rejected by validation", definition.providerType)
		}
	}

	idx := fieldIndex(t)
	for _, path := range []string{"model.provider", "model_providers.type"} {
		if got := enumOptionValues(idx[path].Enum); !reflect.DeepEqual(got, want) {
			t.Errorf("%s enum = %#v, want registry order %#v", path, got, want)
		}
	}
}

func TestModelProviderBaseURLCapabilityIsCanonicalSource(t *testing.T) {
	allowed := modelProviderTypesAllowingCustomBaseURL()
	idx := fieldIndex(t)
	for _, test := range []struct {
		path  string
		field string
	}{
		{path: "model_providers.base_url", field: "model_providers.type"},
	} {
		baseURL := idx[test.path]
		if baseURL.VisibleWhen == nil || len(baseURL.VisibleWhen.All) != 1 {
			t.Fatalf("%s VisibleWhen = %#v, want one registry-backed condition", test.path, baseURL.VisibleWhen)
		}
		condition := baseURL.VisibleWhen.All[0]
		if condition.Field != test.field || condition.Op != "in" || !reflect.DeepEqual(condition.Values, allowed) {
			t.Errorf("%s provider condition = %#v, want field %q with values %#v", test.path, condition, test.field, allowed)
		}
	}

	for _, definition := range modelProviderDefinitions {
		cfg := ModelConfig{Provider: definition.providerType, BaseURL: "https://example.test/v1"}
		clearNonAllowedModelBaseURL(&cfg)
		if definition.allowsCustomBaseURL && cfg.BaseURL == "" {
			t.Errorf("provider %q allows custom base_url but it was cleared", definition.providerType)
		}
		if !definition.allowsCustomBaseURL && cfg.BaseURL != "" {
			t.Errorf("provider %q disallows custom base_url but it was retained", definition.providerType)
		}
	}
}

func TestSTTProviderRegistryIsCanonicalSource(t *testing.T) {
	want := sttProviderTypes()
	if len(want) == 0 {
		t.Fatal("STT provider registry is empty")
	}

	seen := make(map[string]string)
	for _, definition := range sttProviderDefinitions {
		if definition.providerType == "" || definition.providerType != strings.ToLower(strings.TrimSpace(definition.providerType)) {
			t.Errorf("STT provider type is not canonical: %q", definition.providerType)
		}
		if definition.build == nil {
			t.Errorf("STT provider %q has no factory", definition.providerType)
		}
		for _, name := range append([]string{definition.providerType}, definition.aliases...) {
			normalized := strings.ToLower(strings.TrimSpace(name))
			if normalized != name {
				t.Errorf("STT provider name is not canonical: %q", name)
			}
			if owner, duplicate := seen[name]; duplicate {
				t.Errorf("STT provider name %q is shared by %q and %q", name, owner, definition.providerType)
			}
			seen[name] = definition.providerType
			if got, ok := canonicalSTTProviderType(name); !ok || got != definition.providerType {
				t.Errorf("canonicalSTTProviderType(%q) = %q, %v; want %q, true", name, got, ok, definition.providerType)
			}
			if !isKnownSTTProviderType(name) {
				t.Errorf("registered STT provider name %q is rejected by validation", name)
			}
		}
	}

	idx := fieldIndex(t)
	for _, path := range []string{"stt.provider", "stt_providers.type"} {
		if got := enumOptionValues(idx[path].Enum); !reflect.DeepEqual(got, want) {
			t.Errorf("%s enum = %#v, want canonical registry order %#v", path, got, want)
		}
	}
}

func TestSTTProviderNamesForCanonicalAreRegistryBacked(t *testing.T) {
	for _, definition := range sttProviderDefinitions {
		want := append([]string{definition.providerType}, definition.aliases...)
		if got := sttProviderNamesForCanonical(definition.providerType); !reflect.DeepEqual(got, want) {
			t.Errorf("sttProviderNamesForCanonical(%q) = %#v, want %#v", definition.providerType, got, want)
		}
	}

	if got := sttProviderNamesForCanonical("unknown"); got != nil {
		t.Errorf("sttProviderNamesForCanonical(unknown) = %#v, want nil", got)
	}
}

func TestUsesDefaultSTTModelFollowsProviderRegistry(t *testing.T) {
	defaultNames := sttProviderNamesForCanonical(defaultSTTProvider)
	if len(defaultNames) == 0 {
		t.Fatalf("default STT provider %q is not registered", defaultSTTProvider)
	}

	for _, definition := range sttProviderDefinitions {
		for _, name := range sttProviderNamesForCanonical(definition.providerType) {
			want := definition.providerType == defaultSTTProvider
			if got := usesDefaultSTTModel(name); got != want {
				t.Errorf("usesDefaultSTTModel(%q) = %v, want %v", name, got, want)
			}
		}
	}
}

func TestTTSRegistryIsCanonicalSource(t *testing.T) {
	want := ttsmodule.AvailableProviders()
	if len(want) == 0 {
		t.Fatal("TTS provider registry is empty")
	}
	idx := fieldIndex(t)
	for _, path := range []string{"tts.provider", "tts_providers.type"} {
		got := enumOptionValues(idx[path].Enum)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s enum = %#v, want registered providers %#v", path, got, want)
		}
		for _, provider := range got {
			if !ttsmodule.HasProvider(provider) || !isKnownTTSProviderType(provider) {
				t.Errorf("%s exposes unregistered TTS provider %q", path, provider)
			}
		}
	}

	if !isKnownTTSProviderType("minimax-ws") {
		t.Error("legacy minimax-ws alias is rejected by validation")
	}
	canonical := normalizeTTSProvider("minimax-ws")
	if !ttsmodule.HasProvider(canonical) {
		t.Errorf("legacy minimax-ws alias targets unregistered provider %q", canonical)
	}
	if ttsmodule.HasProvider("minimax-ws") {
		t.Error("legacy minimax-ws alias must not be registered as a canonical provider")
	}
}

func enumOptionValues(options []EnumOption) []string {
	values := make([]string, 0, len(options))
	for _, option := range options {
		values = append(values, option.Value)
	}
	return values
}
