package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"aiden-agent/internal/agent/tts"
)

func TestLastSentenceBoundaryHandlesUTF8Punctuation(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		{name: "none", text: "没有结束符", want: -1},
		{name: "ascii", text: "English. remainder", want: len("English")},
		{name: "period", text: "第一句。后续", want: len("第一句")},
		{name: "exclamation", text: "第一句！后续", want: len("第一句")},
		{name: "question", text: "第一句？后续", want: len("第一句")},
		{name: "semicolon", text: "第一句；后续", want: len("第一句")},
		{name: "last boundary", text: "第一句。第二句！后续", want: len("第一句。第二句")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastSentenceBoundary(tt.text); got != tt.want {
				t.Fatalf("lastSentenceBoundary(%q) = %d, want %d", tt.text, got, tt.want)
			}
		})
	}
}

func TestSessionSynthesizesCompleteChineseSentences(t *testing.T) {
	var (
		mu     sync.Mutex
		inputs []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request speechRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		inputs = append(inputs, request.Input)
		mu.Unlock()
		_, _ = w.Write([]byte{0, 1, 2, 3})
	}))
	defer server.Close()

	provider, err := New(tts.ProviderConfig{APIKey: "test-key", Endpoint: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer provider.Close()

	sink := &openRouterTestSink{}
	session, err := provider.BeginStream(context.Background(), sink)
	if err != nil {
		t.Fatalf("BeginStream() error = %v", err)
	}

	if err := session.WriteText("第一句"); err != nil {
		t.Fatalf("WriteText(first fragment) error = %v", err)
	}
	if err := session.WriteText("。第二句！第三句？第四句；余下"); err != nil {
		t.Fatalf("WriteText(second fragment) error = %v", err)
	}
	mu.Lock()
	gotInputs := append([]string(nil), inputs...)
	mu.Unlock()
	if len(gotInputs) != 1 || gotInputs[0] != "第一句。第二句！第三句？第四句；" {
		t.Fatalf("synthesized inputs after WriteText = %#v, want complete Chinese sentences", gotInputs)
	}

	if err := session.Flush(); err != nil {
		t.Fatalf("Flush() error = %v", err)
	}
	mu.Lock()
	gotInputs = append([]string(nil), inputs...)
	mu.Unlock()
	if len(gotInputs) != 2 || gotInputs[1] != "余下" {
		t.Fatalf("synthesized inputs after Flush = %#v, want remainder %q", gotInputs, "余下")
	}

	if err := session.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !sink.drained {
		t.Fatal("Close() did not drain the sink")
	}
}

type openRouterTestSink struct {
	mu      sync.Mutex
	pcm     []byte
	drained bool
}

func (s *openRouterTestSink) Format() tts.AudioFormat {
	return tts.AudioFormat{SampleRate: 24000, Channels: 1, BitWidth: 16}
}

func (s *openRouterTestSink) WritePCM(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pcm = append(s.pcm, data...)
	return nil
}

func (s *openRouterTestSink) Drain(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drained = true
	return nil
}

func (s *openRouterTestSink) Stop() error { return nil }
