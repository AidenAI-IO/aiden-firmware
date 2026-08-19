package agent

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

const phoneBridgeHIDConnectionPollInterval = 500 * time.Millisecond

func readHIDConnectionState() (bool, bool) {
	controllers, err := filepath.Glob("/sys/class/udc/*/state")
	if err != nil || len(controllers) == 0 {
		return false, false
	}
	known := false
	for _, path := range controllers {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		connected, stateKnown := hidUDCStateConnected(string(data))
		if !stateKnown {
			continue
		}
		known = true
		if connected {
			return true, true
		}
	}
	return false, known
}

func hidUDCStateConnected(state string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "attached", "powered", "reconnecting", "unauthenticated", "default", "addressed", "configured", "suspended":
		return true, true
	case "not attached":
		return false, true
	default:
		return false, false
	}
}

func (pb *PhoneBridge) ensureHIDConnectionMonitor() {
	if pb == nil || !pb.hidMonitorEnabled {
		return
	}
	pb.hidMonitorOnce.Do(func() {
		pb.mu.Lock()
		pb.refreshHIDConnectionLocked()
		known := pb.hidConnectionKnown
		pb.mu.Unlock()
		if !known {
			return
		}

		go func() {
			ticker := time.NewTicker(phoneBridgeHIDConnectionPollInterval)
			defer ticker.Stop()
			for range ticker.C {
				pb.mu.Lock()
				beforeID := pb.hidConnectionID
				beforeConnected := pb.hidConnected
				pb.refreshHIDConnectionLocked()
				changed := beforeID != pb.hidConnectionID || beforeConnected != pb.hidConnected
				pb.mu.Unlock()
				if changed {
					pb.notifyEnvironmentObserver()
				}
			}
		}()
	})
}
