package main

import (
	"context"
	"log"
	"os"

	"aiden-agent/internal/agent"
	"aiden-agent/internal/agent/rtclient"
)

const realtimeAudioReadTimeoutMs = 200

// runRealtimeWakeupMode owns the realtime voice path. The legacy wakeup
// runners remain separate so they can be restored without changing this path.
func runRealtimeWakeupMode(cfg agent.Config, sigChan chan os.Signal, newWatcher wakeupWatcherFactory) {
	log.Printf("\n[ready] Starting realtime GPIO wakeup listeners on %s...", wakeupGPIOPinsLabel())
	events := make(chan struct{}, 1)
	watchers, err := startWakeupWatchers(newWatcher, func() {
		signalWakeupEvent(events)
	})
	if err != nil {
		log.Printf("[error] Failed to start GPIO wakeup listeners: %v\n", err)
		return
	}
	defer stopWakeupWatchers(watchers)

	log.Printf("[ready] Waiting for realtime wakeup event (%s)... Ctrl+C to quit", wakeupGPIOPinsLabel())
	for {
		select {
		case <-sigChan:
			log.Println("\n[exit] Stopped.")
			return
		case <-events:
			log.Println("\n[wakeup] GPIO wakeup triggered, connecting realtime voice model...")
			if err := runRealtimeSession(cfg, sigChan); err != nil {
				log.Printf("[realtime] session ended: %v", err)
			}
			drainRealtimeWakeups(events)
			log.Println("[ready] Waiting for realtime wakeup event...")
		}
	}
}

func drainRealtimeWakeups(events <-chan struct{}) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func realtimeSessionConfig(cfg agent.Config) rtclient.SessionConfig {
	voice := cfg.VoiceModel.Voice
	if voice == "" {
		voice = rtclient.DefaultVoice
	}
	inputFormat := cfg.VoiceModel.InputAudioFormat
	if inputFormat == "" {
		inputFormat = "pcm"
	}
	outputFormat := cfg.VoiceModel.OutputAudioFormat
	if outputFormat == "" {
		outputFormat = "pcm"
	}
	turnType := cfg.VoiceModel.TurnDetection
	if turnType == "" {
		turnType = "server_vad"
	}
	turn := &rtclient.TurnDetection{Type: turnType, Threshold: cfg.VoiceModel.TurnDetectionThreshold}
	if cfg.VoiceModel.TurnDetectionSilenceMs > 0 {
		turn.SilenceDurationMS = cfg.VoiceModel.TurnDetectionSilenceMs
	}
	instructions := cfg.VoiceModel.Instructions
	if instructions == "" {
		instructions = cfg.Instruction
	}
	enableEmotion := cfg.VoiceModel.EnableSpeechEmotion
	if enableEmotion == nil {
		v := true
		enableEmotion = &v
	}
	return rtclient.SessionConfig{
		Modalities:          []string{"audio", "text"},
		Voice:               voice,
		EnableSpeechEmotion: enableEmotion,
		Instructions:        instructions,
		InputAudioFormat:    inputFormat,
		OutputAudioFormat:   outputFormat,
		TurnDetection:       turn,
	}
}

