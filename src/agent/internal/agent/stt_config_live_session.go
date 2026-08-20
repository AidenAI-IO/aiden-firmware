package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

var newSTTClientFromConfigForLiveTest = NewSTTClientFromConfig

const (
	sttConfigTestLiveDrainTimeout        = 7 * time.Second
	sttConfigTestLiveStaleCleanupTimeout = 200 * time.Millisecond
)

var sttConfigLiveStreamingSTTFinalizeTimeout = 2 * time.Second

type sttConfigTestStatusError struct {
	status int
	err    error
}

func (e *sttConfigTestStatusError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *sttConfigTestStatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func newSTTConfigTestStatusError(status int, err error) error {
	if err == nil {
		return nil
	}
	return &sttConfigTestStatusError{status: status, err: err}
}

func sttConfigTestStatusCode(err error, fallback int) int {
	var statusErr *sttConfigTestStatusError
	if errors.As(err, &statusErr) && statusErr.status > 0 {
		return statusErr.status
	}
	return fallback
}

type sttConfigTestLiveStartRequest struct {
	STTValues   *sttConfigTestSTTValues   `json:"stt_values"`
	AudioValues *sttConfigTestAudioValues `json:"audio_values"`
}

type sttConfigTestSTTValues struct {
	Provider        string `json:"provider"`
	Language        string `json:"language"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	BaseURL         string `json:"base_url"`
	AppID           string `json:"app_id"`
	SecretID        string `json:"secret_id"`
	SecretKey       string `json:"secret_key"`
	Region          string `json:"region"`
	EngineModelType string `json:"engine_model_type"`
}

type sttConfigTestAudioValues struct {
	Backend    string `json:"backend"`
	Socket     string `json:"socket"`
	SampleRate int    `json:"sample_rate"`
	Channels   int    `json:"channels"`
	BitWidth   int    `json:"bit_width"`
}

type sttConfigTestLiveStartResponse struct {
	OK         bool   `json:"ok"`
	Status     string `json:"status"`
	SampleRate int    `json:"sample_rate"`
}

type sttConfigTestCheck struct {
	Check  string `json:"check"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type sttConfigTestLiveStopResponse struct {
	OK         bool                 `json:"ok"`
	Transcript string               `json:"transcript,omitempty"`
	Results    []sttConfigTestCheck `json:"results"`
}

type sttConfigTestLiveSession struct {
	provider      string
	audioClient   *AudioServiceClient
	recordBackend audioRecordingBackend
	sttClient     STTClient
	sessionID     uint64
	sampleRate    int
	done          chan struct{}

	mu         sync.Mutex
	samples    []int16
	hasPending bool
	pending    byte
	stopping   bool
	err        error
	sttSession *streamingSTTSession
	transcript string
}

type sttConfigTestLiveCapture struct {
	provider       string
	sampleRate     int
	sttClient      STTClient
	transcriptHint string
	samples        []int16
}

func (v sttConfigTestSTTValues) transcriptionTestRequest() STTTranscriptionTestRequest {
	return STTTranscriptionTestRequest{
		Provider:        v.Provider,
		Language:        v.Language,
		APIKey:          v.APIKey,
		Model:           v.Model,
		BaseURL:         v.BaseURL,
		AppID:           v.AppID,
		SecretID:        v.SecretID,
		SecretKey:       v.SecretKey,
		Region:          v.Region,
		EngineModelType: v.EngineModelType,
	}
}

func (v sttConfigTestAudioValues) applyTo(cfg *Config) {
	if cfg == nil {
		return
	}
	if strings.TrimSpace(v.Socket) != "" {
		cfg.Audio.Socket = strings.TrimSpace(v.Socket)
	}
	if strings.TrimSpace(v.Backend) != "" {
		cfg.Audio.Backend = strings.TrimSpace(v.Backend)
	}
	if v.SampleRate > 0 {
		cfg.Audio.SampleRate = v.SampleRate
	}
	if v.Channels > 0 {
		cfg.Audio.Channels = v.Channels
	}
	if v.BitWidth > 0 {
		cfg.Audio.BitWidth = v.BitWidth
	}
}

func (r sttConfigTestLiveStartRequest) appliedConfig(base Config) (Config, error) {
	cfg := base
	if r.STTValues == nil {
		return Config{}, fmt.Errorf("missing stt_values object")
	}
	if r.AudioValues == nil {
		return Config{}, fmt.Errorf("missing audio_values object")
	}

	applySTTTranscriptionTestRequest(&cfg, r.STTValues.transcriptionTestRequest())
	r.AudioValues.applyTo(&cfg)

	if strings.TrimSpace(cfg.STT.Provider) == "" {
		return Config{}, fmt.Errorf("stt.provider is required")
	}
	if strings.TrimSpace(cfg.Audio.SocketOrDefault()) == "" {
		return Config{}, fmt.Errorf("audio.socket is required")
	}
	if cfg.Audio.SampleRateOrDefault() <= 0 {
		return Config{}, fmt.Errorf("audio.sample_rate must be > 0")
	}
	if cfg.Audio.ChannelsOrDefault() != 1 {
		return Config{}, fmt.Errorf("audio.channels must be 1 for STT test")
	}
	if cfg.Audio.BitWidthOrDefault() != 16 {
		return Config{}, fmt.Errorf("audio.bit_width must be 16 for STT test")
	}
	return cfg, nil
}

func (s *Server) handleSTTConfigTestStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req sttConfigTestLiveStartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	s.recordMu.Lock()
	if s.webRecording != nil {
		s.recordMu.Unlock()
		http.Error(w, "device audio recording is already active", http.StatusConflict)
		return
	}
	stale := s.sttConfigTestSession
	s.sttConfigTestSession = nil
	s.recordMu.Unlock()

	if stale != nil {
		if s.logger != nil {
			s.logger.Warn("Clearing stale STT config-test recording before start")
		}
		if err := s.endStaleSTTConfigTestLiveSession(stale); err != nil && s.logger != nil {
			s.logger.Warn("Stale STT config-test cleanup failed: %v", err)
		}
	}

	session, err := s.newSTTConfigTestLiveSession(req)
	if err != nil {
		statusCode := sttConfigTestStatusCode(err, http.StatusInternalServerError)
		if s.logger != nil && statusCode >= 500 {
			s.logger.Error("STT config-test start failed: %v", err)
		}
		http.Error(w, err.Error(), statusCode)
		return
	}

	s.recordMu.Lock()
	if s.webRecording != nil || s.sttConfigTestSession != nil {
		s.recordMu.Unlock()
		if err := s.endStaleSTTConfigTestLiveSession(session); err != nil && s.logger != nil {
			s.logger.Warn("Orphaned STT config-test cleanup failed: %v", err)
		}
		http.Error(w, "audio recording is already active", http.StatusConflict)
		return
	}
	s.sttConfigTestSession = session
	s.recordMu.Unlock()

	go s.readSTTConfigTestLiveSession(session)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sttConfigTestLiveStartResponse{
		OK:         true,
		Status:     "recording",
		SampleRate: session.sampleRate,
	})
}

