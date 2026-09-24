package session

import (
	"aiden-agent/internal/agent/messages"
	"errors"
	"reflect"
	"testing"
)

func TestNewAndCloneMessageListOwnMessageData(t *testing.T) {
	input := []messages.Message{{
		Role:    messages.MessageRoleToolResult,
		Content: "before",
		Usage:   &messages.Usage{InputTokens: 1},
		ToolResults: []messages.ToolResult{{
			ToolCallID: "call-1",
			Meta:       &messages.ToolResultMeta{Summary: "summary"},
		}},
		Attachments:             []messages.Attachment{{FilePath: "attachment.bin"}},
		ResponsesReasoningItems: nil,
	}}
	s := New("s_current", "s_parent", input)

	input[0].Content = "mutated input"
	input[0].Usage.InputTokens = 99
	input[0].ToolResults[0].Meta.Summary = "mutated input"
	input[0].Attachments[0].FilePath = "mutated input"

	got := s.CloneMessageList()
	if got[0].Content != "before" || got[0].Usage.InputTokens != 1 ||
		got[0].ToolResults[0].Meta.Summary != "summary" ||
		got[0].Attachments[0].FilePath != "attachment.bin" {
		t.Fatalf("session retained caller mutation: %#v", got)
	}

	got[0].Content = "mutated snapshot"
	got[0].Usage.InputTokens = 2
	got[0].ToolResults[0].Meta.Summary = "mutated snapshot"
	got[0].Attachments[0].FilePath = "mutated snapshot"
	if want := "before"; s.CloneMessageList()[0].Content != want {
		t.Fatalf("session retained snapshot mutation: content = %q, want %q", s.CloneMessageList()[0].Content, want)
	}

	if gotID, wantID := s.ID(), "s_current"; gotID != wantID {
		t.Fatalf("ID = %q, want %q", gotID, wantID)
	}
	if gotParent, wantParent := s.ParentID(), "s_parent"; gotParent != wantParent {
		t.Fatalf("ParentID = %q, want %q", gotParent, wantParent)
	}
	if s.IsEmpty() {
		t.Fatal("session unexpectedly empty")
	}
}

func TestAppendMessagesClonesAndPreservesOrder(t *testing.T) {
	s := New("s_current", "", nil)
	first := messages.Message{Role: messages.MessageRoleUser, Content: "first"}
	second := messages.Message{Role: messages.MessageRoleUser, Content: "second"}
	s.AppendMessages([]messages.Message{first, second})

	got := s.CloneMessageList()
	want := []messages.Message{first, second}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Messages() = %#v, want %#v", got, want)
	}

	first.Content = "changed"
	if got := s.CloneMessageList()[0].Content; got != "first" {
		t.Fatalf("stored message changed through input: %q", got)
	}
}

func TestAppendMessagesPublishesOnlyAfterPersistence(t *testing.T) {
	persistErr := errors.New("disk full")
	persisted := 0
	s := NewWithStoresAndPersistence("s_current", "", nil, nil, nil, func(messageList []messages.Message) error {
		persisted += len(messageList)
		return persistErr
	})

	if err := s.AppendMessages([]messages.Message{{Role: messages.MessageRoleUser, Content: "not committed"}}); !errors.Is(err, persistErr) {
		t.Fatalf("AppendMessages() error = %v, want %v", err, persistErr)
	}
	if persisted != 1 {
		t.Fatalf("persisted message count = %d, want 1", persisted)
	}
	if !s.IsEmpty() {
		t.Fatal("session changed after persistence failure")
	}
}
