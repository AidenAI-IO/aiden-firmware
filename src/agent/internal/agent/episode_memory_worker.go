package agent

const defaultEpisodeMemoryIdleDelay = defaultMemoryWorkerIdleDelay

var (
	errEpisodeMemoryWorkerBusy    = errMemoryWorkerBusy
	errEpisodeMemoryWorkerStopped = errMemoryWorkerStopped
)

type episodeMemoryWorker struct {
	*MemoryWorker
}
type episodeMemoryBatchProcessor = MemoryProcessor
type episodeMemoryBatchResult = MemoryBatchResult

func newEpisodeMemoryWorker(processor episodeMemoryBatchProcessor) *episodeMemoryWorker {
	return &episodeMemoryWorker{
		MemoryWorker: newMemoryWorker(processor, episodeMemoryBatchLimit, defaultEpisodeMemoryIdleDelay),
	}
}

func (w *episodeMemoryWorker) NotifyEpisode() {
	if w != nil {
		w.Notify()
	}
}
