package agent

import (
	"fmt"
	"strings"
	"testing"

	"aiden-agent/internal/agent/contextmanager"
)

func TestAnthropicScreenshotPrunerDisabledIsNoop(t *testing.T) {
	msgs := screenshotObservationMessages(4)
	out := AnthropicScreenshotPruner{Enabled: false, Config: ScreenshotPruningConfig{}.WithDefaults()}.Transform(msgs)
	if countScreenshotSources(out) != 4 {
		t.Fatalf("disabled pruner changed messages: %#v", out)
	}
	if len(out) == 0 || &out[0] != &msgs[0] {
		t.Fatal("disabled pruner must return the same slice (pointer identity)")
	}
}

func TestAnthropicScreenshotPrunerBatchesLikePrunedCount(t *testing.T) {
	// Defaults KeepN=3 Interval=2: total 6 → pruned 2 (same as scratchpad batch tests).
	pruner := AnthropicScreenshotPruner{Enabled: true, Config: ScreenshotPruningConfig{}.WithDefaults()}
	msgs := screenshotObservationMessages(6)
	snapshot := cloneMessagesForAssert(msgs)
	out := pruner.Transform(msgs)
	assertMessagesUnchanged(t, msgs, snapshot)
	if got := countScreenshotSources(out); got != 4 {
		t.Fatalf("remaining screenshot attachments = %d, want 4", got)
	}
	if got := countImageOmitted(out); got != 2 {
		t.Fatalf("[Image omitted] count = %d, want 2", got)
	}
	assertRemainingScreenshotPaths(t, out, []string{
		"/tmp/fake-2.jpg", "/tmp/fake-3.jpg", "/tmp/fake-4.jpg", "/tmp/fake-5.jpg",
	})
}

func TestAnthropicScreenshotPrunerConfiguredKeepNInterval(t *testing.T) {
	// KeepN=2 Interval=4: total 7 → pruned 4, remaining marked attachments 3
	// (mirrors TestFunctionAgentScratchpadUsesConfiguredScreenshotPruning).
	pruner := AnthropicScreenshotPruner{Enabled: true, Config: ScreenshotPruningConfig{KeepN: 2, Interval: 4}}
	msgs := screenshotObservationMessages(7)
	snapshot := cloneMessagesForAssert(msgs)
	out := pruner.Transform(msgs)
	assertMessagesUnchanged(t, msgs, snapshot)
	if got := countScreenshotSources(out); got != 3 {
		t.Fatalf("remaining screenshot attachments = %d, want 3", got)
	}
	if got := countImageOmitted(out); got != 4 {
		t.Fatalf("[Image omitted] count = %d, want 4", got)
	}
	assertRemainingScreenshotPaths(t, out, []string{
		"/tmp/fake-4.jpg", "/tmp/fake-5.jpg", "/tmp/fake-6.jpg",
	})
}

func TestAnthropicScreenshotPrunerIgnoresUnmarkedAttachments(t *testing.T) {
	msgs := []contextmanager.Message{
		{Role: contextmanager.MessageRoleUser, Content: "upload", Attachments: []contextmanager.Attachment{{
			MIMEType: "image/png", FilePath: "/tmp/user.png",
		}}},
		{Role: contextmanager.MessageRoleUser, Content: "shot", Attachments: []contextmanager.Attachment{{
			MIMEType: "image/png", FilePath: "/tmp/s.png",
			Source: contextmanager.AttachmentSourceScreenshotObservation,
		}}},
	}
	snapshot := cloneMessagesForAssert(msgs)
	// With KeepN=3, a single marked shot should never prune; unmarked must remain.
	out := AnthropicScreenshotPruner{Enabled: true, Config: ScreenshotPruningConfig{KeepN: 3, Interval: 2}}.Transform(msgs)
	assertMessagesUnchanged(t, msgs, snapshot)
	if len(out[0].Attachments) != 1 || out[0].Attachments[0].Source != "" {
		t.Fatalf("user upload mutated: %#v", out[0])
	}
	if len(out[1].Attachments) != 1 {
		t.Fatalf("single screenshot should remain: %#v", out[1])
	}
}

