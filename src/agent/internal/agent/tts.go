package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	minimaxTTSModel      = "speech-2.8-hd"
	minimaxTTSSampleRate = 32000
	minimaxTTSChannels   = 1
	minimaxTTSBitWidth   = 16

	playbackDrainPollInterval = 50 * time.Millisecond
	playbackDrainTimeout      = 30 * time.Second
)

// TTSClient is the interface for text-to-speech providers
type TTSClient interface {
	TextToSpeechStream(ctx context.Context, text string, audio *AudioServiceClient) error
}

// MinimaxTTS implements TTS using Minimax API
type MinimaxTTS struct {
	apiKey     string
	voiceID    string
	emotion    string
	speed      float64
	httpClient *http.Client
}

// NewMinimaxTTS creates a new Minimax TTS client
func NewMinimaxTTS(apiKey, voiceID, emotion string, speed float64, httpClients ...*http.Client) *MinimaxTTS {
	if voiceID == "" {
		voiceID = "male-qn-qingse"
	}
	if emotion == "" {
		emotion = "happy"
	}
	if speed == 0 {
		speed = 1.0
	}
	httpClient := http.DefaultClient
	if len(httpClients) > 0 && httpClients[0] != nil {
		httpClient = httpClients[0]
	}
	return &MinimaxTTS{
		apiKey:     apiKey,
		voiceID:    voiceID,
		emotion:    emotion,
		speed:      speed,
		httpClient: httpClient,
	}
}

