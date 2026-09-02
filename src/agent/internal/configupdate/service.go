package configupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/configdoc"
	"github.com/BurntSushi/toml"
)

// Result describes the persisted, resolved config after an update.
type Result struct {
	OK             bool     `json:"ok"`
	Config         Config   `json:"config"`
	ChangedPaths   []string `json:"changed_paths"`
	RebootRequired bool     `json:"reboot_required"`
	Error          string   `json:"error,omitempty"`
	ErrorKind      string   `json:"error_kind,omitempty"`
}

const (
	ErrorKindInvalidRequest = "invalid_request"
	ErrorKindInternal       = "internal"
)

type configUpdateError struct {
	kind string
	err  error
}

func (e *configUpdateError) Error() string { return e.err.Error() }
func (e *configUpdateError) Unwrap() error { return e.err }

func invalidConfigUpdate(err error) error {
	return &configUpdateError{kind: ErrorKindInvalidRequest, err: err}
}

func internalConfigUpdate(err error) error {
	return &configUpdateError{kind: ErrorKindInternal, err: err}
}

// ErrorKind returns the stable external classification for an update error.
func ErrorKind(err error) string {
	var updateErr *configUpdateError
	if errors.As(err, &updateErr) {
		return updateErr.kind
	}
	return ErrorKindInternal
}

type providerRenames map[string]map[string]string

var providerRecordSections = []string{
	"model_providers",
	"tts_providers",
	"stt_providers",
	"voice_model_providers",
}

// Service applies validated config updates independently of any transport.
type Service struct{}

func NewService() *Service {
	return &Service{}
}

// Update applies a config_web JSON merge patch and atomically persists it.
func (s *Service) Update(path string, patchJSON []byte) (Result, error) {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal(patchJSON, &patch); err != nil {
		return Result{}, invalidConfigUpdate(fmt.Errorf("invalid JSON merge patch: %w", err))
	}
	if patch == nil {
		return Result{}, invalidConfigUpdate(fmt.Errorf("config patch must be an object"))
	}
	if nested, ok := patch["config"]; ok {
		var configPatch map[string]json.RawMessage
		if err := json.Unmarshal(nested, &configPatch); err != nil {
			return Result{}, invalidConfigUpdate(fmt.Errorf("config patch must be an object: %w", err))
		}
		if configPatch == nil {
			return Result{}, invalidConfigUpdate(fmt.Errorf("config patch must be an object"))
		}
		patch = configPatch
	}
	renames, err := takeProviderRenames(patch)
	if err != nil {
		return Result{}, invalidConfigUpdate(err)
	}
	resolvedPath, original, fileMode, err := prepareConfigUpdateFile(path)
	if err != nil {
		return Result{}, internalConfigUpdate(err)
	}
	current, err := agent.LoadResolvedConfig(resolvedPath)
	if err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("load config: %w", err))
	}
	currentDTO := FromAgentConfig(current)
	if err := normalizeLegacyWebConfigPatch(patch, current); err != nil {
		return Result{}, invalidConfigUpdate(err)
	}
	explicitCredentials := explicitProviderCredentialEdits(patch, renames, current)
	if err := stripReadOnlyStatusFields(patch, currentDTO); err != nil {
		return Result{}, internalConfigUpdate(err)
	}
	if err := restoreRenamedProviderCredentials(patch, renames, current); err != nil {
		return Result{}, invalidConfigUpdate(err)
	}
	if err := preserveProviderCredentials(patch, current); err != nil {
		return Result{}, invalidConfigUpdate(err)
	}
	patch, err = filterNoopWebConfigPatch(patch, currentDTO)
	if err != nil {
		return Result{}, internalConfigUpdate(err)
	}
	if len(patch) > 0 {
		if err := persistLegacyProviderFields(patch, current, original, renames, explicitCredentials); err != nil {
			return Result{}, internalConfigUpdate(err)
		}
	}
	operations, err := configPatchOperations(patch)
	if err != nil {
		return Result{}, invalidConfigUpdate(err)
	}
	updated, changed, err := configdoc.Apply(original, operations)
	if err != nil {
		return Result{}, internalConfigUpdate(err)
	}
	if len(changed) == 0 {
		cfg, err := agent.LoadResolvedConfig(resolvedPath)
		if err != nil {
			return Result{}, internalConfigUpdate(err)
		}
		return Result{OK: true, Config: FromAgentConfig(cfg), ChangedPaths: []string{}, RebootRequired: false}, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(resolvedPath), ".agent.toml.config-update-*.toml")
	if err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("create temporary config: %w", err))
	}
	tmpPath := tmp.Name()
	defer tmp.Close()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("set temporary config mode: %w", err))
	}
	n, err := tmp.Write(updated)
	if err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("write temporary config: %w", err))
	}
	if n != len(updated) {
		return Result{}, internalConfigUpdate(fmt.Errorf("write temporary config: %w", io.ErrShortWrite))
	}
	if err := tmp.Sync(); err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("sync temporary config: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("close temporary config: %w", err))
	}
	candidate, err := agent.LoadResolvedConfig(tmpPath)
	if err != nil {
		return Result{}, invalidConfigUpdate(fmt.Errorf("validate config: %w", err))
	}
	if err := candidate.ValidateVoiceProviders(); err != nil {
		return Result{}, invalidConfigUpdate(fmt.Errorf("validate voice providers: %w", err))
	}
	if err := os.Rename(tmpPath, resolvedPath); err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("replace config: %w", err))
	}
	directory, err := os.Open(filepath.Dir(resolvedPath))
	if err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("open config directory: %w", err))
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return Result{}, internalConfigUpdate(fmt.Errorf("sync config directory: %w", err))
	}
	return Result{
		OK:             true,
		Config:         FromAgentConfig(candidate),
		ChangedPaths:   changed,
		RebootRequired: requiresConfigReboot(current, candidate),
	}, nil
}

