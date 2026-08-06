package agent

import (
	"reflect"
	"testing"
)

// sectionByName returns a metadata section by name.
func sectionByName(t *testing.T, name string) SectionMeta {
	t.Helper()
	for _, section := range ConfigMeta().Sections {
		if section.Name == name {
			return section
		}
	}
	t.Fatalf("missing metadata section %q", name)
	return SectionMeta{}
}

// The provider dialog renders from metadata rather than hardcoding provider
// knowledge in JS, so the record fields need their own sections. Three dialogs
// with three hardcoded field lists would be three places to update per provider.
func TestConfigMeta_VoiceProviderSectionsExist(t *testing.T) {
	idx := fieldIndex(t)

	for _, path := range []string{
		"tts_providers.type", "tts_providers.api_key", "tts_providers.model",
		"tts_providers.voice_id", "tts_providers.emotion", "tts_providers.reference_id",
		"stt_providers.type", "stt_providers.api_key", "stt_providers.model",
		"stt_providers.base_url", "stt_providers.app_id", "stt_providers.secret_id",
		"stt_providers.secret_key", "stt_providers.region", "stt_providers.engine_model_type",
	} {
		if _, ok := idx[path]; !ok {
			t.Errorf("missing metadata field %s", path)
		}
	}

	// Secrets must stay flagged so the dialog renders them as password inputs.
	for _, path := range []string{
		"tts_providers.api_key", "stt_providers.api_key",
		"stt_providers.secret_id", "stt_providers.secret_key",
	} {
		if field := idx[path]; !field.Secret {
			t.Errorf("%s must be marked Secret", path)
		}
	}
}

// The per-type rules move with the fields: they must now key on
// tts_providers.type, which is the record's own type select in the dialog.
// Keying them on tts.provider would compare against a record NAME and never
// match, silently showing every field for every provider type.
func TestConfigMeta_VoiceProviderRulesKeyOnRecordType(t *testing.T) {
	idx := fieldIndex(t)

	referenceID := idx["tts_providers.reference_id"]
	wantRefVisibility := VisibleRule{All: []Condition{in("tts_providers.type", "fish-audio")}}
	if referenceID.VisibleWhen == nil || !reflect.DeepEqual(*referenceID.VisibleWhen, wantRefVisibility) {
		t.Errorf("tts_providers.reference_id visibleWhen = %#v, want %#v",
			referenceID.VisibleWhen, wantRefVisibility)
	}

	voice := idx["tts_providers.voice_id"]
	if voice.VisibleWhen == nil {
		t.Fatal("tts_providers.voice_id has no visibleWhen rule")
	}
	for _, cond := range voice.VisibleWhen.All {
		if cond.Field != "tts_providers.type" {
			t.Errorf("tts_providers.voice_id rule keys on %q, want tts_providers.type", cond.Field)
		}
	}

	tencentProviderNames := sttProviderNamesForCanonical(tencentASRProvider)
	if len(tencentProviderNames) == 0 {
		t.Fatalf("Tencent STT provider %q is not registered", tencentASRProvider)
	}
	wantTencentVisibility := VisibleRule{All: []Condition{
		in("stt_providers.type", tencentProviderNames...),
	}}
	for _, path := range []string{
		"stt_providers.app_id",
		"stt_providers.secret_id",
		"stt_providers.secret_key",
		"stt_providers.region",
		"stt_providers.engine_model_type",
	} {
		field := idx[path]
		if field.VisibleWhen == nil || !reflect.DeepEqual(*field.VisibleWhen, wantTencentVisibility) {
			t.Errorf("%s visibleWhen = %#v, want %#v", path, field.VisibleWhen, wantTencentVisibility)
		}
	}

	// No rule anywhere in the two record sections may reference the flat
	// sections, whose provider field now holds a name.
	for _, name := range []string{"tts_providers", "stt_providers"} {
		for _, field := range sectionByName(t, name).Fields {
			for _, rule := range []*VisibleRule{field.VisibleWhen, field.SelectWhen} {
				if rule == nil {
					continue
				}
				for _, cond := range rule.All {
					if cond.Field == "tts.provider" || cond.Field == "stt.provider" {
						t.Errorf("%s.%s rule keys on %q, which holds a record name",
							name, field.Key, cond.Field)
					}
				}
			}
			for _, placeholder := range field.PlaceholderWhen {
				for _, cond := range placeholder.When.All {
					if cond.Field == "tts.provider" || cond.Field == "stt.provider" {
						t.Errorf("%s.%s placeholder keys on %q, which holds a record name",
							name, field.Key, cond.Field)
					}
				}
			}
		}
	}
}

// The flat sections keep only the reference plus the genuinely global settings.
// Leaving the per-provider fields here too would give every credential two
// editors that disagree.
func TestConfigMeta_FlatVoiceSectionsAreSlim(t *testing.T) {
	ttsKeys := map[string]bool{}
	for _, field := range sectionByName(t, "tts").Fields {
		ttsKeys[field.Key] = true
	}
	wantTTS := map[string]bool{"provider": true, "speed": true}
	if !reflect.DeepEqual(ttsKeys, wantTTS) {
		t.Errorf("tts section fields = %v, want %v", ttsKeys, wantTTS)
	}

	sttKeys := map[string]bool{}
	for _, field := range sectionByName(t, "stt").Fields {
		sttKeys[field.Key] = true
	}
	wantSTT := map[string]bool{"provider": true, "language": true}
	if !reflect.DeepEqual(sttKeys, wantSTT) {
		t.Errorf("stt section fields = %v, want %v", sttKeys, wantSTT)
	}
}

// The record's type select offers provider types; the flat section's provider
// select is populated with record names by the UI at runtime, so its metadata
// enum stays the legacy type list for a bare-type config.
func TestConfigMeta_VoiceProviderTypeEnums(t *testing.T) {
	idx := fieldIndex(t)

	ttsTypes := idx["tts_providers.type"]
	if ttsTypes.Widget != WidgetSelect {
		t.Errorf("tts_providers.type widget = %q, want select", ttsTypes.Widget)
	}
	// Every offered type must actually be accepted by validation, or the dialog
	// can save a record that fails on the next load.
	for _, option := range ttsTypes.Enum {
		if option.Value == "" {
			continue
		}
		if !isKnownTTSProviderType(option.Value) {
			t.Errorf("tts_providers.type offers %q, which isKnownTTSProviderType rejects", option.Value)
		}
	}

	sttTypes := idx["stt_providers.type"]
	for _, option := range sttTypes.Enum {
		if option.Value == "" {
			continue
		}
		if !isKnownSTTProviderType(option.Value) {
			t.Errorf("stt_providers.type offers %q, which isKnownSTTProviderType rejects", option.Value)
		}
	}
}