func (s *Server) handleSTTConfigTestStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.recordMu.Lock()
	session := s.sttConfigTestSession
	if session == nil {
		s.recordMu.Unlock()
		http.Error(w, "STT test recording is not active", http.StatusBadRequest)
		return
	}
	s.sttConfigTestSession = nil
	s.recordMu.Unlock()

	capture, err := s.endSTTConfigTestLiveSession(session)
	if err != nil {
		if s.logger != nil {
			s.logger.Error("STT config-test stop failed: %v", err)
		}
		http.Error(w, "stop device audio recording: "+err.Error(), http.StatusServiceUnavailable)
		return
	}

	if len(capture.samples) == 0 {
		s.writeSTTConfigTestLiveResponse(w, false, "", "recording captured no audio data")
		return
	}

	transcriptHint := strings.TrimSpace(capture.transcriptHint)
	mode := "one-shot transcription"
	if transcriptHint != "" {
		mode = "streaming upload"
	}

	audioInput, err := PrepareAudioInput(
		TurnModalitySTT,
		capture.sttClient,
		pcm16MonoToWAV(capture.samples, capture.sampleRate),
		transcriptHint,
		"",
		nil,
	)
	if err != nil {
		s.writeSTTConfigTestLiveResponse(w, false, transcriptHint, err.Error())
		return
	}

	provider := firstNonEmptyString([]string{
		strings.TrimSpace(capture.provider),
		strings.TrimSpace(s.runtime.config.STT.Provider),
		"configured STT provider",
	})
	s.writeSTTConfigTestLiveResponse(
		w,
		true,
		audioInput.Transcript,
		fmt.Sprintf("transcribed live recording with %s via %s", provider, mode),
	)
}