func TestAnthropicScreenshotPrunerRewritesPairedToolResultWhenPruning(t *testing.T) {
	msgs := []contextmanager.Message{
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_1",
				Name:       "screenshot",
				Content:    "screenshot returned a screenshot observation: format=jpeg width=100 height=50 size=123 bytes. The image is attached in the next message.",
			}},
		},
		{
			Role:    contextmanager.MessageRoleUser,
			Content: "This image is the screenshot observation returned by the screenshot tool.",
			Attachments: []contextmanager.Attachment{{
				MIMEType: "image/jpeg",
				FilePath: "/tmp/fake-0.jpg",
				Source:   contextmanager.AttachmentSourceScreenshotObservation,
			}},
		},
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_2",
				Name:       "screenshot",
				Content:    "screenshot returned a screenshot observation: format=jpeg width=100 height=50 size=456 bytes. The image is attached in the next message.",
			}},
		},
		{
			Role:    contextmanager.MessageRoleUser,
			Content: "This image is the screenshot observation returned by the screenshot tool.",
			Attachments: []contextmanager.Attachment{{
				MIMEType: "image/jpeg",
				FilePath: "/tmp/fake-1.jpg",
				Source:   contextmanager.AttachmentSourceScreenshotObservation,
			}},
		},
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_3",
				Name:       "screenshot",
				Content:    "screenshot returned a screenshot observation: format=jpeg width=100 height=50 size=789 bytes. The image is attached in the next message.",
			}},
		},
		{
			Role:    contextmanager.MessageRoleUser,
			Content: "This image is the screenshot observation returned by the screenshot tool.",
			Attachments: []contextmanager.Attachment{{
				MIMEType: "image/jpeg",
				FilePath: "/tmp/fake-2.jpg",
				Source:   contextmanager.AttachmentSourceScreenshotObservation,
			}},
		},
		{
			Role: contextmanager.MessageRoleToolResult,
			ToolResults: []contextmanager.ToolResult{{
				ToolCallID: "call_4",
				Name:       "screenshot",
				Content:    "screenshot returned a screenshot observation: format=jpeg width=100 height=50 size=999 bytes. The image is attached in the next message.",
			}},
		},
		{
			Role:    contextmanager.MessageRoleUser,
			Content: "This image is the screenshot observation returned by the screenshot tool.",
			Attachments: []contextmanager.Attachment{{
				MIMEType: "image/jpeg",
				FilePath: "/tmp/fake-3.jpg",
				Source:   contextmanager.AttachmentSourceScreenshotObservation,
			}},
		},
	}

	out := AnthropicScreenshotPruner{
		Enabled: true,
		Config:  ScreenshotPruningConfig{KeepN: 2, Interval: 1},
	}.Transform(msgs)

	if got := out[0].ToolResults[0].Content; !strings.Contains(got, "replaced with a placeholder") {
		t.Fatalf("first tool result content = %q, want placeholder notice", got)
	}
	if got := out[2].ToolResults[0].Content; !strings.Contains(got, "The image is attached in the next message.") {
		t.Fatalf("second tool result content = %q, want attached notice for retained screenshot", got)
	}
	if got := countImageOmitted(out); got != 1 {
		t.Fatalf("[Image omitted] count = %d, want 1", got)
	}
	if got := countScreenshotSources(out); got != 3 {
		t.Fatalf("remaining screenshot attachments = %d, want 3", got)
	}
}

func screenshotObservationMessages(n int) []contextmanager.Message {
	out := make([]contextmanager.Message, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, contextmanager.Message{
			Role:    contextmanager.MessageRoleUser,
			Content: "This image is the screenshot observation returned by the screenshot tool.",
			Attachments: []contextmanager.Attachment{{
				MIMEType: "image/jpeg",
				FilePath: fmt.Sprintf("/tmp/fake-%d.jpg", i),
				Source:   contextmanager.AttachmentSourceScreenshotObservation,
			}},
		})
	}
	return out
}

func cloneMessagesForAssert(messages []contextmanager.Message) []contextmanager.Message {
	out := make([]contextmanager.Message, len(messages))
	for i, msg := range messages {
		cloned := msg
		if len(msg.Attachments) > 0 {
			cloned.Attachments = append([]contextmanager.Attachment(nil), msg.Attachments...)
		}
		if len(msg.ToolCalls) > 0 {
			cloned.ToolCalls = append([]contextmanager.ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ToolResults) > 0 {
			cloned.ToolResults = append([]contextmanager.ToolResult(nil), msg.ToolResults...)
		}
		out[i] = cloned
	}
	return out
}

func assertMessagesUnchanged(t *testing.T, got, want []contextmanager.Message) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("input slice length mutated: got %d want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Content != want[i].Content {
			t.Fatalf("input[%d].Content mutated: got %q want %q", i, got[i].Content, want[i].Content)
		}
		if len(got[i].Attachments) != len(want[i].Attachments) {
			t.Fatalf("input[%d].Attachments length mutated: got %d want %d", i, len(got[i].Attachments), len(want[i].Attachments))
		}
		for j := range want[i].Attachments {
			if got[i].Attachments[j] != want[i].Attachments[j] {
				t.Fatalf("input[%d].Attachments[%d] mutated: got %#v want %#v", i, j, got[i].Attachments[j], want[i].Attachments[j])
			}
		}
	}
}

func assertRemainingScreenshotPaths(t *testing.T, messages []contextmanager.Message, wantPaths []string) {
	t.Helper()
	var got []string
	for _, msg := range messages {
		for _, a := range msg.Attachments {
			if a.Source == contextmanager.AttachmentSourceScreenshotObservation {
				got = append(got, a.FilePath)
			}
		}
	}
	if len(got) != len(wantPaths) {
		t.Fatalf("remaining screenshot paths = %v, want %v", got, wantPaths)
	}
	for i := range wantPaths {
		if got[i] != wantPaths[i] {
			t.Fatalf("remaining screenshot paths = %v, want %v", got, wantPaths)
		}
	}
}

func countScreenshotSources(messages []contextmanager.Message) int {
	n := 0
	for _, msg := range messages {
		for _, a := range msg.Attachments {
			if a.Source == contextmanager.AttachmentSourceScreenshotObservation {
				n++
			}
		}
	}
	return n
}

func countImageOmitted(messages []contextmanager.Message) int {
	n := 0
	for _, msg := range messages {
		n += strings.Count(msg.Content, "[Image omitted]")
	}
	return n
}
