package agent

import "time"

const defaultNotificationMemoryIdleDelay = 30 * time.Second

type notificationMemoryWorker struct {
	*MemoryWorker
}

func newNotificationMemoryWorker(processor MemoryProcessor) *notificationMemoryWorker {
	return &notificationMemoryWorker{MemoryWorker: newMemoryWorker(processor, notificationMemoryBatchLimit, defaultNotificationMemoryIdleDelay)}
}

func (w *notificationMemoryWorker) NotifyNotification() {
	if w != nil {
		w.Notify()
	}
}
