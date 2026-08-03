package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withBootUptimeFile(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "uptime")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write uptime: %v", err)
	}
	original := bootUptimePath
	bootUptimePath = path
	t.Cleanup(func() { bootUptimePath = original })
}

func TestBootUptimeParsesProcUptime(t *testing.T) {
	withBootUptimeFile(t, "7717.92 6832.62\n")

	uptime, ok := BootUptime()
	if !ok {
		t.Fatal("expected uptime to parse")
	}
	if got := uptime.Seconds(); got < 7717.91 || got > 7717.93 {
		t.Fatalf("uptime = %v, want ~7717.92", got)
	}
}

func TestBootUptimeRejectsUnusableInput(t *testing.T) {
	cases := map[string]string{
		"empty":     "",
		"blank":     "   \n",
		"malformed": "not-a-number 1.0\n",
		"negative":  "-5.0 1.0\n",
	}
	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			withBootUptimeFile(t, contents)
			if _, ok := BootUptime(); ok {
				t.Fatalf("expected %s input to be rejected", name)
			}
		})
	}
}

func TestBootUptimeMissingFile(t *testing.T) {
	original := bootUptimePath
	bootUptimePath = filepath.Join(t.TempDir(), "does-not-exist")
	t.Cleanup(func() { bootUptimePath = original })

	if _, ok := BootUptime(); ok {
		t.Fatal("expected missing /proc/uptime to report not-ok")
	}
}

func TestMarkBootTimelineAppendsRecord(t *testing.T) {
	withBootUptimeFile(t, "51.18 40.00\n")

	timeline := filepath.Join(t.TempDir(), "aiden_boot_timeline.log")
	if err := os.WriteFile(timeline, []byte("0.10 0.10 rcS:begin\n"), 0o644); err != nil {
		t.Fatalf("seed timeline: %v", err)
	}
	original := bootTimelinePath
	bootTimelinePath = timeline
	t.Cleanup(func() { bootTimelinePath = original })

	MarkBootTimeline("agent:listening")

	data, err := os.ReadFile(timeline)
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "0.10 0.10 rcS:begin") {
		t.Fatalf("existing records were lost: %q", got)
	}
	if !strings.Contains(got, "51.18 0.00 mark agent:listening") {
		t.Fatalf("missing milestone record: %q", got)
	}
}

// The timeline belongs to a boot. An agent restart must not resurrect it,
// otherwise the file would hold records with no boot context.
func TestMarkBootTimelineDoesNotCreateFile(t *testing.T) {
	withBootUptimeFile(t, "51.18 40.00\n")

	timeline := filepath.Join(t.TempDir(), "absent.log")
	original := bootTimelinePath
	bootTimelinePath = timeline
	t.Cleanup(func() { bootTimelinePath = original })

	MarkBootTimeline("agent:listening")

	if _, err := os.Stat(timeline); !os.IsNotExist(err) {
		t.Fatalf("expected no timeline file to be created, stat err = %v", err)
	}
}

func TestBootUptimeLogSuffix(t *testing.T) {
	withBootUptimeFile(t, "50.70 1.00\n")
	if got := bootUptimeLogSuffix(); got != " uptime=50.70s" {
		t.Fatalf("suffix = %q", got)
	}

	original := bootUptimePath
	bootUptimePath = filepath.Join(t.TempDir(), "missing")
	t.Cleanup(func() { bootUptimePath = original })
	if got := bootUptimeLogSuffix(); got != "" {
		t.Fatalf("expected empty suffix when uptime unavailable, got %q", got)
	}
}
