package wifiregion

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// DefaultStatePath is the sidecar that records where the country code came
	// from. It is metadata only; see ReconcileState.
	DefaultStatePath = "/userdata/system/wifi-region.json"
	// StateVersion is the on-disk schema version.
	StateVersion = 1

	maxStateSize = 64 * 1024
)

// State is the persisted provenance of the configured country code.
type State struct {
	Version    int            `json:"version"`
	Country    string         `json:"country,omitempty"`
	Source     Source         `json:"source,omitempty"`
	DetectedAt time.Time      `json:"detected_at,omitempty"`
	Votes      map[string]int `json:"votes,omitempty"`
}

// EmptyState is the state of a device that has never recorded provenance.
func EmptyState() State {
	return State{Version: StateVersion, Source: SourceUnknown}
}

// LoadState reads the sidecar. A missing file is not an error: provenance is
// optional metadata, and the device must configure Wi-Fi without it. A corrupt
// or unreadable file degrades to EmptyState with the error reported, so callers
// can log it without failing the request.
func LoadState(path string) (State, error) {
	if strings.TrimSpace(path) == "" {
		return EmptyState(), errors.New("Wi-Fi region state path is empty")
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return EmptyState(), nil
	}
	if err != nil {
		return EmptyState(), err
	}
	if len(data) > maxStateSize {
		return EmptyState(), fmt.Errorf("Wi-Fi region state exceeds %d bytes", maxStateSize)
	}
	state := EmptyState()
	if err := json.Unmarshal(data, &state); err != nil {
		return EmptyState(), fmt.Errorf("parse Wi-Fi region state: %w", err)
	}
	if state.Version != StateVersion {
		return EmptyState(), fmt.Errorf("unsupported Wi-Fi region state version %d", state.Version)
	}
	if country, ok := Normalize(state.Country); ok {
		state.Country = country
	} else {
		state.Country = ""
		state.Source = SourceUnknown
	}
	if !knownSource(state.Source) {
		state.Source = SourceUnknown
	}
	return state, nil
}

// SaveState writes the sidecar atomically. It is always called after the
// wpa_supplicant configuration has been persisted, and a failure here must not
// roll that back: the worst outcome is provenance showing as unknown.
func SaveState(path string, state State) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("Wi-Fi region state path is empty")
	}
	state.Version = StateVersion
	if country, ok := Normalize(state.Country); ok {
		state.Country = country
	} else {
		return fmt.Errorf("refusing to persist invalid Wi-Fi country %q", state.Country)
	}
	if !knownSource(state.Source) {
		state.Source = SourceUnknown
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

// ReconcileState aligns the sidecar with the authoritative configuration file.
//
// The configuration file always wins. A sidecar that disagrees with it is stale
// — a restore, or a power cut between the two writes — and adopting its country
// would change what the radio does based on metadata. Instead the country is
// corrected and the provenance downgraded to unknown, which costs the user
// nothing but a less specific label in the portal.
//
// A device with a valid country and no sidecar at all predates this feature, so
// its configuration is treated as deliberate and marked SourceExisting; that is
// what keeps an upgrade from re-deciding a fleet's regulatory domain. The
// first-boot migration writes a SourceFactory sidecar precisely so that a
// freshly provisioned default is not mistaken for such a device.
//
// The second return value reports whether the caller should persist the result.
func ReconcileState(state State, configCountry string) (State, bool) {
	country, valid := Normalize(configCountry)
	if !valid {
		// Nothing authoritative to anchor to; keep the sidecar as it is rather
		// than inventing provenance for a value that will itself be replaced.
		return state, false
	}
	if state.Country == "" && state.Source == SourceUnknown {
		return State{Version: StateVersion, Country: country, Source: SourceExisting}, true
	}
	if state.Country != country {
		return State{Version: StateVersion, Country: country, Source: SourceUnknown}, true
	}
	return state, false
}

func knownSource(source Source) bool {
	switch source {
	case SourceUser, SourceExisting, SourceBeacon, SourceBrowserTZ,
		SourceTimezone, SourceFactory, SourceUnknown:
		return true
	default:
		return false
	}
}