// TextToSpeechStream streams TTS audio to audio_service.
// Requests PCM format directly from Minimax to avoid MP3 decoding.
func (t *MinimaxTTS) TextToSpeechStream(ctx context.Context, text string, audio *AudioServiceClient) error {
	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Request PCM at 32kHz/16-bit/mono and open audio_service playback with
	// the same format so we can stream directly without any decoding or
	// resampling.
	reqBody := map[string]interface{}{
		"model":  minimaxTTSModel,
		"text":   text,
		"stream": true,
		"voice_setting": map[string]interface{}{
			"voice_id": t.voiceID,
			"speed":    t.speed,
			"vol":      1.0,
			"pitch":    0,
			"emotion":  t.emotion,
		},
		"audio_setting": map[string]interface{}{
			"sample_rate": minimaxTTSSampleRate,
			"format":      "pcm",
			"channel":     minimaxTTSChannels,
		},
		"stream_options": map[string]interface{}{
			"exclude_aggregated_audio": true,
		},
		"subtitle_enable": false,
	}

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// Open playback session matching the format we request from Minimax
	playbackFmt := AudioFormat{
		SampleRate: minimaxTTSSampleRate,
		Channels:   minimaxTTSChannels,
		BitWidth:   minimaxTTSBitWidth,
	}

	playback, err := audio.StartPlayback(playbackFmt)
	if err != nil {
		return fmt.Errorf("start playback: %w", err)
	}
	playbackStopped := false
	stopPlayback := func() {
		if playbackStopped {
			return
		}
		playbackStopped = true
		_ = stopPlaybackIgnoringEnded(audio, playback.SessionID)
	}

	// Make HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.minimaxi.com/v1/t2a_v2", bytes.NewReader(reqData))
	if err != nil {
		stopPlayback()
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		stopPlayback()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		stopPlayback()
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Stream response → parse JSON objects → hex-decode PCM → write to audio_service
	parser := newMinimaxStreamParser()
	readBuf := make([]byte, 8192)
	for {
		select {
		case <-ctx.Done():
			stopPlayback()
			return ctx.Err()
		default:
		}

		n, rerr := resp.Body.Read(readBuf)
		if n > 0 {
			chunks, _ := parser.feed(readBuf[:n])
			for _, pcm := range chunks {
				select {
				case <-ctx.Done():
					stopPlayback()
					return ctx.Err()
				default:
				}
				// Split large PCM chunks into smaller pieces so they fit
				// the AO driver's internal frame buffer (configured for
				// 1024 samples × 4 frames). Sending oversized buffers in
				// one call causes the driver to drop the tail.
				if err := writePlaybackPCMContext(ctx, audio, playback.SessionID, pcm); err != nil {
					stopPlayback()
					return err
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				stopPlayback()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return fmt.Errorf("read response: %w", rerr)
			}
			break
		}
	}

	// Pad a short silence tail to avoid clipping the last phonemes on some
	// AO drivers that stop slightly early.
	const tailMs = 200
	const bytesPerSample = minimaxTTSBitWidth / 8 // s16le mono
	tailBytes := (minimaxTTSSampleRate * tailMs / 1000) * bytesPerSample
	silenceTail := make([]byte, tailBytes)
	select {
	case <-ctx.Done():
		stopPlayback()
		return ctx.Err()
	default:
	}
	if err := writePlaybackPCMContext(ctx, audio, playback.SessionID, silenceTail); err != nil {
		if ctx.Err() != nil {
			stopPlayback()
			return ctx.Err()
		}
		return fmt.Errorf("write silence tail: %w", err)
	}

	// Final chunk drains the playback session
	select {
	case <-ctx.Done():
		stopPlayback()
		return ctx.Err()
	default:
	}
	if err := audio.WritePlayChunk(playback.SessionID, nil, true); err != nil {
		return fmt.Errorf("send final chunk: %w", err)
	}
	if err := waitForPlaybackDrain(ctx, audio, playbackDrainTimeout); err != nil {
		if ctx.Err() != nil {
			stopPlayback()
			return ctx.Err()
		}
		return err
	}

	return nil
}

func waitForPlaybackDrain(ctx context.Context, audio *AudioServiceClient, timeout time.Duration) error {
	if timeout <= 0 {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(playbackDrainPollInterval)
	defer ticker.Stop()

	for {
		health, err := audio.Health()
		if err != nil {
			return fmt.Errorf("wait playback drain: %w", err)
		}
		if health.PlaybackSessions == 0 {
			return nil
		}

		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func stopPlaybackIgnoringEnded(audio *AudioServiceClient, sessionID uint64) error {
	err := audio.StopPlayback(sessionID)
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "session_not_found") ||
		strings.Contains(msg, "not_found") ||
		strings.Contains(msg, "not found") {
		return nil
	}
	return err
}

// writePlaybackPCM splits a PCM buffer into smaller pieces sized for the AO
// driver's internal frame buffer. Sending larger buffers in one call causes
// the driver to play only a portion before dropping the tail.
const playbackChunkBytes = 4096 // 2048 samples = 64ms @32kHz s16le mono

func writePlaybackPCM(audio *AudioServiceClient, sessionID uint64, pcm []byte) error {
	return writePlaybackPCMContext(context.Background(), audio, sessionID, pcm)
}

func writePlaybackPCMContext(ctx context.Context, audio *AudioServiceClient, sessionID uint64, pcm []byte) error {
	for off := 0; off < len(pcm); off += playbackChunkBytes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		end := off + playbackChunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := audio.WritePlayChunk(sessionID, pcm[off:end], false); err != nil {
			return fmt.Errorf("write play chunk: %w", err)
		}
	}
	return nil
}

// minimaxStreamParser parses the streaming response from Minimax.
// The response is a stream of concatenated JSON objects (no SSE framing).
// Each object may contain data.audio as a hex-encoded chunk.
type minimaxStreamParser struct {
	buffer []byte
}

func newMinimaxStreamParser() *minimaxStreamParser {
	return &minimaxStreamParser{}
}

// feed appends data to the buffer and extracts any complete JSON objects.
// Returns hex-decoded audio chunks. The events return value is reserved for
// optional debug logging.
func (p *minimaxStreamParser) feed(data []byte) ([][]byte, []string) {
	p.buffer = append(p.buffer, data...)

	var out [][]byte
	for {
		start := bytes.IndexByte(p.buffer, '{')
		if start < 0 {
			p.buffer = p.buffer[:0]
			break
		}
		if start > 0 {
			p.buffer = p.buffer[start:]
		}

		end, complete := findJSONObjectEnd(p.buffer)
		if !complete {
			break
		}

		var obj struct {
			Data struct {
				Audio string `json:"audio"`
			} `json:"data"`
		}
		if err := json.Unmarshal(p.buffer[:end], &obj); err == nil {
			if obj.Data.Audio != "" {
				if pcm := hexDecode(obj.Data.Audio); len(pcm) > 0 {
					out = append(out, pcm)
				}
			}
		}

		p.buffer = p.buffer[end:]
	}

	return out, nil
}

// findJSONObjectEnd locates the end (exclusive) of the JSON object starting at buffer[0].
// Returns (endIndex, true) if a complete object is found, (0, false) otherwise.
func findJSONObjectEnd(buffer []byte) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i, c := range buffer {
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
	}
	return 0, false
}

// hexDecode decodes a hex string into bytes. Invalid characters yield 0.
func hexDecode(hex string) []byte {
	out := make([]byte, 0, len(hex)/2)
	for i := 0; i+1 < len(hex); i += 2 {
		b := (hexNibble(hex[i]) << 4) | hexNibble(hex[i+1])
		out = append(out, b)
	}
	return out
}

func hexNibble(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
