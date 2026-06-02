package agent

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestFlexStringSlice(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "real JSON array",
			input: `["a","b","c"]`,
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "empty JSON array",
			input: `[]`,
			want:  []string{},
		},
		{
			name:  "string-wrapped empty array (common LLM mistake)",
			input: `"[]"`,
			want:  []string{},
		},
		{
			name:  "string-wrapped array (common LLM mistake)",
			input: `"[\"验证码\",\"登录\"]"`,
			want:  []string{"验证码", "登录"},
		},
		{
			name:  "bare single string",
			input: `"foo"`,
			want:  []string{"foo"},
		},
		{
			name:  "null",
			input: `null`,
			want:  nil,
		},
		{
			name:  "empty string",
			input: `""`,
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got flexStringSlice
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("Unmarshal(%q) error = %v", tt.input, err)
			}
			if !reflect.DeepEqual([]string(got), tt.want) {
				t.Errorf("Unmarshal(%q) = %#v, want %#v", tt.input, []string(got), tt.want)
			}
		})
	}
}

func TestFlexInt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{
			name:  "plain JSON number",
			input: `42`,
			want:  42,
		},
		{
			name:  "string-wrapped number (common LLM mistake)",
			input: `"3"`,
			want:  3,
		},
		{
			name:  "float JSON number",
			input: `3.0`,
			want:  3,
		},
		{
			name:  "string-wrapped float",
			input: `"3.0"`,
			want:  3,
		},
		{
			name:  "null",
			input: `null`,
			want:  0,
		},
		{
			name:  "empty string",
			input: `""`,
			want:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got flexInt
			if err := json.Unmarshal([]byte(tt.input), &got); err != nil {
				t.Fatalf("Unmarshal(%q) error = %v", tt.input, err)
			}
			if int(got) != tt.want {
				t.Errorf("Unmarshal(%q) = %d, want %d", tt.input, int(got), tt.want)
			}
		})
	}
}

// TestDecodeChunkRecallQueryToleratesStringifiedArgs verifies the exact
// failure mode from the logs: tags and limit provided as JSON strings.
func TestDecodeChunkRecallQueryToleratesStringifiedArgs(t *testing.T) {
	// This is the exact malformed payload from the logs:
	// {"tags": "[]", "limit": "3"}
	input := `{"tags": "[]", "limit": "3"}`
	got, err := decodeChunkRecallQuery(input)
	if err != nil {
		t.Fatalf("decodeChunkRecallQuery(%q) error = %v; want success", input, err)
	}
	want := ChunkRecallQuery{
		Tags:  []string{},
		Limit: 3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeChunkRecallQuery(%q) = %#v, want %#v", input, got, want)
	}
}

func TestDecodeChunkRecallQueryWellFormed(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  ChunkRecallQuery
	}{
		{
			name:  "chunk_ids",
			input: `{"chunk_ids":["chunk_001","chunk_002"]}`,
			want: ChunkRecallQuery{
				ChunkIDs: []string{"chunk_001", "chunk_002"},
			},
		},
		{
			name:  "tags and entities",
			input: `{"tags":["验证码"],"entities":["某政务App"],"limit":5}`,
			want: ChunkRecallQuery{
				Tags:     []string{"验证码"},
				Entities: []string{"某政务App"},
				Limit:    5,
			},
		},
		{
			name:  "app_name filter",
			input: `{"app_name":"Gmail","limit":10}`,
			want: ChunkRecallQuery{
				AppName: "Gmail",
				Limit:   10,
			},
		},
		{
			name:  "empty query (all fields omitted)",
			input: `{}`,
			want:  ChunkRecallQuery{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeChunkRecallQuery(tt.input)
			if err != nil {
				t.Fatalf("decodeChunkRecallQuery(%q) error = %v", tt.input, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("decodeChunkRecallQuery(%q) = %#v, want %#v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDecodeMemoryQueryToleratesStringifiedArgs(t *testing.T) {
	input := `{"tags":"[\"verification\"]","types":"[\"preference\"]","limit":"5"}`
	got, err := decodeMemoryQuery(input)
	if err != nil {
		t.Fatalf("decodeMemoryQuery(%q) error = %v; want success", input, err)
	}
	want := MemoryQuery{
		Tags:  []string{"verification"},
		Types: []string{"preference"},
		Limit: 5,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeMemoryQuery(%q) = %#v, want %#v", input, got, want)
	}
}

func TestDecodeDeviceMemoryQueryToleratesStringifiedArgs(t *testing.T) {
	input := `{"terms":"[\"微信\"]","tags":"[]","limit":"3"}`
	got, err := decodeDeviceMemoryQuery(input)
	if err != nil {
		t.Fatalf("decodeDeviceMemoryQuery(%q) error = %v; want success", input, err)
	}
	want := DeviceMemoryQuery{
		Terms: []string{"微信"},
		Tags:  []string{},
		Limit: 3,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("decodeDeviceMemoryQuery(%q) = %#v, want %#v", input, got, want)
	}
}