func prepareConfigUpdateFile(path string) (string, []byte, os.FileMode, error) {
	_, err := os.Lstat(path)
	if err == nil {
		resolvedPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return "", nil, 0, fmt.Errorf("resolve config path: %w", err)
		}
		original, err := os.ReadFile(resolvedPath)
		if err != nil {
			return "", nil, 0, fmt.Errorf("read config: %w", err)
		}
		resolvedInfo, err := os.Stat(resolvedPath)
		if err != nil {
			return "", nil, 0, fmt.Errorf("stat config: %w", err)
		}
		return resolvedPath, original, resolvedInfo.Mode().Perm(), nil
	}
	if !os.IsNotExist(err) {
		return "", nil, 0, fmt.Errorf("read config path: %w", err)
	}

	resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return "", nil, 0, fmt.Errorf("resolve config directory: %w", err)
	}
	dirInfo, err := os.Stat(resolvedDir)
	if err != nil {
		return "", nil, 0, fmt.Errorf("stat config directory: %w", err)
	}
	if !dirInfo.IsDir() {
		return "", nil, 0, fmt.Errorf("config parent must be a directory: %s", resolvedDir)
	}
	return filepath.Join(resolvedDir, filepath.Base(path)), nil, 0o640, nil
}

// stripReadOnlyStatusFields removes unchanged has_* markers emitted by GET
// /api/config. They describe write-only credentials and are not writable TOML
// fields, but a complete GET response must still be safe to submit as a patch.
// Changed or malformed markers remain in the patch and are rejected normally.
func stripReadOnlyStatusFields(values map[string]json.RawMessage, current Config) error {
	encoded, err := json.Marshal(current)
	if err != nil {
		return err
	}
	var currentValues map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &currentValues); err != nil {
		return err
	}
	stripMatchingReadOnlyStatusFields(values, currentValues)
	return nil
}

func stripMatchingReadOnlyStatusFields(values, current map[string]json.RawMessage) {
	for key, raw := range values {
		if strings.HasPrefix(key, "has_") {
			if existing, ok := current[key]; ok && jsonValuesEqual(raw, existing) {
				delete(values, key)
			}
			continue
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			continue
		}
		var child, currentChild map[string]json.RawMessage
		if json.Unmarshal(raw, &child) != nil {
			continue
		}
		if currentRaw, ok := current[key]; !ok || json.Unmarshal(currentRaw, &currentChild) != nil {
			continue
		}
		stripMatchingReadOnlyStatusFields(child, currentChild)
		if encoded, err := json.Marshal(child); err == nil {
			values[key] = encoded
		}
	}
}

