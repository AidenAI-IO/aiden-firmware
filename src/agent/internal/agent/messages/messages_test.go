package messages

import (
	"encoding/json"
	"reflect"
	"testing"
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