func runRealtimeSession(cfg agent.Config, sigChan chan os.Signal) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	client, err := rtclient.New(rtclient.Config{
		APIKey:      cfg.VoiceModel.APIKey,
		Model:       cfg.VoiceModel.Model,
		WorkspaceID: cfg.VoiceModel.WorkspaceID,
		Region:      cfg.VoiceModel.Region,
		Endpoint:    cfg.VoiceModel.Endpoint,
	})
	if err != nil {
		return err
	}
	session, err := client.Connect(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	if err := session.Update(ctx, realtimeSessionConfig(cfg)); err != nil {
		return err
	}

	audio := agent.NewAudioServiceClient(cfg.Audio.SocketOrDefault())
	inputFormat := agent.AudioFormat{
		// Qwen realtime currently accepts only 16 kHz, 16-bit mono PCM.
		SampleRate: 16000,
		Channels:   1,
		BitWidth:   16,
	}
	recording, err := audio.StartRecording(inputFormat)
	if err != nil {
		return err
	}
	defer func() { _ = audio.StopRecording(recording.SessionID) }()

	chunks := make(chan []byte, 8)
	readErrs := make(chan error, 1)
	go streamRealtimeAudio(ctx, audio, recording.SessionID, chunks, readErrs)

	outputFormat := agent.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}
	playback := realtimePlaybackState{}
	defer func() { _ = playback.stop(audio) }()

	for {
		select {
		case <-sigChan:
			return nil
		case <-session.Done():
			return nil
		case err := <-session.Errors():
			if err != nil {
				return err
			}
		case err := <-readErrs:
			if err != nil {
				return err
			}
		case pcm, ok := <-chunks:
			if !ok {
				return nil
			}
			if err := session.AppendAudio(ctx, pcm); err != nil {
				return err
			}
		case event, ok := <-session.Events():
			if !ok {
				return nil
			}
			switch event.Type {
			case "input_audio_buffer.speech_started":
				// Stop the hardware session rather than finalizing it: finalizing
				// drains queued PCM and makes barge-in feel delayed.
				_ = playback.interrupt(audio)
			case "response.created":
				// Ignore any late deltas from the interrupted response until the
				// server explicitly starts the next one.
				_ = playback.beginResponse(audio)
			case "response.audio.delta":
				pcm, err := event.AudioDelta()
				if err != nil {
					return err
				}
				if err := playback.append(audio, outputFormat, pcm); err != nil {
					return err
				}
			case "response.audio.done", "response.done":
				if err := playback.finalize(audio); err != nil {
					log.Printf("[realtime] finalize playback: %v", err)
				}
			case "error":
				var serverErr rtclient.ErrorEvent
				if err := event.Decode(&serverErr); err != nil {
					return err
				}
				return &realtimeServerError{message: serverErr.Error.Message}
			}
		}
	}
}

type realtimePlaybackAudio interface {
	StartPlayback(agent.AudioFormat) (*agent.PlaybackStartResult, error)
	WritePlayChunk(uint64, []byte, bool) error
	StopPlayback(uint64) error
}

type realtimePlaybackState struct {
	session        *agent.PlaybackStartResult
	finalized      bool
	suppressDeltas bool
}

func (p *realtimePlaybackState) append(audio realtimePlaybackAudio, format agent.AudioFormat, pcm []byte) error {
	if p.suppressDeltas || p.finalized || len(pcm) == 0 {
		return nil
	}
	if p.session == nil {
		session, err := audio.StartPlayback(format)
		if err != nil {
			return err
		}
		p.session = session
		p.finalized = false
	}
	return audio.WritePlayChunk(p.session.SessionID, pcm, false)
}

func (p *realtimePlaybackState) finalize(audio realtimePlaybackAudio) error {
	if p.session == nil || p.finalized {
		return nil
	}
	p.finalized = true
	return audio.WritePlayChunk(p.session.SessionID, nil, true)
}

func (p *realtimePlaybackState) interrupt(audio realtimePlaybackAudio) error {
	p.suppressDeltas = true
	return p.stop(audio)
}

func (p *realtimePlaybackState) beginResponse(audio realtimePlaybackAudio) error {
	err := p.stop(audio)
	p.suppressDeltas = false
	return err
}

func (p *realtimePlaybackState) stop(audio realtimePlaybackAudio) error {
	if p.session == nil {
		p.finalized = false
		return nil
	}
	sessionID := p.session.SessionID
	p.session = nil
	p.finalized = false
	return audio.StopPlayback(sessionID)
}

type realtimeServerError struct{ message string }

func (e *realtimeServerError) Error() string { return "rtclient: " + e.message }

func streamRealtimeAudio(ctx context.Context, audio *agent.AudioServiceClient, sessionID uint64, chunks chan<- []byte, errs chan<- error) {
	defer close(chunks)
	reader, err := audio.OpenRecordChunkReader(sessionID)
	if err != nil {
		select {
		case errs <- err:
		case <-ctx.Done():
		}
		return
	}
	defer reader.Close()
	for {
		chunk, err := reader.Read(realtimeAudioReadTimeoutMs)
		if err != nil {
			select {
			case errs <- err:
			case <-ctx.Done():
			}
			return
		}
		if chunk.EndOfStream {
			return
		}
		if len(chunk.PCM) == 0 {
			continue
		}
		select {
		case chunks <- chunk.PCM:
		case <-ctx.Done():
			return
		}
	}
}
