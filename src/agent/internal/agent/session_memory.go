package agent

import (
	"strings"
)

// ChunkStructuredSummary is the typed metadata attached to a conversation chunk
// written at compaction time. It augments, but does not replace, the plain-text
// summary so older chunk indexes stay readable.
type ChunkStructuredSummary struct {
	Summary          string   `json:"summary,omitempty" yaml:"summary,omitempty"`
	UserGoals        []string `json:"user_goals,omitempty" yaml:"user_goals,omitempty"`
	ConfirmedFacts   []string `json:"confirmed_facts,omitempty" yaml:"confirmed_facts,omitempty"`
	Decisions        []string `json:"decisions,omitempty" yaml:"decisions,omitempty"`
	Proposals        []string `json:"proposals,omitempty" yaml:"proposals,omitempty"`
	OpenTasks        []string `json:"open_tasks,omitempty" yaml:"open_tasks,omitempty"`
	RisksOrPitfalls  []string `json:"risks_or_pitfalls,omitempty" yaml:"risks_or_pitfalls,omitempty"`
	MemoryCandidates []string `json:"memory_candidates,omitempty" yaml:"memory_candidates,omitempty"`
}

// Empty reports whether the structured summary contains no data.
func (s ChunkStructuredSummary) Empty() bool {
	return strings.TrimSpace(s.Summary) == "" &&
		len(s.UserGoals) == 0 &&
		len(s.ConfirmedFacts) == 0 &&
		len(s.Decisions) == 0 &&
		len(s.Proposals) == 0 &&
		len(s.OpenTasks) == 0 &&
		len(s.RisksOrPitfalls) == 0 &&
		len(s.MemoryCandidates) == 0
}

// ChunkRecallQuery specifies criteria for recalling compressed chunks.
type ChunkRecallQuery struct {
	ChunkIDs []string `json:"chunk_ids,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Entities []string `json:"entities,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

// ChunkRecallResult returns a recalled chunk with its summary.
type ChunkRecallResult struct {
	ChunkID    string                  `json:"chunk_id"`
	Source     string                  `json:"source,omitempty"`
	SessionID  string                  `json:"session_id,omitempty"`
	Summary    string                  `json:"summary"`
	Structured *ChunkStructuredSummary `json:"structured,omitempty"`
}

// chunkIndex is the on-disk `chunks/index.yaml` for one ContextManager session.
type chunkIndex struct {
	Version   int               `yaml:"version"`
	UpdatedAt string            `yaml:"updated_at"`
	Chunks    []chunkIndexEntry `yaml:"chunks"`
}

type chunkIndexEntry struct {
	ID         string                  `yaml:"id"`
	File       string                  `yaml:"file"`
	Status     string                  `yaml:"status"`
	Summary    string                  `yaml:"summary"`
	Structured *ChunkStructuredSummary `yaml:"structured,omitempty"`
	Tags       []string                `yaml:"tags,omitempty"`
	Entities   []string                `yaml:"entities,omitempty"`
	EventCount int                     `yaml:"event_count"`
	Checksum   string                  `yaml:"checksum"`
}
