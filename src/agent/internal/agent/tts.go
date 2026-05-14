package agent

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
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

// TextToSpeechStream streams TTS audio to audio_service
func (t *MinimaxTTS) TextToSpeechStream(text string, audio *AudioServiceClient) error {
	// Build request
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
			"sample_rate": 32000,
			"bitrate":     128000,
			"format":      "mp3",
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

	// Open playback session (16kHz/16bit/mono - ffmpeg output)
	playbackFmt := AudioFormat{
		SampleRate: 16000,
		Channels:   1,
		BitWidth:   16,
	}

	playback, err := audio.StartPlayback(playbackFmt)
	if err != nil {
		return fmt.Errorf("start playback: %w", err)
	}
	defer audio.StopPlayback(playback.SessionID)

	// Spawn ffmpeg: mp3 stdin → pcm s16le 16kHz mono stdout
	cmd := exec.Command("ffmpeg",
		"-i", "pipe:0",
		"-f", "s16le",
		"-ar", "16000",
		"-ac", "1",
		"pipe:1")

	ffmpegStdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	ffmpegStdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	// Start PCM reader goroutine
	var wg sync.WaitGroup
	var writeErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := ffmpegStdout.Read(buf)
			if n > 0 {
				if err := audio.WritePlayChunk(playback.SessionID, buf[:n], false); err != nil {
					writeErr = fmt.Errorf("write play chunk: %w", err)
					return
				}
			}
			if err != nil {
				break
			}
		}
	}()

	// Make HTTP request
	req, err := http.NewRequest("POST", "https://api.minimaxi.com/v1/t2a_v2", bytes.NewReader(reqData))
	if err != nil {
		ffmpegStdin.Close()
		cmd.Wait()
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		ffmpegStdin.Close()
		cmd.Wait()
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		ffmpegStdin.Close()
		cmd.Wait()
		return fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Stream response to ffmpeg
	parser := NewMinimaxStreamParser()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		chunks := parser.Feed(line)
		for _, mp3Chunk := range chunks {
			if len(mp3Chunk) > 0 {
				if _, err := ffmpegStdin.Write(mp3Chunk); err != nil {
					ffmpegStdin.Close()
					cmd.Wait()
					return fmt.Errorf("write to ffmpeg: %w", err)
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		ffmpegStdin.Close()
		cmd.Wait()
		return fmt.Errorf("read response: %w", err)
	}

	// Close ffmpeg stdin and wait for completion
	ffmpegStdin.Close()
	wg.Wait()
	cmd.Wait()

	if writeErr != nil {
		return writeErr
	}

	// Send final chunk
	return audio.WritePlayChunk(playback.SessionID, []byte{}, true)
}

// MinimaxStreamParser parses Minimax streaming response
type MinimaxStreamParser struct {
	buffer []byte
}

// NewMinimaxStreamParser creates a new stream parser
func NewMinimaxStreamParser() *MinimaxStreamParser {
	return &MinimaxStreamParser{}
}

// Feed processes incoming data and returns MP3 chunks
func (p *MinimaxStreamParser) Feed(line string) [][]byte {
	// Minimax streams data as SSE (Server-Sent Events)
	// Format: data: {"data":{"audio":"base64..."}}
	if !bytes.HasPrefix([]byte(line), []byte("data: ")) {
		return nil
	}

	jsonData := line[6:] // Skip "data: "
	var event struct {
		Data struct {
			Audio string `json:"audio"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(jsonData), &event); err != nil {
		return nil
	}

	if event.Data.Audio == "" {
		return nil
	}

	// Decode base64 MP3 data
	mp3Data := make([]byte, len(event.Data.Audio)*3/4)
	n, err := base64Decode(mp3Data, []byte(event.Data.Audio))
	if err != nil {
		return nil
	}

	return [][]byte{mp3Data[:n]}
}

// base64Decode is a simple base64 decoder
func base64Decode(dst, src []byte) (int, error) {
	return base64.StdEncoding.Decode(dst, src)
}
