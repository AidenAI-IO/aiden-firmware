package realtimevoice

import "testing"

func TestProviderInterfaceSeparatesCoreAndTextCapabilities(t *testing.T) {
	var _ Provider = SpekoProvider{}
	var _ TextSession = (*qwenSession)(nil)
	var _ ContextReplayer = (*qwenSession)(nil)
	var _ ContextReplayer = (*openAISession)(nil)
	var _ ContextReplayer = (*xAISession)(nil)
	var _ ContextReplayer = (*geminiSession)(nil)
	if _, ok := any((*geminiSession)(nil)).(TurnCommitter); ok {
		t.Fatal("Gemini Live must not advertise client-side commit")
	}
	if _, ok := any((*geminiSession)(nil)).(ResponseInterrupter); ok {
		t.Fatal("Gemini Live must not advertise client interruption")
	}
}

func TestTurnDetectionConfigOmitsUnsetValues(t *testing.T) {
	if got := turnDetectionConfig(SessionConfig{TurnDetection: "disabled"}); got != nil {
		t.Fatalf("disabled turn detection = %#v, want nil", got)
	}
	got := turnDetectionConfig(SessionConfig{TurnDetection: "server_vad"})
	if len(got) != 1 || got["type"] != "server_vad" {
		t.Fatalf("default turn detection = %#v", got)
	}
	threshold := 0.25
	got = turnDetectionConfig(SessionConfig{TurnDetection: "server_vad", TurnDetectionThresh: &threshold, TurnDetectionSilenceMs: 700})
	if len(got) != 3 || got["threshold"] != threshold || got["silence_duration_ms"] != 700 {
		t.Fatalf("configured turn detection = %#v", got)
	}
}

func TestPCM16SessionInfoKeepsRatesAndFormatsInSync(t *testing.T) {
	info := newPCM16SessionInfo("session-1", 16000, 24000, Capabilities{ExplicitToolContinuation: true})
	if info.ID != "session-1" || info.InputSampleRate != info.InputAudioFormat.SampleRate || info.OutputSampleRate != info.OutputAudioFormat.SampleRate {
		t.Fatalf("session info = %+v", info)
	}
	if info.InputAudioFormat.Encoding != "pcm_s16le" || info.OutputAudioFormat.Encoding != "pcm_s16le" {
		t.Fatalf("session encodings = %+v", info)
	}
}

func TestAudioFormatDefaultsAndSessionInfoFallback(t *testing.T) {
	format := (AudioFormat{Encoding: "pcm_s16le"}).OrDefault(16000)
	if format.SampleRate != 16000 || format.Channels != 1 || format.BitDepth != 16 {
		t.Fatalf("format defaults = %+v", format)
	}
	info := SessionInfo{InputSampleRate: 12000, OutputAudioFormat: AudioFormat{SampleRate: 22050, Channels: 2, BitDepth: 24}}
	if got := info.InputFormatOrDefault(16000); got.SampleRate != 12000 || got.Channels != 1 || got.BitDepth != 16 {
		t.Fatalf("input format fallback = %+v", got)
	}
	if got := info.OutputFormatOrDefault(24000); got.SampleRate != 22050 || got.Channels != 2 || got.BitDepth != 24 {
		t.Fatalf("output format fallback = %+v", got)
	}
}

func TestContextItemPayloadOmitsEmptyFields(t *testing.T) {
	// xAI validates item.role against an enum, so a function_call item must not
	// carry role="". Sending it ends the session with code=invalid_event as soon
	// as history replay starts.
	call := contextItemPayload(ContextItem{Type: "function_call", CallID: "call_1", Name: "get_time", Arguments: "{}"})
	if _, present := call["role"]; present {
		t.Fatalf("function_call payload must omit role, got %#v", call)
	}
	if _, present := call["content"]; present {
		t.Fatalf("function_call payload must omit content, got %#v", call)
	}
	if call["call_id"] != "call_1" || call["name"] != "get_time" || call["arguments"] != "{}" {
		t.Fatalf("function_call payload lost fields: %#v", call)
	}

	output := contextItemPayload(ContextItem{Type: "function_call_output", CallID: "call_1", Output: "12:00"})
	if _, present := output["role"]; present {
		t.Fatalf("function_call_output payload must omit role, got %#v", output)
	}
	if output["output"] != "12:00" {
		t.Fatalf("function_call_output payload lost output: %#v", output)
	}

	user := contextItemPayload(ContextItem{Type: "message", Role: "user", Content: "hi"})
	if user["role"] != "user" {
		t.Fatalf("message payload role = %#v", user["role"])
	}
	if _, present := user["call_id"]; present {
		t.Fatalf("message payload must omit call_id, got %#v", user)
	}
	content, ok := user["content"].([]map[string]string)
	if !ok || len(content) != 1 || content[0]["type"] != "input_text" || content[0]["text"] != "hi" {
		t.Fatalf("user content = %#v", user["content"])
	}

	assistant := contextItemPayload(ContextItem{Type: "message", Role: "assistant", Content: "yo"})
	assistantContent, ok := assistant["content"].([]map[string]string)
	if !ok || len(assistantContent) != 1 || assistantContent[0]["type"] != "output_text" {
		t.Fatalf("assistant content = %#v", assistant["content"])
	}
}
