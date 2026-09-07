package ble

import (
	"log"
	"time"

	"github.com/godbus/dbus/v5"
)

// Each discovery/subscription stage gets an initial attempt and five retries.
// New service objects may advance the stage even after its old budget expires.
var ancsRetryDelays = [...]time.Duration{
	time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second,
}

type ancsRetryState struct {
	device   dbus.ObjectPath
	stage    string
	attempts int
	next     time.Time
	stopped  bool
}

func (r *ancsRetryState) begin(device dbus.ObjectPath, stage string, now time.Time) bool {
	if r.device != device || r.stage != stage {
		*r = ancsRetryState{device: device, stage: stage}
	}
	if r.stopped || (!r.next.IsZero() && now.Before(r.next)) {
		return false
	}
	r.attempts++
	r.next = time.Time{}
	return true
}

func (r *ancsRetryState) failed(now time.Time, terminal bool) string {
	if terminal {
		r.stopped = true
		return "subscription_rejected"
	}
	if r.attempts > len(ancsRetryDelays) {
		r.stopped = true
		return "retry_exhausted"
	}
	r.next = now.Add(ancsRetryDelays[r.attempts-1])
	return r.stage
}

func (b *blueZBackend) ancsRetryDue(now time.Time) bool {
	b.ancsRecoveryMu.Lock()
	defer b.ancsRecoveryMu.Unlock()
	return !b.ancsRetry.next.IsZero() && !now.Before(b.ancsRetry.next)
}

// A failed GetManagedObjects must consume the same bounded retry budget as a
// missing service; otherwise an overdue retry would poll a broken bus forever.
func (b *blueZBackend) retryANCSScanFailure(err error) {
	b.ancsRecoveryMu.Lock()
	defer b.ancsRecoveryMu.Unlock()
	r := &b.ancsRetry
	if r.stage != "" && r.begin(r.device, r.stage, time.Now()) {
		b.failANCSAttempt(err.Error(), false)
	}
}

// Caller holds ancsRecoveryMu. Keep ANCS diagnostics separate from LastError:
// a later Wake success must not hide an outstanding notification failure.
func (b *blueZBackend) recordANCSState(state, reason string) {
	attempt := max(0, b.ancsRetry.attempts-1)
	changed := false
	b.service.status.update(func(status *RuntimeStatus) {
		changed = status.ANCSState != state || status.ANCSRetryAttempt != attempt || status.ANCSLastError != reason
		status.ANCSState = state
		status.ANCSRetryAttempt = attempt
		status.ANCSLastError = reason
	})
	if changed {
		log.Printf("BLE ANCS state=%s retry_attempt=%d reason=%q", state, attempt, reason)
	}
}

func (b *blueZBackend) failANCSAttempt(reason string, terminal bool) {
	state := b.ancsRetry.failed(time.Now(), terminal)
	b.recordANCSState(state, reason)
}

func (b *blueZBackend) resetANCSRecovery() {
	b.ancsRecoveryMu.Lock()
	defer b.ancsRecoveryMu.Unlock()
	b.ancsRetry = ancsRetryState{}
	b.recordANCSState("disconnected", "")
}

func terminalANCSSubscribeError(err error) bool {
	return isDBusErrorNamed(err, "org.bluez.Error.NotAuthorized") ||
		isDBusErrorNamed(err, "org.bluez.Error.NotPermitted") ||
		isDBusErrorNamed(err, "org.bluez.Error.NotSupported")
}
