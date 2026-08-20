package configdoc

import (
	"bytes"
	"strings"
	"testing"
)

func TestApplyEmptyPatchPreservesDocumentByteForByte(t *testing.T) {
	source := []byte("# factory config\n[hid]\nkeyboard_layout = \"qwerty\" # keep\n")

	got, changed, err := Apply(source, nil)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %v, want empty", changed)
	}
	if !bytes.Equal(got, source) {
		t.Fatalf("document changed:\n%s", got)
	}
}

func TestApplyReplacesOnlyExistingValueToken(t *testing.T) {
	source := []byte("# factory config\n[hid]\nkeyboard_layout   =   \"qwerty\"   # keep this\nunknown = [1, 2, 3]\n")
	want := []byte("# factory config\n[hid]\nkeyboard_layout   =   \"azerty\"   # keep this\nunknown = [1, 2, 3]\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"hid", "keyboard_layout"}, Value: "azerty"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "hid.keyboard_layout" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyUnderstandsQuotedAndDottedKeys(t *testing.T) {
	source := []byte("hid.keyboard_layout = \"qwerty\" # dotted\n[model_providers.\"open.router\"]\napi_key = \"secret\"\nbase_url = \"https://old.example\" # keep\n")
	want := []byte("hid.keyboard_layout = \"azerty\" # dotted\n[model_providers.\"open.router\"]\napi_key = \"secret\"\nbase_url = \"https://new.example\" # keep\n")

	got, _, err := Apply(source, []Operation{
		{Path: []string{"hid", "keyboard_layout"}, Value: "azerty"},
		{Path: []string{"model_providers", "open.router", "base_url"}, Value: "https://new.example"},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyPreservesMultilineValuesAndAdjacentComments(t *testing.T) {
	source := []byte("[model]\nmodel = \"old\"\nresponses = [\"text\", \"audio\"]\nprompt = \"\"\"first\nsecond\"\"\" # untouched\n# belongs to unknown\nfuture = { enabled = true }\n")
	want := []byte("[model]\nmodel = \"new\"\nresponses = [\"text\", \"audio\"]\nprompt = \"\"\"first\nsecond\"\"\" # untouched\n# belongs to unknown\nfuture = { enabled = true }\n")

	got, _, err := Apply(source, []Operation{{Path: []string{"model", "model"}, Value: "new"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeletesOnlyTargetKeyLine(t *testing.T) {
	source := []byte("[model]\n# keep before\nreasoning_effort = \"high\" # remove with key\n# keep after\nmodel = \"gpt\"\n")
	want := []byte("[model]\n# keep before\n# keep after\nmodel = \"gpt\"\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"model", "reasoning_effort"}, Delete: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model.reasoning_effort" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyInsertsIntoOnlyTargetTable(t *testing.T) {
	source := []byte("[model]\nmodel = \"gpt\"\n\n[storage]\nunknown = true\n")
	want := []byte("[model]\nmodel = \"gpt\"\nreasoning_effort = \"high\"\n\n[storage]\nunknown = true\n")

	got, _, err := Apply(source, []Operation{{Path: []string{"model", "reasoning_effort"}, Value: "high"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyInsertsIntoExistingInlineTable(t *testing.T) {
	source := []byte("hid = { keyboard_layout = \"qwerty\" } # keep this\n")
	want := []byte("hid = { keyboard_layout = \"qwerty\", keyboard_device = \"/dev/hidg9\" } # keep this\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"hid", "keyboard_device"}, Value: "/dev/hidg9"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "hid.keyboard_device" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyCreatesNestedRecordInsideInlineTable(t *testing.T) {
	source := []byte("model_providers = { old = { type = \"openai\" } } # keep this\n")
	want := []byte("model_providers = { old = { type = \"openai\" }, new = { type = \"ollama\" } } # keep this\n")

	got, changed, err := Apply(source, []Operation{{
		Path:  []string{"model_providers", "new", "type"},
		Value: "ollama",
	}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.new.type" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyAddsMultipleFieldsToNewInlineTableRecord(t *testing.T) {
	source := []byte("model_providers = { old = { type = \"openai\" } }\n")
	got, changed, err := Apply(source, []Operation{
		{Path: []string{"model_providers", "new", "type"}, Value: "openai"},
		{Path: []string{"model_providers", "new", "base_url"}, Value: "https://example.test"},
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("changed = %v", changed)
	}
	text := string(got)
	for _, want := range []string{
		`new = { base_url = "https://example.test", type = "openai" }`,
		`old = { type = "openai" }`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("inline provider missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[model_providers.new]") {
		t.Fatalf("new provider escaped the inline table:\n%s", text)
	}
}

func TestApplyReplacesExistingInlineTableValue(t *testing.T) {
	source := []byte("hid = { keyboard_layout = \"qwerty\", keyboard_device = \"/dev/hidg0\" }\n")
	want := []byte("hid = { keyboard_layout = \"qwerty\", keyboard_device = \"/dev/hidg9\" }\n")

	got, _, err := Apply(source, []Operation{{Path: []string{"hid", "keyboard_device"}, Value: "/dev/hidg9"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeletesExistingInlineTableValue(t *testing.T) {
	source := []byte("hid = { keyboard_layout = \"qwerty\", keyboard_device = \"/dev/hidg0\" } # keep\n")
	want := []byte("hid = { keyboard_layout = \"qwerty\" } # keep\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"hid", "keyboard_device"}, Delete: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "hid.keyboard_device" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeletesInlineProviderRecord(t *testing.T) {
	source := []byte("model_providers = { old = { type = \"openai\" }, keep = { type = \"ollama\" } }\n")
	want := []byte("model_providers = { keep = { type = \"ollama\" } }\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"model_providers", "old"}, DeleteTable: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeletesDottedKeyProviderRecord(t *testing.T) {
	source := []byte("model_providers.old.type = \"openai\"\nmodel_providers.old.api_key = \"secret\" # remove\nmodel_providers.keep.type = \"ollama\"\n")
	want := []byte("model_providers.keep.type = \"ollama\"\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"model_providers", "old"}, DeleteTable: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyInsertsAlongsideDottedKeys(t *testing.T) {
	source := []byte("model_providers.foo.type = \"openai\" # keep\nmodel_providers.keep.type = \"ollama\"\n")
	want := []byte("model_providers.foo.type = \"openai\" # keep\nmodel_providers.foo.base_url = \"https://example.test\"\nmodel_providers.keep.type = \"ollama\"\n")

	got, changed, err := Apply(source, []Operation{{
		Path:  []string{"model_providers", "foo", "base_url"},
		Value: "https://example.test",
	}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.foo.base_url" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyHandlesDottedProviderUnderExplicitParentTable(t *testing.T) {
	source := []byte("[model_providers]\nold.type = \"openai\"\nold.api_key = \"secret\"\nkeep.type = \"ollama\"\n")
	want := []byte("[model_providers]\nkeep.type = \"ollama\"\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"model_providers", "old"}, DeleteTable: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyAppendsMinimalNewTable(t *testing.T) {
	source := []byte("locale = \"en-US\"\n")
	want := []byte("locale = \"en-US\"\n\n[hid]\nkeyboard_layout = \"azerty\"\n")

	got, _, err := Apply(source, []Operation{{Path: []string{"hid", "keyboard_layout"}, Value: "azerty"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyInsertsMissingTopLevelKeyBeforeFirstTable(t *testing.T) {
	source := []byte("# existing config\nfuture = true\n\n[device]\nbackend = \"hdmi\"\n")
	want := []byte("# existing config\nfuture = true\n\nlocale = \"en-US\"\n[device]\nbackend = \"hdmi\"\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"locale"}, Value: "en-US"}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "locale" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeletesProviderRecordWithoutTouchingOthers(t *testing.T) {
	source := []byte("[tts_providers.old]\ntype = \"fish-audio\"\nunknown = true\n\n# keep next provider\n[tts_providers.keep]\ntype = \"minimax\"\n")
	want := []byte("# keep next provider\n[tts_providers.keep]\ntype = \"minimax\"\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"tts_providers", "old"}, DeleteTable: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "tts_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeleteTableRemovesLeadingCommentOwnedByTable(t *testing.T) {
	source := []byte("[model]\nprovider = \"keep\"\n\n# work account, remove with provider\n[model_providers.old]\ntype = \"openai\"\n\n[model_providers.keep]\ntype = \"openai\"\n")
	want := []byte("[model]\nprovider = \"keep\"\n\n[model_providers.keep]\ntype = \"openai\"\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"model_providers", "old"}, DeleteTable: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeleteLastTableRemovesTrailingComments(t *testing.T) {
	source := []byte("[model_providers.keep]\ntype = \"openai\"\n\n[model_providers.old]\ntype = \"openai\"\n# remove with old provider\n")
	want := []byte("[model_providers.keep]\ntype = \"openai\"\n\n")

	got, changed, err := Apply(source, []Operation{{Path: []string{"model_providers", "old"}, DeleteTable: true}})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestApplyDeleteTablePreservesInterleavedUnrelatedTables(t *testing.T) {
	source := []byte(`[model_providers.old]
type = "openai"

[telemetry]
enabled = true

[model_providers.old.options]
future = "remove with provider"

[model]
provider = "new"
`)
	updated, changed, err := Apply(source, []Operation{{
		Path:        []string{"model_providers", "old"},
		DeleteTable: true,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || changed[0] != "model_providers.old" {
		t.Fatalf("changed = %v", changed)
	}
	text := string(updated)
	if strings.Contains(text, "[model_providers.old]") || strings.Contains(text, "[model_providers.old.options]") {
		t.Fatalf("provider table was not fully deleted:\n%s", text)
	}
	for _, want := range []string{"[telemetry]", "enabled = true", "[model]", `provider = "new"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("unrelated content %q was deleted:\n%s", want, text)
		}
	}
}

func TestApplyRejectsInvalidTOMLWithoutReturningPartialOutput(t *testing.T) {
	source := []byte("[hid\nkeyboard_layout = \"qwerty\"\n")
	got, _, err := Apply(source, []Operation{{Path: []string{"hid", "keyboard_layout"}, Value: "azerty"}})
	if err == nil {
		t.Fatal("Apply() error = nil")
	}
	if got != nil {
		t.Fatalf("Apply() output = %q, want nil", got)
	}
}
