// Package wifiregion resolves the Wi-Fi regulatory domain (country code) that
// the device should run with, and records where that value came from.
//
// The wpa_supplicant configuration file is the single authoritative state: the
// sidecar written by this package only carries provenance metadata and must
// never be used to rewrite the configuration file. See
// docs/02-architecture/wifi-regulatory-domain-plan.md.
package wifiregion

import (
	"sort"
	"strings"
)

// Source records how a country code was chosen. Earlier entries win in Resolve.
type Source string

const (
	// SourceUser is the only source produced by an explicit user confirmation.
	SourceUser Source = "user"
	// SourceExisting marks a valid value found in the configuration file of a
	// device that predates the sidecar.
	SourceExisting Source = "existing"
	// SourceBeacon marks a value derived from 802.11d country information
	// elements observed during a scan.
	SourceBeacon Source = "beacon"
	// SourceBrowserTZ marks a value derived from the IANA timezone reported by
	// the browser driving the configuration portal.
	SourceBrowserTZ Source = "browser_tz"
	// SourceTimezone marks a value derived from the agent's configured timezone.
	SourceTimezone Source = "timezone"
	// SourceFactory marks the default written by the first-boot migration.
	SourceFactory Source = "factory"
	// SourceUnknown marks a value whose provenance was lost, for example after a
	// restore or a power cut between the two writes.
	SourceUnknown Source = "unknown"
)

// WorldCountry is accepted when it is already present in a configuration file,
// but it is never offered as a choice: on the AIC8800 driver shipped with this
// board the "00" regulatory domain is the most permissive rule set rather than
// a conservative world domain.
const WorldCountry = "00"

// FactoryCountry is used only when no country exists anywhere: no configuration
// file, no sidecar, and nothing detected. Refusing to configure Wi-Fi at all in
// that state would make a device with a missing or corrupt configuration
// unreachable over Wi-Fi, including through the recovery portal.
//
// This is not the silent substitution this package exists to prevent. That was
// replacing a value someone had specified; this supplies one where none exists,
// and labels it SourceFactory so the portal shows where it came from and offers
// to change it. It matches the default written by the first-boot migration.
const FactoryCountry = "CN"

// beaconVoteThreshold is the number of distinct access points that must agree
// before a scan result is strong enough to be applied without asking.
const beaconVoteThreshold = 3

// Normalize upper-cases and validates a country code. The second return value
// reports whether the input was usable; callers must not silently substitute a
// default when it is false.
func Normalize(value string) (string, bool) {
	value = strings.ToUpper(strings.TrimSpace(value))
	if !IsSupported(value) {
		return "", false
	}
	return value, true
}

// IsSupported reports whether a country code is known to the Wi-Fi driver's
// regulatory database. WorldCountry is supported so that an existing
// configuration file carrying it keeps working, but Options never offers it.
func IsSupported(code string) bool {
	_, ok := supportedCountries[strings.ToUpper(strings.TrimSpace(code))]
	return ok
}

// Options returns the country codes a user may choose from, sorted. Labels are
// localized by the client, which knows the viewer's locale.
func Options() []string {
	options := make([]string, 0, len(supportedCountries))
	for code := range supportedCountries {
		if code == WorldCountry {
			continue
		}
		options = append(options, code)
	}
	sort.Strings(options)
	return options
}

// VoteBeacon picks a country from per-country counts of distinct access points.
// It requires a unique winner that clears beaconVoteThreshold, because a single
// access point proves nothing and a tie proves less.
//
// Callers must deduplicate by BSSID prefix before counting: one physical access
// point hosting several SSIDs would otherwise cast several votes. Even then the
// result only describes the rules the surrounding radios are using, which is
// not proof of where the device is.
func VoteBeacon(votes map[string]int) (string, bool) {
	best, bestCount, runnerUp := "", 0, 0
	for code, count := range votes {
		if !IsSupported(code) || code == WorldCountry {
			continue
		}
		switch {
		case count > bestCount:
			best, bestCount, runnerUp = code, count, bestCount
		case count > runnerUp:
			runnerUp = count
		}
	}
	if bestCount < beaconVoteThreshold || bestCount == runnerUp {
		return "", false
	}
	return best, true
}

// Inputs carries everything Resolve needs. It performs no I/O so that the
// decision itself stays trivially testable.
type Inputs struct {
	// Saved is the sidecar state, already reconciled against the configuration
	// file by ReconcileState.
	Saved State
	// CurrentCountry is the value currently in the configuration file.
	CurrentCountry string
	// ScanVotes counts distinct access points per country code.
	ScanVotes map[string]int
	// BrowserTimezone is the IANA zone reported by the portal client, if any.
	BrowserTimezone string
	// AgentTimezone is the agent's configured timezone. "UTC" carries no
	// location information and is ignored.
	AgentTimezone string
}

// Decision is the outcome of the chain. Callers decide whether to persist it by
// inspecting Source: only SourceBeacon may be applied without asking, and only
// when the operator has enabled that behavior.
type Decision struct {
	Country string
	Source  Source
}

// Applicable reports whether a decision may be written without an explicit user
// confirmation. Timezone-derived values are user-interface candidates only;
// they never describe where the hardware actually is.
func (d Decision) Applicable(autoApplyBeacon bool) bool {
	return d.Source == SourceBeacon && autoApplyBeacon && d.Country != ""
}

// Resolve runs the provenance chain. It never invents a value: when nothing
// stronger is available it returns the country already in effect, so a device
// that cannot determine its region keeps working with whatever it has.
func Resolve(in Inputs) Decision {
	if in.Saved.Source == SourceUser {
		if country, ok := Normalize(in.Saved.Country); ok {
			return Decision{country, SourceUser}
		}
	}
	if in.Saved.Source == SourceExisting {
		if country, ok := Normalize(in.Saved.Country); ok {
			return Decision{country, SourceExisting}
		}
	}
	if country, ok := VoteBeacon(in.ScanVotes); ok {
		return Decision{country, SourceBeacon}
	}
	if country, ok := CountryForTimezone(in.BrowserTimezone); ok {
		return Decision{country, SourceBrowserTZ}
	}
	if country, ok := CountryForAgentTimezone(in.AgentTimezone); ok {
		return Decision{country, SourceTimezone}
	}
	if country, ok := Normalize(in.CurrentCountry); ok {
		return Decision{country, SourceUnknown}
	}
	if country, ok := Normalize(in.Saved.Country); ok {
		return Decision{country, in.Saved.Source}
	}
	return Decision{FactoryCountry, SourceFactory}
}

// Candidates returns every suggestion the chain can offer, strongest first, for
// display in the configuration portal. Unlike Resolve it does not stop at the
// first hit, so the user can see and choose among the alternatives.
func Candidates(in Inputs) []Decision {
	candidates := []Decision{}
	seen := map[string]bool{}
	add := func(country string, source Source) {
		if country == "" || seen[country] {
			return
		}
		seen[country] = true
		candidates = append(candidates, Decision{country, source})
	}
	if country, ok := VoteBeacon(in.ScanVotes); ok {
		add(country, SourceBeacon)
	}
	if country, ok := CountryForTimezone(in.BrowserTimezone); ok {
		add(country, SourceBrowserTZ)
	}
	if country, ok := CountryForAgentTimezone(in.AgentTimezone); ok {
		add(country, SourceTimezone)
	}
	return candidates
}
