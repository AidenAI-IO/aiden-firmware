package agent

import (
	"strconv"
	"strings"
	"time"
)

func memoryItemExpired(item MemoryItem, now time.Time) bool {
	return memoryExpiresAtPassed(item.ExpiresAt, now)
}

func memoryExpiresAtPassed(expiresAt string, now time.Time) bool {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return false
	}
	ts, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		if fallback, fallbackErr := time.Parse(time.RFC3339, expiresAt); fallbackErr == nil {
			ts = fallback
		} else {
			return false
		}
	}
	return !ts.After(now)
}

func parseRetentionDuration(value string) (time.Duration, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "forever" {
		return 0, false
	}
	unit := value[len(value)-1]
	number := strings.TrimSpace(value[:len(value)-1])
	if number == "" {
		return 0, false
	}
	n, err := strconv.Atoi(number)
	if err != nil || n <= 0 {
		return 0, false
	}
	switch unit {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, true
	case 'h':
		return time.Duration(n) * time.Hour, true
	case 'm':
		return time.Duration(n) * time.Minute, true
	case 's':
		return time.Duration(n) * time.Second, true
	default:
		d, err := time.ParseDuration(value)
		return d, err == nil
	}
}
