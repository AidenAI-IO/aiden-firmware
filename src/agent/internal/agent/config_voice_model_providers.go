package agent

import (
	"aiden-agent/internal/agent/realtimevoice"
	"fmt"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// VoiceModelProvider is one [voice_model_providers.<name>] record. The flat
// [voice_model] section keeps the selected record name and session-wide
// behavior; credentials and provider-specific routing stay on the record.
type VoiceModelProvider struct {
	Type             string `toml:"type"`
	UpstreamProvider string `toml:"upstream_provider,omitempty"`
	AgentID          string `toml:"agent_id,omitempty"`
	APIKey           string `toml:"api_key,omitempty"`
	Model            string `toml:"model,omitempty"`
	WorkspaceID      string `toml:"workspace_id,omitempty"`
	Region           string `toml:"region,omitempty"`
	AuthMode         string `toml:"auth_mode,omitempty"`
	ProjectID        string `toml:"project_id,omitempty"`
	Location         string `toml:"location,omitempty"`
	Endpoint         string `toml:"endpoint,omitempty"`
	BaseURL          string `toml:"base_url,omitempty"`
	RealtimeProtocol string `toml:"realtime_protocol,omitempty"`
	Voice            string `toml:"voice,omitempty"`
}

func normalizeVoiceModelProviderType(providerType string) string {
	return strings.ToLower(strings.TrimSpace(providerType))
}

func isKnownVoiceModelProviderType(providerType string) bool {
	return realtimevoice.IsProvider(providerType)
}

func defaultVoiceModelProviderRecord(providerType string) VoiceModelProvider {
	providerType = normalizeVoiceModelProviderType(providerType)
	record := VoiceModelProvider{Type: providerType}
	if providerType == defaultVoiceModelProvider {
		record.Model = defaultVoiceModelModel
		record.Voice = defaultVoiceModelVoice
	}
	return record
}

// migrateLegacyVoiceModelProvider turns the old flat [voice_model] provider
// settings into one named record. Only fields present in the source TOML are
// copied, except canonical Qwen defaults used to seed its first record.
func migrateLegacyVoiceModelProvider(cfg *Config, metadata toml.MetaData) {
	if cfg == nil {
		return
	}
	ref := strings.TrimSpace(cfg.VoiceModel.Provider)
	if ref == "" {
		return
	}

	record, exists := cfg.VoiceModelProviders[ref]
	if !exists {
		providerType := normalizeVoiceModelProviderType(ref)
		if !isKnownVoiceModelProviderType(providerType) {
			return
		}
		record = defaultVoiceModelProviderRecord(providerType)
	}
	copyDefinedLegacyVoiceModelFields(&record, cfg.VoiceModel, metadata)
	if cfg.VoiceModelProviders == nil {
		cfg.VoiceModelProviders = make(map[string]VoiceModelProvider)
	}
	cfg.VoiceModelProviders[ref] = record
	clearVoiceModelProviderFields(&cfg.VoiceModel)
}

func copyDefinedLegacyVoiceModelFields(record *VoiceModelProvider, legacy VoiceModelConfig, metadata toml.MetaData) {
	if metadata.IsDefined("voice_model", "upstream_provider") {
		record.UpstreamProvider = legacy.UpstreamProvider
	}
	if metadata.IsDefined("voice_model", "agent_id") {
		record.AgentID = legacy.AgentID
	}
	if metadata.IsDefined("voice_model", "api_key") {
		record.APIKey = legacy.APIKey
	}
	if metadata.IsDefined("voice_model", "model") {
		record.Model = legacy.Model
	}
	if metadata.IsDefined("voice_model", "workspace_id") {
		record.WorkspaceID = legacy.WorkspaceID
	}
	if metadata.IsDefined("voice_model", "region") {
		record.Region = legacy.Region
	}
	if metadata.IsDefined("voice_model", "auth_mode") {
		record.AuthMode = legacy.AuthMode
	}
	if metadata.IsDefined("voice_model", "project_id") {
		record.ProjectID = legacy.ProjectID
	}
	if metadata.IsDefined("voice_model", "location") {
		record.Location = legacy.Location
	}
	if metadata.IsDefined("voice_model", "endpoint") {
		record.Endpoint = legacy.Endpoint
	}
	if metadata.IsDefined("voice_model", "base_url") {
		record.BaseURL = legacy.BaseURL
	}
	if metadata.IsDefined("voice_model", "realtime_protocol") {
		record.RealtimeProtocol = legacy.RealtimeProtocol
	}
	if metadata.IsDefined("voice_model", "voice") {
		record.Voice = legacy.Voice
	}
}

func clearVoiceModelProviderFields(config *VoiceModelConfig) {
	config.UpstreamProvider = ""
	config.AgentID = ""
	config.APIKey = ""
	config.Model = ""
	config.WorkspaceID = ""
	config.Region = ""
	config.AuthMode = ""
	config.ProjectID = ""
	config.Location = ""
	config.Endpoint = ""
	config.BaseURL = ""
	config.RealtimeProtocol = ""
	config.Voice = ""
}

// resolveVoiceModelProvider expands the selected record into the legacy flat
// runtime shape consumed by the realtime adapter seam.
func resolveVoiceModelProvider(cfg *Config) {
	if cfg == nil {
		return
	}
	ref := strings.TrimSpace(cfg.VoiceModel.Provider)
	record, ok := cfg.VoiceModelProviders[ref]
	if ok {
		providerType := normalizeVoiceModelProviderType(record.Type)
		if providerType == "" {
			return
		}
		cfg.VoiceModel.Provider = providerType
		cfg.VoiceModel.ActiveProviderRecord = ref
		fillVoiceModelProviderFields(&cfg.VoiceModel, record)
	} else {
		cfg.VoiceModel.Provider = normalizeVoiceModelProviderType(ref)
	}
	applyVoiceModelTypeDefaults(&cfg.VoiceModel)
}

func fillVoiceModelProviderFields(config *VoiceModelConfig, record VoiceModelProvider) {
	if config.UpstreamProvider == "" {
		config.UpstreamProvider = record.UpstreamProvider
	}
	if config.AgentID == "" {
		config.AgentID = record.AgentID
	}
	if config.APIKey == "" {
		config.APIKey = record.APIKey
	}
	if config.Model == "" {
		config.Model = record.Model
	}
	if config.WorkspaceID == "" {
		config.WorkspaceID = record.WorkspaceID
	}
	if config.Region == "" {
		config.Region = record.Region
	}
	if config.AuthMode == "" {
		config.AuthMode = record.AuthMode
	}
	if config.ProjectID == "" {
		config.ProjectID = record.ProjectID
	}
	if config.Location == "" {
		config.Location = record.Location
	}
	if config.Endpoint == "" {
		config.Endpoint = record.Endpoint
	}
	if config.BaseURL == "" {
		config.BaseURL = record.BaseURL
	}
	if config.RealtimeProtocol == "" {
		config.RealtimeProtocol = record.RealtimeProtocol
	}
	if config.Voice == "" {
		config.Voice = record.Voice
	}
}

func applyVoiceModelTypeDefaults(config *VoiceModelConfig) {
	if config == nil || normalizeVoiceModelProviderType(config.Provider) != defaultVoiceModelProvider {
		return
	}
	if config.Model == "" {
		config.Model = defaultVoiceModelModel
	}
	if config.Voice == "" {
		config.Voice = defaultVoiceModelVoice
	}
	if config.TurnDetection == "" {
		config.TurnDetection = defaultVoiceModelTurnDetection
	}
}

func validateVoiceModelProviderRecords(cfg Config) error {
	names := make([]string, 0, len(cfg.VoiceModelProviders))
	for name := range cfg.VoiceModelProviders {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("voice_model_providers: record name cannot be empty")
		}
	}

	ref := strings.TrimSpace(cfg.VoiceModel.Provider)
	if ref == "" {
		return nil
	}
	if record, ok := cfg.VoiceModelProviders[ref]; ok {
		providerType := normalizeVoiceModelProviderType(record.Type)
		if providerType == "" {
			return fmt.Errorf("voice_model_providers.%s: provider type is required", ref)
		}
		if !isKnownVoiceModelProviderType(providerType) {
			return fmt.Errorf("voice_model_providers.%s: unsupported provider type %q", ref, record.Type)
		}
		if err := validateVoiceModelRealtimeProtocol(record.RealtimeProtocol, providerType, fmt.Sprintf("voice_model_providers.%s.realtime_protocol", ref)); err != nil {
			return err
		}
		return nil
	}
	if !isKnownVoiceModelProviderType(ref) {
		return fmt.Errorf("voice_model.provider %q is neither a [voice_model_providers] record nor a known realtime provider type", ref)
	}
	return nil
}

func validateVoiceModelRealtimeProtocol(protocol, providerType, field string) error {
	normalized := strings.ToLower(strings.TrimSpace(protocol))
	switch normalized {
	case "", "ga", "legacy", "beta":
	default:
		return fmt.Errorf("%s: unsupported protocol %q (expected ga or legacy)", field, protocol)
	}
	if normalized != "" && normalizeVoiceModelProviderType(providerType) != "openai" {
		return fmt.Errorf("%s is only supported for provider=openai", field)
	}
	return nil
}
