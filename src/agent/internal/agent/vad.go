package agent

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

const (
	sileroVADSampleRate       = 16000
	sileroVADFrameSamples     = 512
	defaultVADSpeechThreshold = 0.5
	defaultVADModelPath       = "/userdata/agent/silero_vad_6_2_encoder_rv1106_w8a8_v1.rknn"
	defaultVADWeightsPath     = "/userdata/agent/silero_vad_6_2_lstm_decoder_weights.bin"
	defaultVADHelperPath      = "/oem/usr/bin/rknn_vad"
)

type AudioVADConfig struct {
	SampleRate      int
	SilenceMs       int
	MinSpeechMs     int
	AlwaysBuffer    bool
	ModelPath       string
	HelperPath      string
	SpeechThreshold float64
}

type VADScorer interface {
	Score(samples []int16) (float64, error)
	Reset() error
	Close() error
}

// AudioVAD segments utterances using Silero VAD probabilities produced by the RKNN helper.
type AudioVAD struct {
	scorer          VADScorer
	alwaysBuffer    bool
	silenceCount    int
	speechFrames    int
	speaking        bool
	frameSamples    int
	silenceLimit    int
	minSpeechFrames int
	speechBuf       []int16
	utterance       []int16
	threshold       float64
	lastProbability float64
	lastErr         error
}

type VADDebugState struct {
	Probability   float64
	Threshold     float64
	Speaking      bool
	SpeechFrames  int
	SilenceFrames int
	LastError     string
}

func DefaultVADModelPath() string {
	return defaultVADModelPath
}

func DefaultVADHelperPath() string {
	return defaultVADHelperPath
}

func DefaultVADSpeechThreshold() float64 {
	return defaultVADSpeechThreshold
}

func NewAudioVAD(cfg AudioVADConfig) (*AudioVAD, error) {
	modelPath := strings.TrimSpace(cfg.ModelPath)
	if modelPath == "" {
		modelPath = defaultVADModelPath
	}
	helperPath := strings.TrimSpace(cfg.HelperPath)
	if helperPath == "" {
		helperPath = defaultVADHelperPath
	}
	return newAudioVAD(cfg, newRKNNVADScorer(modelPath, helperPath))
}

func NewAudioVADWithScorer(cfg AudioVADConfig, scorer VADScorer) (*AudioVAD, error) {
	if scorer == nil {
		return nil, errors.New("nil VAD scorer")
	}
	return newAudioVAD(cfg, scorer)
}

func newAudioVAD(cfg AudioVADConfig, scorer VADScorer) (*AudioVAD, error) {
	sampleRate := cfg.SampleRate
	if sampleRate == 0 {
		sampleRate = sileroVADSampleRate
	}
	if sampleRate != sileroVADSampleRate {
		return nil, fmt.Errorf("silero RKNN VAD requires %d Hz audio, got %d Hz", sileroVADSampleRate, sampleRate)
	}

	threshold := cfg.SpeechThreshold
	if threshold <= 0 {
		threshold = defaultVADSpeechThreshold
	}
	if threshold >= 1 {
		return nil, fmt.Errorf("vad_speech_threshold must be between 0 and 1, got %.3f", threshold)
	}

	silenceMs := cfg.SilenceMs
	if silenceMs <= 0 {
		silenceMs = 650
	}
	minSpeechMs := cfg.MinSpeechMs
	if minSpeechMs <= 0 {
		minSpeechMs = 300
	}

	frameMs := sileroVADFrameSamples * 1000 / sileroVADSampleRate
	silenceLimit := framesForDuration(silenceMs, frameMs)
	minSpeechFrames := framesForDuration(minSpeechMs, frameMs)

	return &AudioVAD{
		scorer:          scorer,
		alwaysBuffer:    cfg.AlwaysBuffer,
		frameSamples:    sileroVADFrameSamples,
		silenceLimit:    silenceLimit,
		minSpeechFrames: minSpeechFrames,
		speechBuf:       make([]int16, 0, sileroVADFrameSamples*100),
		utterance:       make([]int16, 0, sileroVADFrameSamples*100),
		threshold:       threshold,
	}, nil
}

func framesForDuration(durationMs, frameMs int) int {
	frames := durationMs / frameMs
	if durationMs%frameMs != 0 {
		frames++
	}
	if frames < 1 {
		return 1
	}
	return frames
}

// FrameSamples returns the number of samples expected in each Silero VAD frame.
func (v *AudioVAD) FrameSamples() int {
	return v.frameSamples
}

func (v *AudioVAD) DebugState() VADDebugState {
	state := VADDebugState{
		Probability:   v.lastProbability,
		Threshold:     v.threshold,
		Speaking:      v.speaking,
		SpeechFrames:  v.speechFrames,
		SilenceFrames: v.silenceCount,
	}
	if v.lastErr != nil {
		state.LastError = v.lastErr.Error()
	}
	return state
}