func normalizeLegacyWebConfigPatch(patch map[string]json.RawMessage, current agent.Config) error {
	for _, section := range providerRecordSections {
		raw, ok := patch[section]
		if !ok {
			continue
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal(raw, &records); err != nil || records == nil {
			continue
		}
		for name, rawRecord := range records {
			if bytes.Equal(bytes.TrimSpace(rawRecord), []byte("null")) {
				continue
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(rawRecord, &record); err != nil || record == nil {
				continue
			}
			if _, hasType := record["type"]; !hasType {
				if legacyType, hasLegacyType := record["provider"]; hasLegacyType {
					record["type"] = legacyType
				}
			}
			delete(record, "provider")
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			records[name] = encoded
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			return err
		}
		patch[section] = encoded
	}

	if rawAgent, ok := patch["agent"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rawAgent, &fields) == nil && fields != nil {
			delete(fields, "default_platform")
			delete(fields, "instruction")
			if len(fields) == 0 {
				delete(patch, "agent")
			} else if encoded, err := json.Marshal(fields); err == nil {
				patch["agent"] = encoded
			}
		}
	}

	rawModel, ok := patch["model"]
	if !ok {
		return nil
	}
	var model map[string]json.RawMessage
	if err := json.Unmarshal(rawModel, &model); err != nil || model == nil {
		return nil
	}
	delete(model, "base_url")
	rawKey, hasKey := model["api_key"]
	if hasKey {
		var apiKey string
		if err := json.Unmarshal(rawKey, &apiKey); err == nil {
			model["api_key"] = json.RawMessage("null")
		}
		if strings.TrimSpace(apiKey) != "" {
			provider := current.Model.Provider
			if rawProvider, ok := model["provider"]; ok {
				_ = json.Unmarshal(rawProvider, &provider)
			}
			if strings.TrimSpace(provider) != "" {
				if err := addLegacyModelProviderCredential(patch, current, provider, apiKey); err != nil {
					return err
				}
			}
		}
	}
	if len(model) == 0 {
		delete(patch, "model")
	} else {
		encoded, err := json.Marshal(model)
		if err != nil {
			return err
		}
		patch["model"] = encoded
	}
	return nil
}

func addLegacyModelProviderCredential(patch map[string]json.RawMessage, current agent.Config, provider, apiKey string) error {
	var records map[string]json.RawMessage
	if raw, ok := patch["model_providers"]; ok {
		if err := json.Unmarshal(raw, &records); err != nil {
			return fmt.Errorf("model_providers patch must be an object: %w", err)
		}
		if records == nil {
			return fmt.Errorf("model_providers patch must be an object")
		}
	}
	if records == nil {
		records = make(map[string]json.RawMessage)
	}
	var record map[string]json.RawMessage
	if raw, ok := records[provider]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		_ = json.Unmarshal(raw, &record)
	}
	if record == nil {
		record = make(map[string]json.RawMessage)
		if existing, ok := current.ModelProviders[provider]; ok {
			typeJSON, _ := json.Marshal(existing.Type)
			record["type"] = typeJSON
		} else {
			typeJSON, _ := json.Marshal(provider)
			record["type"] = typeJSON
		}
	}
	keyJSON, _ := json.Marshal(apiKey)
	record["api_key"] = keyJSON
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return err
	}
	records[provider] = encodedRecord
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		return err
	}
	patch["model_providers"] = encodedRecords
	return nil
}

type providerFieldEdits map[string]map[string]map[string]bool

func explicitProviderCredentialEdits(patch map[string]json.RawMessage, renames providerRenames, current agent.Config) providerFieldEdits {
	edits := make(providerFieldEdits)
	for _, section := range providerRecordSections {
		var records map[string]json.RawMessage
		if raw, ok := patch[section]; !ok || json.Unmarshal(raw, &records) != nil {
			continue
		}
		for name, rawRecord := range records {
			var record map[string]json.RawMessage
			if json.Unmarshal(rawRecord, &record) != nil {
				continue
			}
			sourceName := name
			if oldName, ok := renames[section][name]; ok {
				sourceName = oldName
			}
			previous := providerCredentialValues(current, section, sourceName)
			for key := range previous {
				raw, ok := record[key]
				if !ok || !isExplicitCredentialEdit(raw, previous[key]) {
					continue
				}
				if edits[section] == nil {
					edits[section] = make(map[string]map[string]bool)
				}
				if edits[section][name] == nil {
					edits[section][name] = make(map[string]bool)
				}
				edits[section][name][key] = true
			}
		}
	}
	return edits
}

