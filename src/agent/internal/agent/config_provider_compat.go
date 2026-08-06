package agent

import (
	"fmt"

	"github.com/BurntSushi/toml"
)

// UnmarshalTOML accepts the former provider field while keeping type as the
// canonical field. When both are present, type wins even when it is empty.
func (p *ModelProvider) UnmarshalTOML(data any) error {
	fields, err := providerRecordFields(data)
	if err != nil {
		return err
	}
	if p.Type, err = providerRecordType(fields); err != nil {
		return err
	}
	return providerRecordStrings(fields,
		providerRecordField{"api_key", &p.APIKey},
		providerRecordField{"token_env", &p.TokenEnv},
		providerRecordField{"base_url", &p.BaseURL},
	)
}

// UnmarshalTOML provides the same provider-to-type compatibility for TTS
// records as ModelProvider.
func (p *TTSProvider) UnmarshalTOML(data any) error {
	fields, err := providerRecordFields(data)
	if err != nil {
		return err
	}
	if p.Type, err = providerRecordType(fields); err != nil {
		return err
	}
	return providerRecordStrings(fields,
		providerRecordField{"api_key", &p.APIKey},
		providerRecordField{"token_env", &p.TokenEnv},
		providerRecordField{"model", &p.Model},
		providerRecordField{"voice_id", &p.VoiceID},
		providerRecordField{"emotion", &p.Emotion},
		providerRecordField{"reference_id", &p.ReferenceID},
	)
}

// UnmarshalTOML provides the same provider-to-type compatibility for STT
// records as ModelProvider.
func (p *STTProvider) UnmarshalTOML(data any) error {
	fields, err := providerRecordFields(data)
	if err != nil {
		return err
	}
	if p.Type, err = providerRecordType(fields); err != nil {
		return err
	}
	return providerRecordStrings(fields,
		providerRecordField{"api_key", &p.APIKey},
		providerRecordField{"token_env", &p.TokenEnv},
		providerRecordField{"model", &p.Model},
		providerRecordField{"base_url", &p.BaseURL},
		providerRecordField{"app_id", &p.AppID},
		providerRecordField{"secret_id", &p.SecretID},
		providerRecordField{"secret_key", &p.SecretKey},
		providerRecordField{"region", &p.Region},
		providerRecordField{"engine_model_type", &p.EngineModelType},
	)
}

func providerRecordFields(data any) (map[string]any, error) {
	fields, ok := data.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("provider record must be a TOML table")
	}
	return fields, nil
}

func providerRecordType(fields map[string]any) (string, error) {
	if _, exists := fields["type"]; exists {
		return providerRecordString(fields, "type")
	}
	return providerRecordString(fields, "provider")
}

type providerRecordField struct {
	key string
	dst *string
}

func providerRecordStrings(fields map[string]any, records ...providerRecordField) error {
	for _, record := range records {
		value, err := providerRecordString(fields, record.key)
		if err != nil {
			return err
		}
		*record.dst = value
	}
	return nil
}

func providerRecordString(fields map[string]any, key string) (string, error) {
	value, exists := fields[key]
	if !exists {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("provider record field %q must be a string", key)
	}
	return text, nil
}

// mergeLegacyModelProviders reads the former [providers] namespace. Canonical
// [model_providers] records win on name collisions, while legacy-only records
// remain available during migration.
func mergeLegacyModelProviders(path string, cfg *Config) error {
	var legacy struct {
		Providers map[string]ModelProvider `toml:"providers"`
	}
	if _, err := toml.DecodeFile(path, &legacy); err != nil {
		return fmt.Errorf("decode legacy model providers: %w", err)
	}
	if len(legacy.Providers) == 0 {
		return nil
	}
	if cfg.ModelProviders == nil {
		cfg.ModelProviders = make(map[string]ModelProvider, len(legacy.Providers))
	}
	for name, provider := range legacy.Providers {
		if _, canonical := cfg.ModelProviders[name]; !canonical {
			cfg.ModelProviders[name] = provider
		}
	}
	return nil
}