func (s *Server) writeSTTConfigTestLiveResponse(w http.ResponseWriter, ok bool, transcript, detail string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sttConfigTestLiveStopResponse{
		OK:         ok,
		Transcript: strings.TrimSpace(transcript),
		Results: []sttConfigTestCheck{{
			Check:  "stt_transcription",
			Passed: ok,
			Detail: detail,
		}},
	})
}

func (s *Server) newSTTConfigTestLiveSession(req sttConfigTestLiveStartRequest) (*sttConfigTestLiveSession, error) {
	cfg, err := req.appliedConfig(s.runtime.config)
	if err != nil {
		return nil, newSTTConfigTestStatusError(http.StatusBadRequest, err)
	}

	sttClient, err := newSTTClientFromConfigForLiveTest(cfg)
	if err != nil {
		return nil, newSTTConfigTestStatusError(http.StatusBadRequest, err)
	}
	if sttClient == nil {
		return nil, newSTTConfigTestStatusError(http.StatusBadRequest, fmt.Errorf("STT client is unavailable"))
	}

	audioClient := NewAudioServiceClient(cfg.Audio.SocketOrDefault())
	recordBackend := newAudioRecordingBackendFromConfig(cfg, audioClient, s.logger)
	sampleRate := cfg.Audio.SampleRateOrDefault()
	channels := cfg.Audio.ChannelsOrDefault()
	bitWidth := cfg.Audio.BitWidthOrDefault()
	result, err := startRecordingWithRetry(recordBackend, AudioFormat{
		SampleRate: uint32(sampleRate),
		Channels:   uint32(channels),
		BitWidth:   uint32(bitWidth),
	}, recordingStartRetryTimeout, recordingStartRetryInterval)
	if err != nil {
		return nil, newSTTConfigTestStatusError(http.StatusServiceUnavailable, err)
	}

	session := &sttConfigTestLiveSession{
		provider:      strings.TrimSpace(cfg.STT.Provider),
		audioClient:   audioClient,
		recordBackend: recordBackend,
		sttClient:     sttClient,
		sessionID:     result.SessionID,
		sampleRate:    sampleRate,
		done:          make(chan struct{}),
		samples:       make([]int16, 0, sampleRate),
	}

	streamingSTT, err := beginStreamingSTTSession(context.Background(), sttClient, STTStreamConfig{
		SampleRate: sampleRate,
		Channels:   channels,
		BitWidth:   bitWidth,
	})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("Live STT config-test streaming unavailable, falling back to one-shot STT: %v", err)
		}
	} else if streamingSTT != nil {
		if s.logger != nil {
			s.logger.Info("Live STT config-test streaming upload enabled for realtime transcription")
		}
		session.sttSession = streamingSTT
	}

	return session, nil
}

func (s *Server) endSTTConfigTestLiveSession(session *sttConfigTestLiveSession) (sttConfigTestLiveCapture, error) {
	return s.endSTTConfigTestLiveSessionWithTimeout(session, sttConfigTestLiveDrainTimeout)
}

