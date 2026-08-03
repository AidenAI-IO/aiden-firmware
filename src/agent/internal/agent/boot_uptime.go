package agent

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// bootUptimePath is the kernel's monotonic since-boot clock. Overridable in tests.
var bootUptimePath = "/proc/uptime"

// bootTimelinePath is the per-boot profile written by /etc/init.d/rcS
// (see overlay/etc/aiden_boot_timeline.sh). The agent appends its own
// milestones so the whole boot lives in one file. Overridable in tests.
var bootTimelinePath = "/var/log/aiden_boot_timeline.log"

// BootUptime reports how long the kernel has been up. The second return value
// is false when /proc/uptime is unavailable or malformed, which is the normal
// case on non-Linux hosts and in unit tests.
func BootUptime() (time.Duration, bool) {
	data, err := os.ReadFile(bootUptimePath)
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}

// MarkBootTimeline appends a milestone to the boot timeline recorded by rcS,
// matching the "<uptime_s> <delta_s> mark <label>" record format.
//
// Best-effort by design: the timeline is a diagnostic, so a missing file (the
// usual case once the tmpfs log is rotated, or on a dev host) is not an error
// and never affects the caller.
func MarkBootTimeline(label string) {
	uptime, ok := BootUptime()
	if !ok {
		return
	}
	// Only append to an existing timeline: creating it here would produce a
	// stray file with no boot context on every agent restart.
	f, err := os.OpenFile(bootTimelinePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%.2f 0.00 mark %s\n", uptime.Seconds(), label)
}

// bootUptimeLogSuffix renders " uptime=12.34s" for log lines, or "" when the
// uptime is unavailable, so callers can append it unconditionally.
func bootUptimeLogSuffix() string {
	uptime, ok := BootUptime()
	if !ok {
		return ""
	}
	return fmt.Sprintf(" uptime=%.2fs", uptime.Seconds())
}
