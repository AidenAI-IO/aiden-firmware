package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	modelpkg "aiden-agent/internal/agent/model"

	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/llms"
)

type reflectionQualityCases struct {
	Cases []reflectionQualityCase `json:"cases"`
	Pairs []reflectionQualityPair `json:"pairs"`
}

type reflectionQualityCase struct {
	Name     string                    `json:"name"`
	Expected reflectionQualityExpected `json:"expected"`
	Episode  TaskEpisode               `json:"episode"`
}

type reflectionQualityExpected struct {
	Action       string   `json:"action"`
	Group        string   `json:"group"`
	FilterReason string   `json:"filter_reason"`
	EvidenceRefs []string `json:"evidence_refs"`
	MustMention  []string `json:"must_mention"`
	MustAvoid    []string `json:"must_avoid"`
}

type reflectionQualityPair struct {
	Left           string `json:"left"`
	Right          string `json:"right"`
	ExpectedAction string `json:"expected_action"`
}

type reflectionEvalModel struct {
	inner llms.Model
}

func (m reflectionEvalModel) Call(ctx context.Context, prompt string, options ...llms.CallOption) (string, error) {
	return m.inner.Call(ctx, prompt, options...)
}

func (m reflectionEvalModel) GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	return m.inner.GenerateContent(ctx, messages, options...)
}

func (reflectionEvalModel) CallOptions() []chains.ChainCallOption { return nil }

func (reflectionEvalModel) Spec() modelpkg.ModelSpec { return modelpkg.ModelSpec{} }

func TestReflectionQualityCasesAreValid(t *testing.T) {
	fixtures := loadReflectionQualityCases(t)

	if len(fixtures.Cases) < 8 {
		t.Fatalf("quality case count = %d, want at least 8", len(fixtures.Cases))
	}

	caseNames := map[string]bool{}
	episodeIDs := map[string]bool{}
	for _, fixture := range fixtures.Cases {
		if fixture.Name == "" || caseNames[fixture.Name] {
			t.Fatalf("duplicate or empty case name %q", fixture.Name)
		}
		caseNames[fixture.Name] = true
		if fixture.Episode.ID == "" || episodeIDs[fixture.Episode.ID] {
			t.Fatalf("duplicate or empty Episode ID %q", fixture.Episode.ID)
		}
		episodeIDs[fixture.Episode.ID] = true
		if fixture.Episode.Outcome.Success {
			t.Fatalf("case %s is not a failed Episode", fixture.Name)
		}
		if fixture.Expected.Action != reflectionActionKeep && fixture.Expected.Action != reflectionActionIgnore {
			t.Fatalf("case %s action = %q", fixture.Name, fixture.Expected.Action)
		}

		reason := invalidReflectionEpisodeReason(fixture.Episode)
		if reason != fixture.Expected.FilterReason {
			t.Fatalf("case %s filter reason = %q, want %q", fixture.Name, reason, fixture.Expected.FilterReason)
		}
		validRefs := validReflectionEventIDs(fixture.Episode, fixture.Expected.EvidenceRefs)
		if !slices.Equal(validRefs, fixture.Expected.EvidenceRefs) {
			t.Fatalf("case %s evidence refs = %#v, want valid %#v", fixture.Name, validRefs, fixture.Expected.EvidenceRefs)
		}
	}

	for _, pair := range fixtures.Pairs {
		if !caseNames[pair.Left] || !caseNames[pair.Right] {
			t.Fatalf("pair references unknown cases: %#v", pair)
		}
		if pair.ExpectedAction != reflectionActionMerge && pair.ExpectedAction != reflectionActionCreate {
			t.Fatalf("pair action = %q", pair.ExpectedAction)
		}
	}
}

