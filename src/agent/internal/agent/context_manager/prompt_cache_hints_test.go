package context_manager

import (
	"testing"
)

func TestConvertToStandardMessageListWithCacheHints(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role: MessageRoleSystem,
		PromptSections: []PromptSection{
			{Text: "stable prefix", CacheEphemeral: true},
			{Text: "dynamic suffix"},
		},
	})
	manager.AppendMessage(Message{
		Role:    MessageRoleUser,
		Content: "hello",
	})

	messages, hints := manager.ConvertToStandardMessageListWithCacheHints()
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(messages))
	}
	if len(messages[0].Parts) != 2 {
		t.Fatalf("system parts = %d, want 2", len(messages[0].Parts))
	}
	if len(hints.EphemeralParts) != 1 {
		t.Fatalf("hints = %#v, want one ephemeral part", hints.EphemeralParts)
	}
	if got := hints.EphemeralParts[0]; got.MessageIndex != 0 || got.PartIndex != 0 {
		t.Fatalf("hint = %#v, want message 0 part 0", got)
	}
	if !hints.ShouldCache(0, 0) || hints.ShouldCache(0, 1) || hints.ShouldCache(1, 0) {
		t.Fatalf("unexpected ShouldCache results for hints=%#v", hints.EphemeralParts)
	}
}

func TestConvertToStandardMessageListWithCacheHintsSingleStablePart(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role: MessageRoleSystem,
		PromptSections: []PromptSection{
			{Text: "entire stable system prompt", CacheEphemeral: true},
		},
	})

	messages, hints := manager.ConvertToStandardMessageListWithCacheHints()
	if len(messages[0].Parts) != 1 {
		t.Fatalf("system parts = %d, want single stable part", len(messages[0].Parts))
	}
	if !hints.ShouldCache(0, 0) {
		t.Fatalf("single stable system part should be cacheable")
	}
}

func TestWithPromptCacheHintsRoundTrip(t *testing.T) {
	hints := PromptCacheHints{
		EphemeralParts: []PromptCachePartHint{{MessageIndex: 0, PartIndex: 0}},
	}
	ctx := WithPromptCacheHints(t.Context(), hints)
	got, ok := PromptCacheHintsFromContext(ctx)
	if !ok {
		t.Fatal("expected hints in context")
	}
	if !got.ShouldCache(0, 0) {
		t.Fatalf("round-trip hints = %#v", got.EphemeralParts)
	}
}

func TestMessageListDumpCopiesPromptSections(t *testing.T) {
	manager := NewContextManager()
	manager.AppendMessage(Message{
		Role: MessageRoleSystem,
		PromptSections: []PromptSection{
			{Text: "stable prefix", CacheEphemeral: true},
		},
	})

	dump := manager.MessageListDump()
	dump.Messages[0].PromptSections[0].Text = "mutated"
	dump.Messages[0].PromptSections[0].CacheEphemeral = false

	got := manager.MessageListDump().Messages[0].PromptSections[0]
	if got.Text != "stable prefix" || !got.CacheEphemeral {
		t.Fatalf("internal prompt section mutated through dump: %#v", got)
	}
}
