package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// TTSClient is the interface for text-to-speech providers
type TTSClient interface {
	TextToSpeechStream(text string, audio *AudioServiceClient) error
}

// MinimaxTTS implements TTS using Minimax API
type MinimaxTTS struct {
	apiKey  string
	voiceID string
	emotion string
	speed   float64
}

// NewMinimaxTTS creates a new Minimax TTS client
func NewMinimaxTTS(apiKey, voiceID, emotion string, speed float64) *MinimaxTTS {
	if voiceID == "" {
		voiceID = "male-qn-qingse"
	}
	if emotion == "" {
		emotion = "happy"
	}
	if speed == 0 {
		speed = 1.0
	}
	return &MinimaxTTS{
		apiKey:  apiKey,
		voiceID: voiceID,
		emotion: emotion,
		speed:   speed,
	}
}

// TextToSpeechStream streams TTS audio to audio_service.
// Requests PCM format directly from Minimax to avoid MP3 decoding.
func (t *MinimaxTTS) TextToSpeechStream(text string, audio *AudioServiceClient) error {
	// Request PCM at 16kHz/16-bit/mono so we can stream directly to
	// audio_service without any decoding or resampling.
	reqBody := map[string]interface{}{
		"model":  "speech-2.8-hd",
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
			"sample_rate": 16000,
			"format":      "pcm",
			"channel":     1,
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
		SampleRate: 16000,
		Channels:   1,
		BitWidth:   16,
	}

	playback, err := audio.StartPlayback(playbackFmt)
	if err != nil {
		return fmt.Errorf("start playback: %w", err)
	}

	// Make HTTP request
	req, err := http.NewRequest("POST", "https://api.minimaxi.com/v1/t2a_v2", bytes.NewReader(reqData))
	if err != nil {
		audio.StopPlayback(playback.SessionID)
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		audio.StopPlayback(playback.SessionID)
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		audio.StopPlayback(playback.SessionID)
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Stream response → parse JSON objects → hex-decode PCM → write to audio_service
	parser := newMinimaxStreamParser()
	readBuf := make([]byte, 8192)
	for {
		n, rerr := resp.Body.Read(readBuf)
		if n > 0 {
			chunks, _ := parser.feed(readBuf[:n])
			for _, pcm := range chunks {
				// Split large PCM chunks into smaller pieces so they fit
				// the AO driver's internal frame buffer (configured for
				// 1024 samples × 4 frames). Sending oversized buffers in
				// one call causes the driver to drop the tail.
				if err := writePlaybackPCM(audio, playback.SessionID, pcm); err != nil {
					audio.StopPlayback(playback.SessionID)
					return err
				}
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				audio.StopPlayback(playback.SessionID)
				return fmt.Errorf("read response: %w", rerr)
			}
			break
		}
	}

	// Pad a short silence tail to avoid clipping the last phonemes on some
	// AO drivers that stop slightly early.
	const tailMs = 200
	const bytesPerSample = 2 // s16le
	tailBytes := (16000 * tailMs / 1000) * bytesPerSample
	silenceTail := make([]byte, tailBytes)
	if err := writePlaybackPCM(audio, playback.SessionID, silenceTail); err != nil {
		return fmt.Errorf("write silence tail: %w", err)
	}

	// Final chunk drains the playback session
	if err := audio.WritePlayChunk(playback.SessionID, nil, true); err != nil {
		return fmt.Errorf("send final chunk: %w", err)
	}

	return nil
}

// writePlaybackPCM splits a PCM buffer into smaller pieces sized for the AO
// driver's internal frame buffer. Sending larger buffers in one call causes
// the driver to play only a portion before dropping the tail.
const playbackChunkBytes = 4096 // 2048 samples = 128ms @16kHz s16le mono

func writePlaybackPCM(audio *AudioServiceClient, sessionID uint64, pcm []byte) error {
	for off := 0; off < len(pcm); off += playbackChunkBytes {
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
