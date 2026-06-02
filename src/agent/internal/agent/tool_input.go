package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// LLMs frequently stringify structured tool arguments, emitting `"tags": "[]"`
// or `"limit": "3"` instead of a real JSON array or number. Strict decoding
// rejects those payloads and the whole tool call fails. The flex types below
// tolerate those shapes so a malformed-but-recoverable argument still works.

// flexStringSlice decodes a JSON array of strings, but also tolerates a single
// bare string ("foo" -> ["foo"]) and a string that itself contains a JSON
// array ("[]" or "[\"a\",\"b\"]" -> the decoded array).
type flexStringSlice []string

func (f *flexStringSlice) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*f = nil
		return nil
	}

	switch trimmed[0] {
	case '[':
		var arr []string
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return err
		}
		*f = arr
		return nil
	case '"':
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = nil
			return nil
		}
		// A string that wraps a JSON array, e.g. "[\"a\",\"b\"]".
		if s[0] == '[' {
			var arr []string
			if err := json.Unmarshal([]byte(s), &arr); err != nil {
				return fmt.Errorf("decode string-wrapped array %q: %w", s, err)
			}
			*f = arr
			return nil
		}
		// A bare single value, e.g. "foo".
		*f = []string{s}
		return nil
	default:
		return fmt.Errorf("cannot decode %s into string slice", trimmed)
	}
}

// flexInt decodes a JSON number, but also tolerates a numeric string ("3"),
// a float ("3.0" or 3.0), and an empty/null value (-> 0).
type flexInt int

func (f *flexInt) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		*f = 0
		return nil
	}

	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err != nil {
			return err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			*f = 0
			return nil
		}
		n, err := parseFlexInt(s)
		if err != nil {
			return err
		}
		*f = flexInt(n)
		return nil
	}

	// Plain JSON number; decode as float so "3.0" style values are accepted.
	var n float64
	if err := json.Unmarshal(trimmed, &n); err != nil {
		return fmt.Errorf("cannot decode %s into int", trimmed)
	}
	*f = flexInt(int(n))
	return nil
}

func parseFlexInt(s string) (int, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return n, nil
	}
	if ff, err := strconv.ParseFloat(s, 64); err == nil {
		return int(ff), nil
	}
	return 0, fmt.Errorf("cannot decode %q into int", s)
}

// decodeChunkRecallQuery tolerantly decodes a recall_session_chunks argument.
func decodeChunkRecallQuery(input string) (ChunkRecallQuery, error) {
	var flex struct {
		ChunkIDs flexStringSlice `json:"chunk_ids"`
		Tags     flexStringSlice `json:"tags"`
		Entities flexStringSlice `json:"entities"`
		AppName  string          `json:"app_name"`
		Limit    flexInt         `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &flex); err != nil {
		return ChunkRecallQuery{}, err
	}
	return ChunkRecallQuery{
		ChunkIDs: flex.ChunkIDs,
		Tags:     flex.Tags,
		Entities: flex.Entities,
		AppName:  flex.AppName,
		Limit:    int(flex.Limit),
	}, nil
}

// decodeMemoryQuery tolerantly decodes a recall_memory argument.
func decodeMemoryQuery(input string) (MemoryQuery, error) {
	var flex struct {
		Tags     flexStringSlice `json:"tags"`
		Entities flexStringSlice `json:"entities"`
		Types    flexStringSlice `json:"types"`
		Limit    flexInt         `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &flex); err != nil {
		return MemoryQuery{}, err
	}
	return MemoryQuery{
		Tags:     flex.Tags,
		Entities: flex.Entities,
		Types:    flex.Types,
		Limit:    int(flex.Limit),
	}, nil
}

// decodeDeviceMemoryQuery tolerantly decodes a recall_device_memory argument.
func decodeDeviceMemoryQuery(input string) (DeviceMemoryQuery, error) {
	var flex struct {
		Terms    flexStringSlice `json:"terms"`
		Tags     flexStringSlice `json:"tags"`
		Entities flexStringSlice `json:"entities"`
		DeviceID string          `json:"device_id"`
		Types    flexStringSlice `json:"types"`
		Limit    flexInt         `json:"limit"`
	}
	if err := json.Unmarshal([]byte(input), &flex); err != nil {
		return DeviceMemoryQuery{}, err
	}
	return DeviceMemoryQuery{
		Terms:    flex.Terms,
		Tags:     flex.Tags,
		Entities: flex.Entities,
		DeviceID: flex.DeviceID,
		Types:    flex.Types,
		Limit:    int(flex.Limit),
	}, nil
}

// SaveMemoryRequest represents the input for save_memory tool.
type SaveMemoryRequest struct {
	Type     string
	Title    string
	Content  string
	Tags     []string
	Entities []string
	Evidence []string
	Priority int
}

// decodeSaveMemoryRequest tolerantly decodes a save_memory argument.
func decodeSaveMemoryRequest(input string) (SaveMemoryRequest, error) {
	var flex struct {
		Type     string          `json:"type"`
		Title    string          `json:"title"`
		Content  string          `json:"content"`
		Tags     flexStringSlice `json:"tags"`
		Entities flexStringSlice `json:"entities"`
		Evidence flexStringSlice `json:"evidence"`
		Priority flexInt         `json:"priority"`
	}
	if err := json.Unmarshal([]byte(input), &flex); err != nil {
		return SaveMemoryRequest{}, err
	}
	return SaveMemoryRequest{
		Type:     flex.Type,
		Title:    flex.Title,
		Content:  flex.Content,
		Tags:     flex.Tags,
		Entities: flex.Entities,
		Evidence: flex.Evidence,
		Priority: int(flex.Priority),
	}, nil
}

// ForgetMemoryRequest represents the input for forget_memory tool.
type ForgetMemoryRequest struct {
	ID     string
	Reason string
}

// decodeForgetMemoryRequest tolerantly decodes a forget_memory argument.
func decodeForgetMemoryRequest(input string) (ForgetMemoryRequest, error) {
	var req ForgetMemoryRequest
	if err := json.Unmarshal([]byte(input), &req); err != nil {
		return ForgetMemoryRequest{}, err
	}
	return req, nil
}
