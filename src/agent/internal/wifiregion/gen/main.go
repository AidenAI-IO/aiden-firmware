// Command gen writes the IANA timezone to country table used by wifiregion.
//
// It reads the tzdata zone.tab shipped with the host system (or a path given
// with -zone-tab) and emits a Go source file. Browsers report arbitrary IANA
// zones, so the table has to cover far more than the agent's own short list.
//
// Usage:
//
//	go run ./internal/wifiregion/gen -out internal/wifiregion/timezone_table.go
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	zoneTab := flag.String("zone-tab", "/usr/share/zoneinfo/zone.tab", "path to the tzdata zone.tab")
	out := flag.String("out", "", "output Go file (default: stdout)")
	flag.Parse()

	table, err := parseZoneTab(*zoneTab)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	source := render(table)
	if *out == "" {
		fmt.Print(source)
		return
	}
	if err := os.WriteFile(*out, []byte(source), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d entries)\n", *out, len(table))
}

// parseZoneTab reads the "country code, coordinates, zone" columns. A zone can
// appear once per country only, which is what makes the reverse mapping safe.
func parseZoneTab(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read zone.tab: %w", err)
	}
	defer file.Close()

	table := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		country, zone := strings.TrimSpace(fields[0]), strings.TrimSpace(fields[2])
		if len(country) != 2 || zone == "" {
			continue
		}
		// zone.tab lists a zone under exactly one country; keep the first so a
		// malformed duplicate cannot flip an established mapping.
		if _, exists := table[zone]; !exists {
			table[zone] = strings.ToUpper(country)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(table) == 0 {
		return nil, fmt.Errorf("no zones parsed from %s", path)
	}
	return table, nil
}

func render(table map[string]string) string {
	zones := make([]string, 0, len(table))
	for zone := range table {
		zones = append(zones, zone)
	}
	sort.Strings(zones)

	var out strings.Builder
	out.WriteString(`package wifiregion

// Code generated from the tzdata zone.tab. DO NOT EDIT.
//
// Regenerate with:
//
//	go generate ./internal/wifiregion/...
//
// The mapping is advisory: a timezone tells you where the operator's browser
// thinks it is, never where this hardware is. It only ever preselects a choice.
var ianaTimezoneCountry = map[string]string{
`)
	for _, zone := range zones {
		fmt.Fprintf(&out, "\t%q: %q,\n", zone, table[zone])
	}
	out.WriteString("}\n")
	return out.String()
}
