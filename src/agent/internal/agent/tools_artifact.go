package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"aiden-agent/internal/agent/contextmanager"
)

type ArtifactReadTool struct {
	managerFn func() *contextmanager.ContextManager
}

func NewArtifactReadTool(managerFn func() *contextmanager.ContextManager) *ArtifactReadTool {
	return &ArtifactReadTool{managerFn: managerFn}
}

func (t *ArtifactReadTool) Name() string { return "artifact_read" }

func (t *ArtifactReadTool) Description() string {
	return "Read a bounded page from a large tool-result artifact. If the original tool exposes a source file or native pagination API, prefer that original source. Otherwise use artifact_read only when the current bounded observation lacks a needed detail. Prefer a targeted query or a small offset/limit page; do not read the whole artifact by default."
}

func (t *ArtifactReadTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"ref":    stringArgSchema("Opaque artifact ref returned by a previous tool result."),
		"offset": minIntegerArgSchema("Byte offset for paged reading. Defaults to 0.", 0),
		"limit":  rangedIntegerArgSchema("Maximum bytes to return. Defaults to 8192 and cannot exceed 16384.", 1, contextmanager.ArtifactReadMaxBytes),
		"query":  stringArgSchema("Optional literal text to search for instead of reading by offset."),
	}, "ref")
}

type artifactReadArgs struct {
	Ref    string `json:"ref"`
	Offset int64  `json:"offset"`
	Limit  int    `json:"limit"`
	Query  string `json:"query"`
}

type artifactReadResponse struct {
	Content    string `json:"content"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Complete   bool   `json:"complete"`
	Found      bool   `json:"found"`
	SHA256     string `json:"sha256"`
	MIMEType   string `json:"mime_type"`
}

func (t *ArtifactReadTool) Call(ctx context.Context, input string) (string, error) {
	var args artifactReadArgs
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return artifactReadToolError(ctx, CodeInvalidArguments, fmt.Sprintf("invalid artifact_read input: %v", err)), nil
	}
	if strings.TrimSpace(args.Ref) == "" {
		return artifactReadToolError(ctx, CodeInvalidArguments, "artifact_read requires ref"), nil
	}
	if args.Offset < 0 {
		return artifactReadToolError(ctx, CodeInvalidArguments, "artifact_read offset must be >= 0"), nil
	}
	if args.Limit < 0 || args.Limit > contextmanager.ArtifactReadMaxBytes {
		return artifactReadToolError(ctx, CodeInvalidArguments, fmt.Sprintf("artifact_read limit must be between 1 and %d", contextmanager.ArtifactReadMaxBytes)), nil
	}
	if t == nil || t.managerFn == nil {
		return artifactReadToolError(ctx, CodeToolExecutionFailed, "artifact store is unavailable"), nil
	}
	manager := t.managerFn()
	if manager == nil {
		return artifactReadToolError(ctx, CodeToolExecutionFailed, "artifact store is unavailable"), nil
	}
	var chunk contextmanager.ArtifactChunk
	var err error
	if strings.TrimSpace(args.Query) != "" {
		chunk, err = manager.SearchArtifact(args.Ref, args.Query, args.Offset, args.Limit)
	} else {
		chunk, err = manager.ReadArtifact(args.Ref, args.Offset, args.Limit)
	}
	if err != nil {
		return artifactReadToolError(ctx, CodeToolExecutionFailed, err.Error()), nil
	}
	return boundedArtifactReadResponse(chunk, args.Query), nil
}

func boundedArtifactReadResponse(chunk contextmanager.ArtifactChunk, query string) string {
	encode := func(candidate contextmanager.ArtifactChunk) []byte {
		data, _ := json.Marshal(artifactReadResponse{
			Content:    string(candidate.Content),
			Offset:     candidate.Offset,
			NextOffset: candidate.NextOffset,
			Complete:   candidate.Complete,
			Found:      candidate.Found,
			SHA256:     candidate.SHA256,
			MIMEType:   candidate.MIMEType,
		})
		return data
	}
	if data := encode(chunk); len(data) <= contextmanager.ArtifactReadMaxBytes {
		return string(data)
	}

	low, high := 0, len(chunk.Content)
	best := boundedArtifactChunk(chunk, query, 0)
	for low <= high {
		mid := low + (high-low)/2
		candidate := boundedArtifactChunk(chunk, query, mid)
		if len(encode(candidate)) <= contextmanager.ArtifactReadMaxBytes {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return string(encode(best))
}

func boundedArtifactChunk(chunk contextmanager.ArtifactChunk, query string, limit int) contextmanager.ArtifactChunk {
	if limit >= len(chunk.Content) {
		return chunk
	}
	limit = max(0, limit)
	query = strings.TrimSpace(query)
	if query == "" {
		chunk.Content = append([]byte(nil), chunk.Content[:limit]...)
		chunk.NextOffset = chunk.Offset + int64(len(chunk.Content))
		chunk.Complete = false
		return chunk
	}

	queryBytes := []byte(query)
	match := bytes.Index(chunk.Content, queryBytes)
	if match < 0 || limit < len(queryBytes) {
		chunk.Content = append([]byte(nil), chunk.Content[:limit]...)
		return chunk
	}
	start := max(0, match-(limit-len(queryBytes))/3)
	end := min(len(chunk.Content), start+limit)
	if end < match+len(queryBytes) {
		end = match + len(queryBytes)
		start = max(0, end-limit)
	}
	chunk.Offset += int64(start)
	chunk.Content = append([]byte(nil), chunk.Content[start:end]...)
	return chunk
}

func artifactReadToolError(ctx context.Context, code, message string) string {
	toolErr := NewToolError(code, message)
	SetToolError(ctx, toolErr)
	return toolErrorString(toolErr)
}
