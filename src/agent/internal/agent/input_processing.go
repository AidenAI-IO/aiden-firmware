package agent

import (
	"fmt"
	"strings"
)

const (
	AttachmentKindImage = "image"
	AttachmentKindAudio = "audio"
)

type InputAttachment struct {
	Kind     string
	Name     string
	MIMEType string
	Data     []byte
}

type AudioInputResult struct {
	InputText   string
	Attachments []InputAttachment
	Transcript  string
}

func NewSTTClientFromConfig(cfg Config) (STTClient, error) {
	if strings.TrimSpace(cfg.STT.Provider) == "" {
		return nil, nil
	}

	switch strings.ToLower(cfg.STT.Provider) {
	case "openai", "openai-whisper":
		return NewOpenAIWhisperSTT(cfg.STT.APIKey, cfg.STT.Model, cfg.STT.BaseURL), nil
	case "tencent":
		return NewTencentASRSTT(cfg.STT.SecretID, cfg.STT.SecretKey, cfg.STT.Region, cfg.STT.EngineModelType), nil
	default:
		return nil, fmt.Errorf("unsupported STT provider: %s", cfg.STT.Provider)
	}
}

func NewTTSClientFromConfig(cfg Config) (TTSClient, error) {
	if strings.TrimSpace(cfg.TTS.Provider) == "" {
		return nil, nil
	}

	switch strings.ToLower(cfg.TTS.Provider) {
	case "minimax":
		return NewMinimaxTTS(cfg.TTS.APIKey, cfg.TTS.VoiceID, cfg.TTS.Emotion, cfg.TTS.Speed), nil
	default:
		return nil, fmt.Errorf("unsupported TTS provider: %s", cfg.TTS.Provider)
	}
}

func PrepareAudioInput(mode string, sttClient STTClient, wavData []byte, userText string, attachments []InputAttachment) (AudioInputResult, error) {
	resolvedMode := strings.ToLower(strings.TrimSpace(mode))
	if resolvedMode == "" {
		resolvedMode = "stt"
	}

	finalAttachments := append([]InputAttachment{}, attachments...)
	trimmedText := strings.TrimSpace(userText)

	switch resolvedMode {
	case "stt":
		if sttClient == nil {
			return AudioInputResult{}, fmt.Errorf("STT mode enabled but STT client is unavailable")
		}

		transcript, err := sttClient.TranscribeWAV(wavData)
		if err != nil {
			return AudioInputResult{}, fmt.Errorf("STT transcription failed: %w", err)
		}
		transcript = strings.TrimSpace(transcript)
		if transcript == "" {
			return AudioInputResult{}, fmt.Errorf("STT returned empty transcript")
		}

		return AudioInputResult{
			InputText:   normalizeRunInput(mergeUserTextAndTranscript(trimmedText, transcript), finalAttachments),
			Attachments: finalAttachments,
			Transcript:  transcript,
		}, nil

	case "audio":
		finalAttachments = append(finalAttachments, InputAttachment{
			Kind:     AttachmentKindAudio,
			Name:     "recording.wav",
			MIMEType: "audio/wav",
			Data:     wavData,
		})
		return AudioInputResult{
			InputText:   normalizeRunInput(trimmedText, finalAttachments),
			Attachments: finalAttachments,
		}, nil

	default:
		return AudioInputResult{}, fmt.Errorf("invalid audio input mode: %s", mode)
	}
}

func mergeUserTextAndTranscript(userText, transcript string) string {
	userText = strings.TrimSpace(userText)
	transcript = strings.TrimSpace(transcript)

	switch {
	case userText == "":
		return transcript
	case transcript == "":
		return userText
	case strings.EqualFold(userText, transcript):
		return userText
	default:
		return userText + "\n\nVoice transcript:\n" + transcript
	}
}

func normalizeRunInput(input string, attachments []InputAttachment) string {
	input = strings.TrimSpace(input)
	if input != "" {
		return input
	}

	if len(attachments) == 0 {
		return ""
	}

	hasImage := false
	hasAudio := false
	for _, attachment := range attachments {
		switch attachment.Kind {
		case AttachmentKindImage:
			hasImage = true
		case AttachmentKindAudio:
			hasAudio = true
		}
	}

	switch {
	case hasImage && hasAudio:
		return "Please analyze the attached image and audio."
	case hasImage:
		return "Please analyze the attached image."
	case hasAudio:
		return "Please analyze the attached audio."
	default:
		return "Please analyze the attached content."
	}
}