func isExplicitCredentialEdit(raw json.RawMessage, previous string) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return true
	}
	if strings.TrimSpace(value) == "" {
		return false
	}
	return previous == "" || (value != previous && value != maskCredential(previous))
}

// persistLegacyProviderFields joins a real save with the on-disk migration.
// The record receives the legacy effective value unless this request supplied
// a new credential, then the flat field is deleted in the same atomic update.
func persistLegacyProviderFields(
	patch map[string]json.RawMessage,
	current agent.Config,
	original []byte,
	renames providerRenames,
	explicitCredentials providerFieldEdits,
) error {
	var rawConfig map[string]any
	metadata, err := toml.Decode(string(original), &rawConfig)
	if err != nil {
		return fmt.Errorf("decode legacy provider fields: %w", err)
	}

	if metadata.IsDefined("model", "api_key") && strings.TrimSpace(current.Model.Provider) != "" {
		provider := current.Model.Provider
		providerType := provider
		if record, ok := current.ModelProviders[provider]; ok {
			providerType = record.Type
		}
		if err := persistLegacyProviderRecord(patch, metadata, renames, explicitCredentials,
			"model_providers", provider, providerType, map[string]string{"api_key": current.Model.APIKey},
			map[string]bool{"api_key": true}); err != nil {
			return err
		}
		if err := addLegacyFieldDeletes(patch, "model", []string{"api_key"}); err != nil {
			return err
		}
	}

	if provider := current.TTS.Provider; strings.TrimSpace(provider) != "" {
		if record, ok := current.TTSProviders[provider]; ok {
			values := definedLegacyValues(metadata, "tts", map[string]string{
				"api_key": record.APIKey, "model": record.Model, "voice_id": record.VoiceID,
				"emotion": record.Emotion, "reference_id": record.ReferenceID,
			})
			if len(values) > 0 {
				if err := persistLegacyProviderRecord(patch, metadata, renames, explicitCredentials,
					"tts_providers", provider, record.Type, values, map[string]bool{"api_key": true}); err != nil {
					return err
				}
				if err := addLegacyFieldDeletes(patch, "tts", mapKeys(values)); err != nil {
					return err
				}
			}
		}
	}

	if provider := current.STT.Provider; strings.TrimSpace(provider) != "" {
		if record, ok := current.STTProviders[provider]; ok {
			values := definedLegacyValues(metadata, "stt", map[string]string{
				"api_key": record.APIKey, "model": record.Model, "base_url": record.BaseURL,
				"app_id": record.AppID, "secret_id": record.SecretID, "secret_key": record.SecretKey,
				"region": record.Region, "engine_model_type": record.EngineModelType,
			})
			if len(values) > 0 {
				if err := persistLegacyProviderRecord(patch, metadata, renames, explicitCredentials,
					"stt_providers", provider, record.Type, values,
					map[string]bool{"api_key": true, "secret_id": true, "secret_key": true}); err != nil {
					return err
				}
				if err := addLegacyFieldDeletes(patch, "stt", mapKeys(values)); err != nil {
					return err
				}
			}
		}
	}

	if provider := current.VoiceModel.Provider; strings.TrimSpace(provider) != "" {
		if record, ok := current.VoiceModelProviders[provider]; ok {
			values := definedLegacyValues(metadata, "voice_model", map[string]string{
				"upstream_provider": record.UpstreamProvider,
				"agent_id":          record.AgentID,
				"api_key":           record.APIKey,
				"model":             record.Model,
				"workspace_id":      record.WorkspaceID,
				"region":            record.Region,
				"auth_mode":         record.AuthMode,
				"project_id":        record.ProjectID,
				"location":          record.Location,
				"endpoint":          record.Endpoint,
				"base_url":          record.BaseURL,
				"voice":             record.Voice,
			})
			if len(values) > 0 {
				if err := persistLegacyProviderRecord(patch, metadata, renames, explicitCredentials,
					"voice_model_providers", provider, record.Type, values,
					map[string]bool{"api_key": true}); err != nil {
					return err
				}
				if err := addLegacyFieldDeletes(patch, "voice_model", mapKeys(values)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func definedLegacyValues(metadata toml.MetaData, section string, values map[string]string) map[string]string {
	result := make(map[string]string)
	for key, value := range values {
		if metadata.IsDefined(section, key) {
			result[key] = value
		}
	}
	return result
}

func persistLegacyProviderRecord(
	patch map[string]json.RawMessage,
	metadata toml.MetaData,
	renames providerRenames,
	explicitCredentials providerFieldEdits,
	section, sourceName, providerType string,
	values map[string]string,
	credentialFields map[string]bool,
) error {
	targetName := sourceName
	for newName, oldName := range renames[section] {
		if oldName == sourceName {
			targetName = newName
			break
		}
	}

	records := make(map[string]json.RawMessage)
	if raw, ok := patch[section]; ok {
		if err := json.Unmarshal(raw, &records); err != nil || records == nil {
			return fmt.Errorf("%s patch must be an object", section)
		}
	}
	if raw, ok := records[targetName]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	record := make(map[string]json.RawMessage)
	if raw, ok := records[targetName]; ok {
		if err := json.Unmarshal(raw, &record); err != nil || record == nil {
			return fmt.Errorf("%s.%s patch must be an object", section, targetName)
		}
	}
	if !metadata.IsDefined(section, sourceName) {
		if _, exists := record["type"]; !exists {
			record["type"], _ = json.Marshal(providerType)
		}
	}
	for key, value := range values {
		if credentialFields[key] && explicitCredentials[section][targetName][key] {
			continue
		}
		if !credentialFields[key] {
			if _, exists := record[key]; exists {
				continue
			}
		}
		record[key], _ = json.Marshal(value)
	}
	encodedRecord, err := json.Marshal(record)
	if err != nil {
		return err
	}
	records[targetName] = encodedRecord
	encodedRecords, err := json.Marshal(records)
	if err != nil {
		return err
	}
	patch[section] = encodedRecords
	return nil
}

func addLegacyFieldDeletes(patch map[string]json.RawMessage, section string, fields []string) error {
	values := make(map[string]json.RawMessage)
	if raw, ok := patch[section]; ok {
		if err := json.Unmarshal(raw, &values); err != nil || values == nil {
			return fmt.Errorf("%s patch must be an object", section)
		}
	}
	for _, field := range fields {
		values[field] = json.RawMessage("null")
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return err
	}
	patch[section] = encoded
	return nil
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func takeProviderRenames(patch map[string]json.RawMessage) (providerRenames, error) {
	raw, exists := patch["_provider_renames"]
	if !exists {
		return nil, nil
	}
	delete(patch, "_provider_renames")
	var sections map[string]map[string]string
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil, fmt.Errorf("_provider_renames must be an object: %w", err)
	}
	if sections == nil {
		return nil, fmt.Errorf("_provider_renames must be an object")
	}
	for section, renames := range sections {
		if !isProviderRecordSection(section) {
			return nil, fmt.Errorf("_provider_renames has unsupported section %s", section)
		}
		if renames == nil {
			return nil, fmt.Errorf("_provider_renames.%s must be an object", section)
		}
		oldNames := make(map[string]struct{}, len(renames))
		for newName, oldName := range renames {
			if strings.TrimSpace(newName) == "" || strings.TrimSpace(oldName) == "" || newName == oldName {
				return nil, fmt.Errorf("invalid provider rename in %s", section)
			}
			if _, exists := oldNames[oldName]; exists {
				return nil, fmt.Errorf("provider rename source %s.%s is used more than once", section, oldName)
			}
			oldNames[oldName] = struct{}{}
		}
	}
	return sections, nil
}

func restoreRenamedProviderCredentials(patch map[string]json.RawMessage, renames providerRenames, current agent.Config) error {
	for section, sectionRenames := range renames {
		rawSection, ok := patch[section]
		if !ok {
			return fmt.Errorf("provider rename in %s requires a provider patch", section)
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal(rawSection, &records); err != nil {
			return fmt.Errorf("%s provider patch must be an object: %w", section, err)
		}
		for newName, oldName := range sectionRenames {
			if !providerRecordExists(current, section, oldName) {
				return fmt.Errorf("provider rename source %s.%s does not exist", section, oldName)
			}
			if providerRecordExists(current, section, newName) {
				return fmt.Errorf("provider rename target %s.%s already exists", section, newName)
			}
			oldRaw, oldDeleted := records[oldName]
			if !oldDeleted || !bytes.Equal(bytes.TrimSpace(oldRaw), []byte("null")) {
				return fmt.Errorf("provider rename in %s must delete %s", section, oldName)
			}
			newRaw, newExists := records[newName]
			if !newExists || bytes.Equal(bytes.TrimSpace(newRaw), []byte("null")) {
				return fmt.Errorf("provider rename in %s must create %s", section, newName)
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(newRaw, &record); err != nil {
				return fmt.Errorf("provider rename target %s.%s must be an object: %w", section, newName, err)
			}
			for key, value := range providerCredentialValues(current, section, oldName) {
				if value == "" {
					continue
				}
				if submitted, ok := record[key]; ok && !credentialNeedsPreservation(submitted, value) {
					continue
				}
				encoded, err := json.Marshal(value)
				if err != nil {
					return err
				}
				record[key] = encoded
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			records[newName] = encoded
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			return err
		}
		patch[section] = encoded
	}
	return nil
}

func providerRecordExists(config agent.Config, section, name string) bool {
	switch section {
	case "model_providers":
		_, ok := config.ModelProviders[name]
		return ok
	case "tts_providers":
		_, ok := config.TTSProviders[name]
		return ok
	case "stt_providers":
		_, ok := config.STTProviders[name]
		return ok
	case "voice_model_providers":
		_, ok := config.VoiceModelProviders[name]
		return ok
	default:
		return false
	}
}

func preserveProviderCredentials(patch map[string]json.RawMessage, current agent.Config) error {
	for _, section := range providerRecordSections {
		rawSection, ok := patch[section]
		if !ok {
			continue
		}
		var records map[string]json.RawMessage
		if err := json.Unmarshal(rawSection, &records); err != nil {
			return fmt.Errorf("%s provider patch must be an object: %w", section, err)
		}
		for name, rawRecord := range records {
			if bytes.Equal(bytes.TrimSpace(rawRecord), []byte("null")) {
				continue
			}
			var record map[string]json.RawMessage
			if err := json.Unmarshal(rawRecord, &record); err != nil {
				continue
			}
			for key, previous := range providerCredentialValues(current, section, name) {
				if previous == "" {
					continue
				}
				submitted, exists := record[key]
				if !exists || !credentialNeedsPreservation(submitted, previous) {
					continue
				}
				// Omitting a preserved credential leaves its original TOML token
				// untouched. Re-inserting the decoded value would unnecessarily
				// normalize literal strings and report a false change.
				delete(record, key)
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				return err
			}
			records[name] = encoded
		}
		encoded, err := json.Marshal(records)
		if err != nil {
			return err
		}
		patch[section] = encoded
	}
	return nil
}

func credentialNeedsPreservation(raw json.RawMessage, previous string) bool {
	var submitted string
	if json.Unmarshal(raw, &submitted) != nil {
		return false
	}
	return strings.TrimSpace(submitted) == "" || submitted == previous || submitted == maskCredential(previous)
}

func maskCredential(value string) string {
	if len(value) <= 8 {
		return "***"
	}
	return value[:4] + "***" + value[len(value)-4:]
}

func providerCredentialValues(config agent.Config, section, name string) map[string]string {
	switch section {
	case "model_providers":
		return map[string]string{"api_key": config.ModelProviders[name].APIKey}
	case "tts_providers":
		return map[string]string{"api_key": config.TTSProviders[name].APIKey}
	case "stt_providers":
		provider := config.STTProviders[name]
		return map[string]string{
			"api_key":    provider.APIKey,
			"secret_id":  provider.SecretID,
			"secret_key": provider.SecretKey,
		}
	case "voice_model_providers":
		return map[string]string{"api_key": config.VoiceModelProviders[name].APIKey}
	default:
		return nil
	}
}

func isProviderRecordSection(section string) bool {
	for _, candidate := range providerRecordSections {
		if section == candidate {
			return true
		}
	}
	return false
}

func filterNoopWebConfigPatch(patch map[string]json.RawMessage, current Config) (map[string]json.RawMessage, error) {
	encoded, err := json.Marshal(current)
	if err != nil {
		return nil, err
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &values); err != nil {
		return nil, err
	}
	return filterNoopObject(patch, values), nil
}

func filterNoopObject(patch, current map[string]json.RawMessage) map[string]json.RawMessage {
	result := make(map[string]json.RawMessage)
	for key, raw := range patch {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			result[key] = raw
			continue
		}
		var child map[string]json.RawMessage
		if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &child) == nil {
			var currentChild map[string]json.RawMessage
			if currentRaw, ok := current[key]; ok && json.Unmarshal(currentRaw, &currentChild) == nil {
				filtered := filterNoopObject(child, currentChild)
				if len(filtered) == 0 {
					continue
				}
				encoded, _ := json.Marshal(filtered)
				result[key] = encoded
				continue
			}
		}
		if existing, ok := current[key]; ok && jsonValuesEqual(raw, existing) {
			continue
		}
		result[key] = raw
	}
	return result
}

func jsonValuesEqual(left, right json.RawMessage) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}

func configPatchOperations(patch map[string]json.RawMessage) ([]configdoc.Operation, error) {
	if err := validateWebConfigPatch(patch); err != nil {
		return nil, err
	}
	var operations []configdoc.Operation
	sections := make([]string, 0, len(patch))
	for key := range patch {
		sections = append(sections, key)
	}
	sort.Strings(sections)
	for _, section := range sections {
		if err := flattenConfigPatch([]string{section}, patch[section], &operations); err != nil {
			return nil, err
		}
	}
	return operations, nil
}

func flattenConfigPatch(path []string, raw json.RawMessage, operations *[]configdoc.Operation) error {
	path = tomlPathForWebPath(path)
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if len(path) >= 2 && strings.HasSuffix(path[0], "_providers") && len(path) == 2 {
			*operations = append(*operations, configdoc.Operation{Path: append([]string(nil), path...), DeleteTable: true})
		} else {
			*operations = append(*operations, configdoc.Operation{Path: append([]string(nil), path...), Delete: true})
		}
		return nil
	}
	var object map[string]json.RawMessage
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := flattenConfigPatch(append(append([]string(nil), path...), key), object[key], operations); err != nil {
				return err
			}
		}
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("invalid patch at %s: %w", strings.Join(path, "."), err)
	}
	normalized, err := normalizeJSONValue(value)
	if err != nil {
		return fmt.Errorf("invalid patch at %s: %w", strings.Join(path, "."), err)
	}
	*operations = append(*operations, configdoc.Operation{Path: append([]string(nil), path...), Value: normalized})
	return nil
}

func validateWebConfigPatch(patch map[string]json.RawMessage) error {
	return validatePatchObject(reflect.TypeOf(Config{}), patch, nil)
}

func validatePatchObject(typ reflect.Type, patch map[string]json.RawMessage, path []string) error {
	if typ.Kind() == reflect.Map {
		elementType := typ.Elem()
		for elementType.Kind() == reflect.Pointer {
			elementType = elementType.Elem()
		}
		for key, raw := range patch {
			entryPath := append(path, key)
			if isCodeTTLMapPath(path) && !isBareTOMLKey(key) {
				return fmt.Errorf("%s: expected bare TOML key", strings.Join(entryPath, "."))
			}
			if strings.HasPrefix(key, "has_") {
				return fmt.Errorf("%s is a read-only status field", strings.Join(entryPath, "."))
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			if elementType.Kind() != reflect.Struct && elementType.Kind() != reflect.Map {
				if err := validateJSONScalarType(raw, elementType, entryPath); err != nil {
					return err
				}
				continue
			}
			var child map[string]json.RawMessage
			if err := json.Unmarshal(raw, &child); err != nil {
				return fmt.Errorf("%s must be an object", strings.Join(append(path, key), "."))
			}
			if err := validatePatchObject(elementType, child, append(path, key)); err != nil {
				return err
			}
		}
		return nil
	}
	for key, raw := range patch {
		if len(path) == 0 && key == "providers" {
			return fmt.Errorf("agent config field providers is unsupported; use model_providers")
		}
		if strings.HasPrefix(key, "has_") {
			return fmt.Errorf("%s is a read-only status field", strings.Join(append(path, key), "."))
		}
		if len(path) == 1 && path[0] == "hid" && key == "pointer_mode" {
			return fmt.Errorf("hid.pointer_mode is a read-only derived field")
		}
		fieldType, found := jsonFieldType(typ, key)
		if !found {
			return fmt.Errorf("unknown config field %s", strings.Join(append(path, key), "."))
		}
		base := fieldType
		for base.Kind() == reflect.Pointer {
			base = base.Elem()
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			if base.Kind() == reflect.Struct || base.Kind() == reflect.Map {
				return fmt.Errorf("%s must be an object", strings.Join(append(path, key), "."))
			}
			continue
		}
		if base.Kind() == reflect.Struct || base.Kind() == reflect.Map {
			var child map[string]json.RawMessage
			if err := json.Unmarshal(raw, &child); err != nil {
				return fmt.Errorf("%s must be an object", strings.Join(append(path, key), "."))
			}
			if err := validatePatchObject(base, child, append(path, key)); err != nil {
				return err
			}
		} else if err := validateJSONScalarType(raw, base, append(path, key)); err != nil {
			return err
		}
	}
	return nil
}

func validateJSONScalarType(raw json.RawMessage, typ reflect.Type, path []string) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("%s: invalid JSON value", strings.Join(path, "."))
	}
	if isNonNegativeIntegerPath(path) {
		number, ok := value.(json.Number)
		integer, err := number.Int64()
		if !ok || err != nil || integer < 0 {
			return fmt.Errorf("%s: expected non-negative integer", strings.Join(path, "."))
		}
	}
	expected := "value"
	valid := false
	switch typ.Kind() {
	case reflect.String:
		expected = "string"
		_, valid = value.(string)
	case reflect.Bool:
		expected = "bool"
		_, valid = value.(bool)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		expected = "number"
		number, ok := value.(json.Number)
		if !ok {
			break
		}
		if err := validateIntegerNumber(number, typ); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(path, "."), err)
		}
		valid = true
	case reflect.Float32, reflect.Float64:
		expected = "number"
		number, ok := value.(json.Number)
		if !ok {
			break
		}
		parsed, err := strconv.ParseFloat(string(number), typ.Bits())
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return fmt.Errorf("%s: number is out of range", strings.Join(path, "."))
		}
		valid = true
	case reflect.Slice, reflect.Array:
		expected = "array"
		_, valid = value.([]any)
	}
	if !valid {
		return fmt.Errorf("%s: expected %s, got %s", strings.Join(path, "."), expected, jsonValueType(value))
	}
	return nil
}

