package agent

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	_ "time/tzdata"
)

const defaultTimezone = "UTC"

// POSIX TZ values keep child processes on the configured timezone even on the
// embedded image, which does not ship an IANA zoneinfo database. Go uses the
// bundled time/tzdata copy for the same IANA names.
var supportedTimezones = map[string]string{
	"UTC":                 "UTC0",
	"Africa/Johannesburg": "SAST-2",
	"America/Chicago":     "CST6CDT,M3.2.0/2,M11.1.0/2",
	"America/Denver":      "MST7MDT,M3.2.0/2,M11.1.0/2",
	"America/Los_Angeles": "PST8PDT,M3.2.0/2,M11.1.0/2",
	"America/New_York":    "EST5EDT,M3.2.0/2,M11.1.0/2",
	"America/Sao_Paulo":   "BRT3",
	"America/Toronto":     "EST5EDT,M3.2.0/2,M11.1.0/2",
	"America/Vancouver":   "PST8PDT,M3.2.0/2,M11.1.0/2",
	"Asia/Bangkok":        "ICT-7",
	"Asia/Dubai":          "GST-4",
	"Asia/Hong_Kong":      "HKT-8",
	"Asia/Jakarta":        "WIB-7",
	"Asia/Kolkata":        "IST-5:30",
	"Asia/Kuala_Lumpur":   "MYT-8",
	"Asia/Seoul":          "KST-9",
	"Asia/Shanghai":       "CST-8",
	"Asia/Singapore":      "SGT-8",
	"Asia/Taipei":         "CST-8",
	"Asia/Tokyo":          "JST-9",
	"Australia/Sydney":    "AEST-10AEDT,M10.1.0,M4.1.0/3",
	"Europe/Amsterdam":    "CET-1CEST,M3.5.0/2,M10.5.0/3",
	"Europe/Berlin":       "CET-1CEST,M3.5.0/2,M10.5.0/3",
	"Europe/London":       "GMT0BST,M3.5.0/1,M10.5.0/2",
	"Europe/Moscow":       "MSK-3",
	"Europe/Paris":        "CET-1CEST,M3.5.0/2,M10.5.0/3",
	"Pacific/Auckland":    "NZST-12NZDT,M9.5.0,M4.1.0/3",
}

func SupportedTimezones() []string {
	values := make([]string, 0, len(supportedTimezones))
	for timezone := range supportedTimezones {
		values = append(values, timezone)
	}
	sort.Strings(values)
	return values
}

func (c Config) TimezoneOrDefault() string {
	timezone := strings.TrimSpace(c.Timezone)
	if timezone == "" {
		return defaultTimezone
	}
	return timezone
}

func loadConfiguredTimezone(timezone string) (*time.Location, string, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultTimezone
	}
	posix, ok := supportedTimezones[timezone]
	if !ok {
		return nil, "", fmt.Errorf("unsupported timezone: %s", timezone)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, "", fmt.Errorf("load timezone %s: %w", timezone, err)
	}
	return location, posix, nil
}

// ApplyTimezone configures the environment inherited by shell tools and other
// child processes. User-facing Go timestamps use the explicit IANA location
// returned by configuredTime, avoiding a process-global time.Local mutation
// while configuration is hot-reloaded.
func ApplyTimezone(cfg Config) error {
	_, posix, err := loadConfiguredTimezone(cfg.TimezoneOrDefault())
	if err != nil {
		return err
	}
	if err := os.Setenv("TZ", posix); err != nil {
		return fmt.Errorf("set timezone environment: %w", err)
	}
	return nil
}

func configuredTime(value time.Time, timezone string) time.Time {
	location, _, err := loadConfiguredTimezone(timezone)
	if err != nil {
		location = time.UTC
	}
	return value.In(location)
}
