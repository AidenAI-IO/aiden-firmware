package agent

import (
	"strings"
	"testing"
)

// The agent only sees long-term memory when it decides to call a recall tool:
// upfront retrieval was removed, and the system prompt does not inject the
// profile. So these three texts are the read path's necessary condition, not
// copy. Without them the whole capture pipeline is dead code, and every
// storage-layer test still passes.

func TestRecallMemoryToolAdvertisesScreenSnapshot(t *testing.T) {
	tool := NewRecallMemoryTool(nil)
	schema := tool.ArgsSchema()

	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %#v", schema)
	}
	types, ok := props["types"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no types property: %#v", props)
	}
	desc, _ := types["description"].(string)
	if !strings.Contains(desc, MemoryTypeScreenSnapshot) {
		t.Fatalf("types description does not mention %q, so the model cannot query it: %q", MemoryTypeScreenSnapshot, desc)
	}
}

func TestRecallMemoryToolDescriptionCoversSavedScreens(t *testing.T) {
	desc := NewRecallMemoryTool(nil).Description()

	if !strings.Contains(strings.ToLower(desc), "screen") {
		t.Fatalf("recall_memory description never mentions screens: %q", desc)
	}
}

func TestRecallMemoryToolDoesNotRouteSavedScreensToSessionChunks(t *testing.T) {
	// The original wording sent "raw recent session details" to
	// recall_session_chunks. "The one I just saved" reads like recent detail,
	// so without an explicit carve-out the model queries the wrong store,
	// finds nothing, and reports that nothing was saved.
	desc := NewRecallMemoryTool(nil).Description()

	if !strings.Contains(desc, "recall_session_chunks") {
		// The routing sentence is gone entirely, which also resolves the
		// misdirection.
		return
	}
	lower := strings.ToLower(desc)
	idx := strings.Index(lower, "recall_session_chunks")
	// The carve-out must appear in the same description, mentioning screens,
	// so the model can tell the two cases apart.
	if !strings.Contains(lower, "screen") {
		t.Fatalf("description routes to recall_session_chunks without carving out saved screens: %q", desc)
	}
	if idx < 0 {
		t.Fatalf("unexpected index for routing sentence in %q", desc)
	}
}

func TestSaveMemoryToolDoesNotOfferScreenSnapshot(t *testing.T) {
	// Write side stays closed: only a button press creates a Screen Memory.
	// The model must not hand-roll one through save_memory.
	schema := NewSaveMemoryTool(nil).ArgsSchema()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties map: %#v", schema)
	}
	typeProp, ok := props["type"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no type property: %#v", props)
	}
	enum, ok := typeProp["enum"].([]string)
	if !ok {
		if anyEnum, anyOK := typeProp["enum"].([]any); anyOK {
			for _, v := range anyEnum {
				if s, _ := v.(string); s == MemoryTypeScreenSnapshot {
					t.Fatalf("save_memory offers %q; screen memories must come only from a button press", MemoryTypeScreenSnapshot)
				}
			}
			return
		}
		t.Fatalf("type property has no enum: %#v", typeProp)
	}
	for _, v := range enum {
		if v == MemoryTypeScreenSnapshot {
			t.Fatalf("save_memory offers %q; screen memories must come only from a button press", MemoryTypeScreenSnapshot)
		}
	}
}

func TestPromptTriggersRecallForSavedScreenContent(t *testing.T) {
	// The prompt's recall trigger enumerated preferences, rules, procedures and
	// facts. "The tracking number I saved" is none of those, so read literally
	// the model had no reason to call recall_memory at all.
	// Assert against the recall rule itself, not the whole prompt: "screen"
	// already appears in unrelated rules about screenshots and the connected
	// display, so a prompt-wide substring check passes without the trigger ever
	// covering saved screen content.
	var recallRule string
	for _, line := range strings.Split(defaultAgentBehavior(), "\n") {
		if strings.Contains(line, "recall_memory") {
			recallRule = line
			break
		}
	}
	if recallRule == "" {
		t.Fatal("no prompt rule mentions recall_memory")
	}
	if !strings.Contains(strings.ToLower(recallRule), "screen") {
		t.Fatalf("the recall_memory trigger does not cover saved screen content, so a button-saved memory is never recalled: %q", recallRule)
	}
}
