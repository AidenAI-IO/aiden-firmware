package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"aiden-agent/internal/agent"
)

// runNotifications exposes the persisted notification log to shell users.
// It deliberately creates a read-only NotificationContext query path and
// never calls ble_service or changes either notification cursor.
func runNotifications(args []string) int {
	return runNotificationsIO(args, os.Stdout, os.Stderr)
}

func runNotificationsIO(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "list" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("notifications", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("dir", "/userdata/agent", "agent data directory containing memory/notifications")
	since := fs.String("since", "", "return records with an id greater than this cursor")
	date := fs.String("date", "", "filter by received date (YYYY-MM-DD)")
	app := fs.String("app", "", "filter by app identifier")
	text := fs.String("text", "", "case-insensitive search across notification text fields")
	limit := fs.Int("limit", 20, "maximum records to return")
	format := fs.String("format", "json", "output format: json or jsonl")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 1 || (fs.NArg() == 1 && fs.Arg(0) != "list") {
		fmt.Fprintln(stderr, "usage: agent notifications [list] [--dir DIR] [--since ID] [--date YYYY-MM-DD] [--app APP] [--text TEXT] [--limit N] [--format json|jsonl]")
		return 2
	}
	if *limit < 1 || *limit > 1000 {
		fmt.Fprintln(stderr, "notifications: --limit must be between 1 and 1000")
		return 2
	}
	if *format != "json" && *format != "jsonl" {
		fmt.Fprintln(stderr, "notifications: --format must be json or jsonl")
		return 2
	}
	results, err := agent.QueryNotificationRecords(nil, filepath.Join(strings.TrimSpace(*dataDir), "memory"), agent.NotificationQuery{
		Since:         *since,
		Limit:         *limit,
		Date:          *date,
		AppIdentifier: *app,
		Text:          *text,
	})
	if err != nil {
		fmt.Fprintf(stderr, "notifications: query: %v\n", err)
		return 1
	}
	if *format == "jsonl" {
		for _, event := range results {
			if err := json.NewEncoder(stdout).Encode(event); err != nil {
				fmt.Fprintf(stderr, "notifications: encode: %v\n", err)
				return 1
			}
		}
		return 0
	}
	if err := json.NewEncoder(stdout).Encode(map[string]any{
		"ok":     true,
		"count":  len(results),
		"events": results,
	}); err != nil {
		fmt.Fprintf(stderr, "notifications: encode: %v\n", err)
		return 1
	}
	return 0
}
