package agent

import (
	"fmt"
	"log"
	"time"
)

type audioRecordingBackend interface {
	StartRecording(format AudioFormat) (*RecordStartResult, error)
	ReadRecordChunk(sessionID uint64, timeoutMs uint32) (*AudioChunkResult, error)
	StopRecording(sessionID uint64) error
}

func recordingBackendOrService(backend audioRecordingBackend, service *AudioServiceClient) audioRecordingBackend {
	if backend != nil {
		return backend
	}
	return service
}

func newAudioRecordingBackendFromConfig(cfg Config, service *AudioServiceClient, logger *Logger) audioRecordingBackend {
	switch cfg.AudioBackendOrDefault() {
	case AudioBackendLocal:
		return newLocalAudioRecordingBackend(logger)
	default:
		return service
	}
}

func startRecordingWithRetry(audio audioRecordingBackend, format AudioFormat, retryTimeout, retryInterval time.Duration) (*RecordStartResult, error) {
	if retryTimeout <= 0 {
		return audio.StartRecording(format)
	}
	if retryInterval <= 0 {
		retryInterval = 100 * time.Millisecond
	}

	deadline := time.Now().Add(retryTimeout)
	attempts := 0
	var lastErr error
	for {
		result, err := audio.StartRecording(format)
		if err == nil {
			if attempts > 0 {
				log.Printf("[audio] Record session opened after %d retries\n", attempts)
			}
			return result, nil
		}
		lastErr = err
		attempts++
		if attempts == 1 {
			log.Printf("[audio] Record session unavailable, retrying for up to %s: %v\n", retryTimeout, err)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		sleep := retryInterval
		if sleep > remaining {
			sleep = remaining
		}
		time.Sleep(sleep)
	}
	return nil, fmt.Errorf("after %s: %w", retryTimeout, lastErr)
}
