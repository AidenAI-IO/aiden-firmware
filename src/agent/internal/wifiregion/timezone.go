package wifiregion

import "strings"

//go:generate go run ./gen -out timezone_table.go

// utcTimezone carries no location information and is the agent's default, so it
// never contributes a country.
const utcTimezone = "UTC"

// agentTimezoneCountry maps the timezones the agent itself offers to a country.
// It is deliberately written out rather than derived from ianaTimezoneCountry:
// the agent's list is short, reviewed, and a test asserts it stays in step with
// agent.SupportedTimezones(), so an upstream addition cannot silently fall
// through to a lookup that happens to succeed.
//
// "UTC" is absent on purpose.
var agentTimezoneCountry = map[string]string{
	"Africa/Johannesburg": "ZA",
	"America/Chicago":     "US",
	"America/Denver":      "US",
	"America/Los_Angeles": "US",
	"America/New_York":    "US",
	"America/Sao_Paulo":   "BR",
	"America/Toronto":     "CA",
	"America/Vancouver":   "CA",
	"Asia/Bangkok":        "TH",
	"Asia/Dubai":          "AE",
	"Asia/Hong_Kong":      "HK",
	"Asia/Jakarta":        "ID",
	"Asia/Kolkata":        "IN",
	"Asia/Kuala_Lumpur":   "MY",
	"Asia/Seoul":          "KR",
	"Asia/Shanghai":       "CN",
	"Asia/Singapore":      "SG",
	"Asia/Taipei":         "TW",
	"Asia/Tokyo":          "JP",
	"Australia/Sydney":    "AU",
	"Europe/Amsterdam":    "NL",
	"Europe/Berlin":       "DE",
	"Europe/London":       "GB",
	"Europe/Moscow":       "RU",
	"Europe/Paris":        "FR",
	"Pacific/Auckland":    "NZ",
}

// CountryForAgentTimezone maps the agent's configured timezone to a country.
// It reports false for UTC, for unknown zones, and for zones whose country the
// Wi-Fi driver does not recognize.
func CountryForAgentTimezone(timezone string) (string, bool) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || timezone == utcTimezone {
		return "", false
	}
	return Normalize(agentTimezoneCountry[timezone])
}

// CountryForTimezone maps an arbitrary IANA timezone, such as one reported by a
// browser, to a country. Unknown zones report false; no prefix guessing is
// attempted, because a continent does not identify a regulatory domain.
func CountryForTimezone(timezone string) (string, bool) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" || timezone == utcTimezone {
		return "", false
	}
	return Normalize(ianaTimezoneCountry[timezone])
}