func validateIntegerNumber(number json.Number, typ reflect.Type) error {
	text := string(number)
	if strings.ContainsAny(text, ".eE") {
		return fmt.Errorf("expected integer")
	}
	if typ.Kind() >= reflect.Uint && typ.Kind() <= reflect.Uint64 {
		if _, err := strconv.ParseUint(text, 10, typ.Bits()); err != nil {
			return fmt.Errorf("integer is out of range")
		}
		return nil
	}
	if _, err := strconv.ParseInt(text, 10, typ.Bits()); err != nil {
		return fmt.Errorf("integer is out of range")
	}
	return nil
}

func isNonNegativeIntegerPath(path []string) bool {
	joined := strings.Join(path, ".")
	if joined == "voice_notifications.max_pending" ||
		joined == "voice_notifications.response_tail.max_items" ||
		joined == "voice_notifications.response_tail.max_text_chars" ||
		joined == "voice_notifications.expiration.default_ttl_seconds" {
		return true
	}
	return len(path) == 4 && path[0] == "voice_notifications" &&
		path[1] == "expiration" && path[2] == "code_ttl_seconds"
}

func jsonValueType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case json.Number:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	case nil:
		return "null"
	default:
		return "value"
	}
}

func isCodeTTLMapPath(path []string) bool {
	return len(path) == 3 && path[0] == "voice_notifications" && path[1] == "expiration" && path[2] == "code_ttl_seconds"
}

func isBareTOMLKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func jsonFieldType(typ reflect.Type, name string) (reflect.Type, bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == name {
			return field.Type, true
		}
	}
	return nil, false
}

func tomlPathForWebPath(path []string) []string {
	if len(path) < 2 || path[0] != "agent" {
		return path
	}
	return append([]string{path[1]}, path[2:]...)
}

func normalizeJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		if strings.ContainsAny(string(typed), ".eE") {
			f, err := typed.Float64()
			if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, fmt.Errorf("number is out of range")
			}
			return f, nil
		}
		if i, err := typed.Int64(); err == nil {
			return i, nil
		}
		if u, err := strconv.ParseUint(string(typed), 10, 64); err == nil {
			return u, nil
		}
		return nil, fmt.Errorf("integer is out of range")
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized, err := normalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			result[key] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}

// Source spelling remains lossless; reboot decisions use the resolved runtime
// values so aliases do not masquerade as USB/HID behavior changes.
func requiresConfigReboot(current, candidate agent.Config) bool {
	return current.PointerModeOrDefault() != candidate.PointerModeOrDefault() ||
		current.HID.KeyboardLayoutOrDefault() != candidate.HID.KeyboardLayoutOrDefault()
}
