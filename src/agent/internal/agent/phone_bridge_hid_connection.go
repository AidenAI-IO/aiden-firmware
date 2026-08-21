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
	pb.hidMonitorMu.Lock()
	if pb.hidMonitorRunning {
		pb.hidMonitorMu.Unlock()
		return
	}
	stop := pb.hidMonitorStop
	if stop == nil {
		stop = make(chan struct{})
		pb.hidMonitorStop = stop
	}
	select {
	case <-stop:
		pb.hidMonitorMu.Unlock()
		return
	default:
	}
	pb.refreshHIDConnectionNow()
	pb.mu.Lock()
	known := pb.hidConnectionKnown
	pb.mu.Unlock()
	if !known {
		pb.hidMonitorMu.Unlock()
		return
	}
	pb.hidMonitorRunning = true
	pb.hidMonitorMu.Unlock()

	go func(stop <-chan struct{}) {
		ticker := time.NewTicker(phoneBridgeHIDConnectionPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				pb.hidMonitorMu.Lock()
				pb.hidMonitorRunning = false
				pb.hidMonitorMu.Unlock()
				return
			case <-ticker.C:
				pb.mu.Lock()
				beforeID := pb.hidConnectionID
				beforeConnected := pb.hidConnected
				pb.mu.Unlock()
				pb.refreshHIDConnectionNow()
				pb.mu.Lock()
				changed := beforeID != pb.hidConnectionID || beforeConnected != pb.hidConnected
				pb.mu.Unlock()
				if changed {
					pb.notifyEnvironmentObserver()
				}
			}
		}
	}(stop)
}