// Process feeds one 512-sample 16 kHz frame to the RKNN VAD and returns an utterance when speech ends.
func (v *AudioVAD) Process(samples []int16) ([]int16, error) {
	if len(samples) != v.frameSamples {
		return nil, fmt.Errorf("invalid Silero VAD frame: got %d samples, want %d", len(samples), v.frameSamples)
	}

	probability, err := v.scorer.Score(samples)
	if err != nil {
		v.lastErr = err
		return nil, err
	}
	v.lastErr = nil
	v.lastProbability = probability
	isSpeech := probability > v.threshold

	if v.alwaysBuffer {
		v.speechBuf = append(v.speechBuf, samples...)
		if isSpeech {
			v.speechFrames++
			v.speaking = true
			v.silenceCount = 0
		} else if v.speaking {
			v.silenceCount++
			if v.silenceCount >= v.silenceLimit {
				if v.speechFrames >= v.minSpeechFrames {
					return v.finishUtterance(), nil
				}
				v.resetBuffers()
			}
		}
		return nil, nil
	}

	if isSpeech {
		v.speechBuf = append(v.speechBuf, samples...)
		v.silenceCount = 0
		v.speechFrames++
		v.speaking = true
	} else if v.speaking {
		v.speechBuf = append(v.speechBuf, samples...)
		v.silenceCount++
		if v.silenceCount >= v.silenceLimit {
			if v.speechFrames >= v.minSpeechFrames {
				return v.finishUtterance(), nil
			}
			v.resetBuffers()
		}
	}

	return nil, nil
}

func (v *AudioVAD) finishUtterance() []int16 {
	v.utterance = make([]int16, len(v.speechBuf))
	copy(v.utterance, v.speechBuf)
	v.resetBuffers()
	return v.utterance
}

// Flush returns any buffered audio as an utterance.
func (v *AudioVAD) Flush() []int16 {
	if len(v.speechBuf) == 0 {
		_ = v.Reset()
		return nil
	}

	v.utterance = make([]int16, len(v.speechBuf))
	copy(v.utterance, v.speechBuf)
	_ = v.Reset()
	return v.utterance
}

// Reset clears the VAD segmentation buffers and RKNN recurrent state.
func (v *AudioVAD) Reset() error {
	v.resetBuffers()
	v.lastProbability = 0
	v.lastErr = nil
	if err := v.scorer.Reset(); err != nil {
		v.lastErr = err
		return err
	}
	return nil
}

func (v *AudioVAD) Close() error {
	return v.scorer.Close()
}

func (v *AudioVAD) resetBuffers() {
	v.speechBuf = v.speechBuf[:0]
	v.silenceCount = 0
	v.speechFrames = 0
	v.speaking = false
}

type rknnVADScorer struct {
	modelPath   string
	weightsPath string
	helperPath  string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	waitErr chan error
}

func newRKNNVADScorer(modelPath, helperPath string) *rknnVADScorer {
	return &rknnVADScorer{
		modelPath:   modelPath,
		weightsPath: defaultVADWeightsPath,
		helperPath:  helperPath,
	}
}

func (s *rknnVADScorer) Score(samples []int16) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureStarted(); err != nil {
		return 0, err
	}

	frame := make([]byte, 1+len(samples)*2)
	frame[0] = 'F'
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(frame[1+i*2:], uint16(sample))
	}
	if _, err := s.stdin.Write(frame); err != nil {
		s.stopAfterProtocolError()
		return 0, fmt.Errorf("write RKNN VAD frame: %w", err)
	}

	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.stopAfterProtocolError()
		return 0, fmt.Errorf("read RKNN VAD score: %w", err)
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "ERR ") {
		return 0, errors.New(strings.TrimPrefix(line, "ERR "))
	}
	if !strings.HasPrefix(line, "P ") {
		return 0, fmt.Errorf("unexpected RKNN VAD response %q", line)
	}
	probability, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, "P ")), 64)
	if err != nil {
		return 0, fmt.Errorf("parse RKNN VAD probability %q: %w", line, err)
	}
	return probability, nil
}

func (s *rknnVADScorer) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cmd == nil {
		return nil
	}
	if _, err := s.stdin.Write([]byte{'R'}); err != nil {
		s.stopAfterProtocolError()
		return fmt.Errorf("write RKNN VAD reset: %w", err)
	}
	line, err := s.stdout.ReadString('\n')
	if err != nil {
		s.stopAfterProtocolError()
		return fmt.Errorf("read RKNN VAD reset response: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "OK" {
		return nil
	}
	if strings.HasPrefix(line, "ERR ") {
		return errors.New(strings.TrimPrefix(line, "ERR "))
	}
	return fmt.Errorf("unexpected RKNN VAD reset response %q", line)
}

func (s *rknnVADScorer) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *rknnVADScorer) ensureStarted() error {
	if s.cmd != nil {
		return nil
	}

	args := []string{"--model", s.modelPath}
	if s.weightsPath != "" {
		args = append(args, "--weights", s.weightsPath)
	}
	cmd := exec.Command(s.helperPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open RKNN VAD stdin: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open RKNN VAD stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("open RKNN VAD stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start RKNN VAD helper %q: %w", s.helperPath, err)
	}

	go logRKNNVADStderr(stderrPipe)

	reader := bufio.NewReader(stdoutPipe)
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("wait for RKNN VAD readiness: %w", err)
	}
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "ERR ") {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New(strings.TrimPrefix(line, "ERR "))
	}
	if line != "READY" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("unexpected RKNN VAD readiness response %q", line)
	}

	s.cmd = cmd
	s.stdin = stdin
	s.stdout = reader
	s.waitErr = make(chan error, 1)
	go func() {
		s.waitErr <- cmd.Wait()
	}()
	return nil
}

func logRKNNVADStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		log.Printf("[rknn_vad] %s\n", scanner.Text())
	}
}

func (s *rknnVADScorer) stopAfterProtocolError() {
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.closeLocked()
}

func (s *rknnVADScorer) closeLocked() error {
	if s.cmd == nil {
		return nil
	}
	_, _ = s.stdin.Write([]byte{'Q'})
	_ = s.stdin.Close()
	err := <-s.waitErr
	s.cmd = nil
	s.stdin = nil
	s.stdout = nil
	s.waitErr = nil
	if err != nil && !strings.Contains(err.Error(), "signal: killed") {
		return err
	}
	return nil
}
