package messages

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func TestToolResultMetaUnmarshalJSON(t *testing.T) {
	want := ToolResultMeta{
		ArtifactPath:        "/tmp/tool-results/result.data",
		OriginalBytes:       2048,
		OriginalChars:       1024,
		EstimatedTokens:     256,
		Complete:            true,
		ArtifactComplete:    true,
		Reason:              "tool_result_budget",
		Summary:             "command completed",
		ActionCompleted:     true,
		ObservationComplete: true,
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var got ToolResultMeta
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func TestConvertMessageListWrapsNoticeContent(t *testing.T) {
	converted := ConvertMessageList([]Message{{
		Role:    MessageRoleNotice,
		Content: "change strategy",
	}})

	if len(converted) != 1 || converted[0].Role != llms.ChatMessageTypeHuman || len(converted[0].Parts) != 1 {
		t.Fatalf("converted messages = %#v", converted)
	}
	text, ok := converted[0].Parts[0].(llms.TextContent)
	if !ok || text.Text != "<notice>\nchange strategy\n</notice>" {
		t.Fatalf("notice content = %#v", converted[0].Parts[0])
	}
}

func TestConvertMessageListWrapsStateContent(t *testing.T) {
	converted := ConvertMessageList([]Message{{
		Role:    MessageRoleState,
		Content: "device_type: phone",
	}})

	text, ok := converted[0].Parts[0].(llms.TextContent)
	if !ok || text.Text != "<state>\ndevice_type: phone\n</state>" {
		t.Fatalf("state content = %#v", converted[0].Parts[0])
	}
}