func (s *Server) endStaleSTTConfigTestLiveSession(session *sttConfigTestLiveSession) error {
	_, err := s.endSTTConfigTestLiveSessionWithTimeout(session, sttConfigTestLiveStaleCleanupTimeout)
	return err
}

func (s *Server) endSTTConfigTestLiveSessionWithTimeout(session *sttConfigTestLiveSession, timeout time.Duration) (sttConfigTestLiveCapture, error) {
	if session == nil {
		return sttConfigTestLiveCapture{}, nil
	}

	session.setStopping()
	stopErr := recordingBackendOrService(session.recordBackend, session.audioClient).StopRecording(session.sessionID)
	drained := false
	select {
	case <-session.done:
		drained = true
	case <-time.After(timeout):
		if stopErr == nil {
			stopErr = fmt.Errorf("timed out waiting for STT test recording to drain")
		}
	}
	if err := session.readError(); err != nil {
		session.clearStreamingSession()
		return sttConfigTestLiveCapture{}, err
	}
	if !drained {
		session.clearStreamingSession()
		return sttConfigTestLiveCapture{}, stopErr
	}
	if err := s.finalizeSTTConfigTestLiveTranscript(session); err != nil && s.logger != nil {
		s.logger.Warn("Finalize STT config-test streaming transcript failed: %v", err)
	}

	capture := sttConfigTestLiveCapture{
		provider:       session.provider,
		sampleRate:     session.sampleRate,
		sttClient:      session.sttClient,
		transcriptHint: session.readTranscript(),
		samples:        session.snapshotSamples(),
	}
	return capture, stopErr
}

func (s *Server) finalizeSTTConfigTestLiveTranscript(session *sttConfigTestLiveSession) error {
	if session == nil {
		return nil
	}

	session.mu.Lock()
	stream := session.sttSession
	session.sttSession = nil
	session.mu.Unlock()
	if stream == nil {
		return nil
	}

	transcript, err := stream.FinalizeWithTimeout(sttConfigLiveStreamingSTTFinalizeTimeout)
	if err != nil {
		_ = stream.Close()
		return err
	}
	session.storeTranscript(transcript)
	return stream.Close()
}

func (s *Server) readSTTConfigTestLiveSession(session *sttConfigTestLiveSession) {
	defer close(session.done)

	for {
		chunk, err := recordingBackendOrService(session.recordBackend, session.audioClient).ReadRecordChunk(session.sessionID, 1000)
		if err != nil {
			if !session.isStopping() {
				session.setError(err)
			}
			return
		}
		if len(chunk.PCM) > 0 {
			session.appendPCM(chunk.PCM)
			if err := session.uploadPCM(chunk.PCM); err != nil {
				session.clearStreamingSession()
				if s.logger != nil {
					s.logger.Warn("STT config-test streaming upload failed, falling back to one-shot STT: %v", err)
				}
			}
		}
		if chunk.EndOfStream {
			return
		}
	}
}

func (s *sttConfigTestLiveSession) appendPCM(pcm []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	AppendPCM16Samples(pcm, &s.samples, &s.hasPending, &s.pending)
}

func (s *sttConfigTestLiveSession) uploadPCM(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}

	s.mu.Lock()
	stream := s.sttSession
	s.mu.Unlock()
	if stream == nil {
		return nil
	}
	return stream.UploadPCM(pcm)
}

func (s *sttConfigTestLiveSession) clearStreamingSession() {
	s.mu.Lock()
	stream := s.sttSession
	s.sttSession = nil
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

func (s *sttConfigTestLiveSession) snapshotSamples() []int16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.samples) == 0 {
		return nil
	}
	out := make([]int16, len(s.samples))
	copy(out, s.samples)
	return out
}

func (s *sttConfigTestLiveSession) storeTranscript(transcript string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.transcript = strings.TrimSpace(transcript)
}

func (s *sttConfigTestLiveSession) readTranscript() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.transcript
}

func (s *sttConfigTestLiveSession) setStopping() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopping = true
}

func (s *sttConfigTestLiveSession) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *sttConfigTestLiveSession) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *sttConfigTestLiveSession) readError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
