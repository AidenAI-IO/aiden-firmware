package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"aiden-agent/internal/ota"
)

type statusReport struct {
	State    ota.State  `json:"state"`
	ABData   ota.ABData `json:"ab_data"`
	Active   ota.Slot   `json:"active_slot"`
	ActiveOK bool       `json:"active_slot_ok"`
}

func writeStatus(out io.Writer, report statusReport) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "OTA Status")
	fmt.Fprintf(tw, "  Phase\t%s\n", valueOrDash(report.State.Phase))
	fmt.Fprintf(tw, "  Current\t%s\n", formatVersionBuild(report.State.CurrentVersion, report.State.CurrentBuildTime))
	fmt.Fprintf(tw, "  Target\t%s\n", formatVersionBuild(report.State.TargetVersion, report.State.TargetBuildTime))
	fmt.Fprintf(tw, "  Last committed\t%s\n", formatVersionBuild(report.State.LastCommittedVersion, report.State.LastCommittedBuildTime))
	fmt.Fprintf(tw, "  State active slot\t%s\n", formatSlot(report.State.ActiveSlot))
	fmt.Fprintf(tw, "  State target slot\t%s\n", formatStateTargetSlot(report.State))
	fmt.Fprintf(tw, "  Last error\t%s\n", valueOrDash(report.State.LastError))
	fmt.Fprintf(tw, "  Retry\t%s\n", formatRetry(report.State.Retry))
	fmt.Fprintf(tw, "  Pending boot\t%s\n", formatPendingBoot(report.State))

	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "A/B Metadata")
	fmt.Fprintf(tw, "  Version\t%d.%d\n", report.ABData.VersionMajor, report.ABData.VersionMinor)
	fmt.Fprintf(tw, "  Active slot\t%s\n", formatActiveSlot(report.Active, report.ActiveOK))
	fmt.Fprintf(tw, "  Last boot\t%s\n", formatSlot(report.ABData.LastBoot))
	fmt.Fprintf(tw, "  CRC32\t%08x\n", report.ABData.CRC32)
	fmt.Fprintln(tw, "  Slot\tPriority\tTries\tSuccessful\tBootable")
	for _, slot := range []ota.Slot{ota.SlotA, ota.SlotB} {
		data := report.ABData.Slots[slot]
		fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\t%s\n",
			formatSlot(slot),
			data.Priority,
			data.TriesRemaining,
			yesNo(data.SuccessfulBoot),
			yesNo(report.ABData.Bootable(slot)),
		)
	}

	fmt.Fprintln(tw)
	fmt.Fprintln(tw, "Partitions")
	if len(report.State.Slots) == 0 {
		fmt.Fprintln(tw, "  -")
		return tw.Flush()
	}
	fmt.Fprintln(tw, "  Slot\tPartition\tVersion\tSHA256")
	for _, slotName := range sortedKeys(report.State.Slots) {
		slot := report.State.Slots[slotName]
		for _, partName := range sortedKeys(slot.Partitions) {
			part := slot.Partitions[partName]
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
				slotName,
				partName,
				valueOrDash(part.Version),
				valueOrDash(part.Hash),
			)
		}
	}
	return tw.Flush()
}

func formatVersionBuild(version string, buildTime string) string {
	version = strings.TrimSpace(version)
	buildTime = strings.TrimSpace(buildTime)
	switch {
	case version == "" && buildTime == "":
		return "-"
	case version == "":
		return buildTime
	case buildTime == "":
		return version
	default:
		return fmt.Sprintf("%s (%s)", version, buildTime)
	}
}

func formatRetry(retry ota.RetryMetadata) string {
	if retry.Count == 0 && strings.TrimSpace(retry.NextAt) == "" && strings.TrimSpace(retry.LastReason) == "" {
		return "-"
	}
	parts := []string{}
	if retry.Count != 0 {
		parts = append(parts, fmt.Sprintf("count=%d", retry.Count))
	}
	if strings.TrimSpace(retry.NextAt) != "" {
		parts = append(parts, "next="+strings.TrimSpace(retry.NextAt))
	}
	if strings.TrimSpace(retry.LastReason) != "" {
		parts = append(parts, "reason="+strings.TrimSpace(retry.LastReason))
	}
	return strings.Join(parts, ", ")
}

func formatStateTargetSlot(state ota.State) string {
	if state.Phase != "writing" &&
		state.Phase != "pending-reboot" &&
		strings.TrimSpace(state.TargetVersion) == "" &&
		strings.TrimSpace(state.TargetBuildTime) == "" {
		return "-"
	}
	return formatSlot(state.TargetSlot)
}

func formatPendingBoot(state ota.State) string {
	if state.Phase != "pending-reboot" &&
		strings.TrimSpace(state.PendingBootNonce) == "" &&
		strings.TrimSpace(state.PendingBootID) == "" &&
		state.PendingTargetSlot == nil {
		return "-"
	}
	parts := []string{}
	if state.TargetSlot == ota.SlotA || state.TargetSlot == ota.SlotB {
		parts = append(parts, "target_slot="+formatSlot(state.TargetSlot))
	}
	if strings.TrimSpace(state.PendingBootNonce) != "" {
		parts = append(parts, "nonce="+strings.TrimSpace(state.PendingBootNonce))
	}
	if strings.TrimSpace(state.PendingBootID) != "" {
		parts = append(parts, "boot_id="+strings.TrimSpace(state.PendingBootID))
	}
	if state.PendingTargetSlot != nil {
		parts = append(parts, "has_previous_target_snapshot=yes")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

func formatActiveSlot(slot ota.Slot, ok bool) string {
	if !ok {
		return "none"
	}
	return formatSlot(slot)
}

func formatSlot(slot ota.Slot) string {
	switch slot {
	case ota.SlotA:
		return "a"
	case ota.SlotB:
		return "b"
	default:
		return fmt.Sprintf("unknown(%d)", slot)
	}
}

func valueOrDash(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
