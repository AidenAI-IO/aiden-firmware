package main

import "time"

const realtimeResponseIdleTimeout = 60 * time.Second

// Owned by the session select loop. Only accepted assistant output and
// foreground tool progress renew the deadline, never mic traffic or heartbeats.
type realtimeResponseWatchdog struct {
	timeout      time.Duration
	timer        *time.Timer
	deadline     <-chan time.Time
	startedAt    time.Time
	lastProgress time.Time
}

func (w *realtimeResponseWatchdog) setBusy(busy bool) {
	if !busy {
		w.stop()
		return
	}
	if w.deadline == nil {
		w.startedAt = time.Now()
		w.progress()
	}
}

func (w *realtimeResponseWatchdog) progress() {
	if w.startedAt.IsZero() {
		w.startedAt = time.Now()
	}
	w.lastProgress = time.Now()
	if w.timer == nil {
		w.timer = time.NewTimer(w.timeout)
	} else {
		w.timer.Reset(w.timeout)
	}
	w.deadline = w.timer.C
}

func (w *realtimeResponseWatchdog) stop() {
	if w.timer != nil {
		w.timer.Stop()
	}
	w.deadline = nil
	w.startedAt = time.Time{}
	w.lastProgress = time.Time{}
}

func (w *realtimeResponseWatchdog) age() time.Duration {
	if w.startedAt.IsZero() {
		return 0
	}
	return time.Since(w.startedAt)
}

func realtimeChatRequestID(command *realtimeChatCommand) string {
	if command == nil {
		return ""
	}
	return command.request.RequestID
}