// TestReflectionQualityCasesAgainstLiveModel executes the quality corpus against
// the real reflection prompts. It is opt-in because it requires a provider key:
//
//	OPENROUTER_API_KEY=... go test ./internal/agent -run TestReflectionQualityCasesAgainstLiveModel -v
func TestReflectionQualityCasesAgainstLiveModel(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY"))
	if apiKey == "" {
		t.Skip("OPENROUTER_API_KEY not set; skipping live reflection quality evaluation")
	}
	modelName := strings.TrimSpace(os.Getenv("REFLECTION_EVAL_MODEL"))
	if modelName == "" {
		modelName = "anthropic/claude-haiku-4.5"
	}
	models := reflectionEvalModel{inner: newOpenAICompatibleModel("https://openrouter.ai/api/v1", modelName, apiKey, http.DefaultClient)}
	plane := NewFilesystemMemoryPlane(t.TempDir(), DefaultMemoryExtractionConfig(), nil)
	processor := newFailureReflectionProcessor(plane, models)
	fixtures := loadReflectionQualityCases(t)
	summaries := make(map[string]failureSummary, len(fixtures.Cases))

	for _, fixture := range fixtures.Cases {
		fixture := fixture
		t.Run("summary/"+fixture.Name, func(t *testing.T) {
			if reason := invalidReflectionEpisodeReason(fixture.Episode); reason != "" {
				if fixture.Expected.Action != reflectionActionIgnore {
					t.Fatalf("deterministic filter action = ignore, want %s", fixture.Expected.Action)
				}
				return
			}
			summary, err := processor.summarizeFailure(context.Background(), fixture.Episode)
			if err != nil {
				t.Fatalf("summarizeFailure() error = %v", err)
			}
			if summary.Action != fixture.Expected.Action {
				t.Fatalf("summary action = %q, want %q: %#v", summary.Action, fixture.Expected.Action, summary)
			}
			if summary.Action == reflectionActionIgnore {
				return
			}
			summaries[fixture.Name] = summary
			combined := strings.ToLower(strings.Join([]string{summary.Pattern, summary.Cause, summary.MissedSignal, summary.Guard, summary.Scope}, " "))
			for _, expectation := range fixture.Expected.MustMention {
				if !containsQualityAlternative(combined, expectation) {
					t.Errorf("summary %q does not mention %q: %#v", combined, expectation, summary)
				}
			}
			for _, expectation := range fixture.Expected.MustAvoid {
				if containsQualityAlternative(combined, expectation) {
					t.Errorf("summary %q unexpectedly mentions %q: %#v", combined, expectation, summary)
				}
			}
			for _, eventID := range fixture.Expected.EvidenceRefs {
				if !slices.Contains(summary.EvidenceRefs, eventID) {
					t.Errorf("summary evidence_refs = %#v, want %q", summary.EvidenceRefs, eventID)
				}
			}
		})
	}

	for _, pair := range fixtures.Pairs {
		pair := pair
		t.Run("pair/"+pair.Left+"/"+pair.Right, func(t *testing.T) {
			left, leftOK := summaries[pair.Left]
			right, rightOK := summaries[pair.Right]
			if !leftOK || !rightOK {
				t.Fatalf("missing keep summaries for pair: left=%v right=%v", leftOK, rightOK)
			}
			candidate := DeviceMemoryItem{
				ID:      "candidate_" + pair.Left,
				Type:    "failure",
				Status:  "pending",
				Title:   left.Pattern,
				Summary: left.Guard,
				Content: renderFailureMemoryContent(left),
				Tags:    append([]string{reflectionFailureTag}, left.Tags...),
			}
			decision, err := processor.decideFailureMerge(context.Background(), right, []DeviceMemoryItem{candidate})
			if err != nil {
				t.Fatalf("decideFailureMerge() error = %v", err)
			}
			if decision.Action != pair.ExpectedAction {
				t.Fatalf("pair decision = %#v, want action %q", decision, pair.ExpectedAction)
			}
		})
	}
}

func loadReflectionQualityCases(t *testing.T) reflectionQualityCases {
	t.Helper()
	data, err := os.ReadFile("testdata/reflection_quality_cases.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var fixtures reflectionQualityCases
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	return fixtures
}

func containsQualityAlternative(text, expectation string) bool {
	for _, alternative := range strings.Split(strings.ToLower(expectation), "|") {
		if alternative = strings.TrimSpace(alternative); alternative != "" && strings.Contains(text, alternative) {
			return true
		}
	}
	return false
}
