package realtimevoice

import (
	"fmt"
	"strings"
)

func terminalResponseEvent(provider, responseID, status string, usage Usage) Event {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "completed":
		return Event{Kind: EventResponseDone, ResponseID: responseID, Status: status, Usage: usage}
	case "cancelled", "canceled":
		return Event{Kind: EventResponseCancelled, ResponseID: responseID, Status: status, Usage: usage}
	default:
		return Event{
			Kind:       EventError,
			ResponseID: responseID,
			Status:     status,
			Usage:      usage,
			Error:      fmt.Errorf("%s realtime response %q ended with status %q", provider, responseID, status),
		}
	}
}
