package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	planArtifactKindTargetText       = "target_text"
	planArtifactKindClipboardPayload = "clipboard_payload"
	planArtifactDeliveryClipboard    = "clipboard"
	planSourceToolContacts           = "contacts"
	planSourceActionQuery            = "query"

	planErrTargetTextMissingTargetOpenStep = "target_text_missing_target_open_step"
	planErrTargetTextMissingConsumeStep    = "target_text_missing_consume_step"
	planErrArtifactPreparedAfterTargetOpen = "artifact_prepare_not_before_target_open"
	planErrSourceAfterTargetOpen           = "source_not_before_target_open"
	planErrSourceRefUndeclared             = "source_ref_undeclared"
	planErrSourceUnlinked                  = "source_not_linked_to_artifact"
	planErrTargetTextPlaceholderOnly       = "target_text_placeholder_only"

	artifactAppLabelTokenSeparators = "-_/|,;:()[]"
)

var templatePlaceholderRE = regexp.MustCompile(`\{\{\s*[^{}]+\s*\}\}`)

type planArtifact struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Delivery       string   `json:"delivery,omitempty"`
	PrepareStep    int      `json:"prepare_step,omitempty"`
	TargetOpenStep int      `json:"target_open_step,omitempty"`
	ConsumeStep    int      `json:"consume_step,omitempty"`
	TextTemplate   string   `json:"text_template,omitempty"`
	SourceRefs     []string `json:"source_refs,omitempty"`
	TargetApp      string   `json:"target_app,omitempty"`
	TargetLabel    string   `json:"target_label,omitempty"`
}

type planSource struct {
	ID         string   `json:"id"`
	Tool       string   `json:"tool"`
	Action     string   `json:"action,omitempty"`
	Step       int      `json:"step,omitempty"`
	Query      string   `json:"query,omitempty"`
	Produces   []string `json:"produces,omitempty"`
	ArtifactID string   `json:"artifact_id,omitempty"`
}

type planArtifactState struct {
	planArtifact
	PreparedText string
	PreparedAt   time.Time
	ConsumedAt   time.Time
}

type planSourceState struct {
	planSource
}

type planValidationError struct {
	Code    string
	Message string
	Hint    map[string]any
}

func (e *planValidationError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func newPlanValidationError(code, message string, hint map[string]any) error {
	return &planValidationError{
		Code:    code,
		Message: message,
		Hint:    hint,
	}
}

func parsePlanArtifacts(raw json.RawMessage) ([]planArtifact, error) {
	artifacts, _, err := parsePlanArtifactsAndMisplacedSources(raw)
	return artifacts, err
}

func parsePlanArtifactsAndMisplacedSources(raw json.RawMessage) ([]planArtifact, []planSource, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		raw = json.RawMessage(strings.TrimSpace(encoded))
		if len(raw) == 0 {
			return nil, nil, nil
		}
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, nil, fmt.Errorf("artifacts must be an array of objects: %w", err)
	}
	artifacts := make([]planArtifact, 0, len(items))
	var misplacedSources []planSource
	for _, item := range items {
		if planArtifactItemLooksLikeSource(item) {
			var source planSource
			if err := decodePlanJSONStrict(item, &source); err != nil {
				return nil, nil, fmt.Errorf("artifacts contains a source-shaped object that is not a valid source: %w", err)
			}
			misplacedSources = append(misplacedSources, source)
			continue
		}
		var artifact planArtifact
		if err := decodePlanJSONStrict(item, &artifact); err != nil {
			return nil, nil, fmt.Errorf("artifacts must be an array of objects: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	return normalizePlanArtifacts(artifacts), normalizePlanSources(misplacedSources), nil
}

func normalizePlanArtifacts(artifacts []planArtifact) []planArtifact {
	out := make([]planArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		artifact.ID = strings.TrimSpace(artifact.ID)
		artifact.Kind = normalizePlanArtifactKind(artifact.Kind)
		artifact.Delivery = normalizePlanArtifactDelivery(artifact.Delivery)
		artifact.TextTemplate = strings.TrimSpace(artifact.TextTemplate)
		artifact.SourceRefs = uniqueNonEmpty(artifact.SourceRefs)
		artifact.TargetApp = strings.TrimSpace(artifact.TargetApp)
		artifact.TargetLabel = strings.TrimSpace(artifact.TargetLabel)
		out = append(out, artifact)
	}
	return out
}

func planArtifactItemLooksLikeSource(raw json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	if _, hasKind := fields["kind"]; hasKind {
		return false
	}
	_, hasTool := fields["tool"]
	_, hasAction := fields["action"]
	return hasTool || hasAction
}

func normalizePlanArtifactKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	kind = strings.ReplaceAll(kind, "-", "_")
	kind = strings.ReplaceAll(kind, " ", "_")
	switch kind {
	case "text", "message", "message_text", "target_text", "app_text":
		return planArtifactKindTargetText
	case "clipboard_payload", "clipboard_text", "clipboard":
		return planArtifactKindClipboardPayload
	default:
		return kind
	}
}

func normalizePlanArtifactDelivery(delivery string) string {
	delivery = strings.ToLower(strings.TrimSpace(delivery))
	delivery = strings.ReplaceAll(delivery, "-", "_")
	delivery = strings.ReplaceAll(delivery, " ", "_")
	switch delivery {
	case "", "clipboard", "paste":
		return planArtifactDeliveryClipboard
	default:
		return delivery
	}
}

func parsePlanSources(raw json.RawMessage) ([]planSource, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var encoded string
	if err := json.Unmarshal(raw, &encoded); err == nil {
		raw = json.RawMessage(strings.TrimSpace(encoded))
		if len(raw) == 0 {
			return nil, nil
		}
	}
	var sources []planSource
	if err := decodePlanJSONStrict(raw, &sources); err != nil {
		return nil, fmt.Errorf("sources must be an array of objects: %w", err)
	}
	return normalizePlanSources(sources), nil
}

func decodePlanJSONStrict(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("must contain a single JSON value")
	}
	return nil
}

func normalizePlanSources(sources []planSource) []planSource {
	out := make([]planSource, 0, len(sources))
	for _, source := range sources {
		source.ID = strings.TrimSpace(source.ID)
		source.Tool = normalizePlanSourceTool(source.Tool)
		source.Action = normalizePlanSourceAction(source.Action)
		source.Query = strings.TrimSpace(source.Query)
		source.Produces = normalizePlanSourceProduces(source.Produces)
		source.ArtifactID = strings.TrimSpace(source.ArtifactID)
		out = append(out, source)
	}
	return out
}

func normalizePlanSourceTool(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	tool = strings.ReplaceAll(tool, "-", "_")
	tool = strings.ReplaceAll(tool, " ", "_")
	switch tool {
	case "contact", "contacts":
		return planSourceToolContacts
	default:
		return tool
	}
}

func normalizePlanSourceAction(action string) string {
	action = strings.ToLower(strings.TrimSpace(action))
	action = strings.ReplaceAll(action, "-", "_")
	action = strings.ReplaceAll(action, " ", "_")
	switch action {
	case "", "query", "search", "lookup":
		return planSourceActionQuery
	default:
		return action
	}
}

func normalizePlanSourceProduces(fields []string) []string {
	out := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		field = strings.ReplaceAll(field, "-", "_")
		field = strings.ReplaceAll(field, " ", "_")
		switch field {
		case "phone_number":
			field = "phone_numbers"
		case "contact_name", "name", "names":
			field = "contact_names"
		case "email":
			field = "emails"
		}
		if field == "" || seen[field] {
			continue
		}
		seen[field] = true
		out = append(out, field)
	}
	return out
}

func initialPlanArtifactStates(artifacts []planArtifact) []planArtifactState {
	artifacts = normalizePlanArtifacts(artifacts)
	out := make([]planArtifactState, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, planArtifactState{planArtifact: artifact})
	}
	return out
}

func initialPlanSourceStates(sources []planSource) []planSourceState {
	sources = normalizePlanSources(sources)
	out := make([]planSourceState, 0, len(sources))
	for _, source := range sources {
		out = append(out, planSourceState{planSource: source})
	}
	return out
}

func normalizePhoneWorkflowContracts(decision plannerDecision) plannerDecision {
	decision.Sources = normalizePlanSources(decision.Sources)
	decision.Artifacts = normalizePlanArtifacts(decision.Artifacts)
	decision.Artifacts = normalizeGenericSourceRefs(decision.Artifacts, decision.Sources)

	contactIndexes := contactsPlanSourceIndexes(decision.Sources)
	targetIndexes := targetTextPlanArtifactIndexes(decision.Artifacts)
	if len(contactIndexes) == 1 && len(targetIndexes) == 1 {
		source := &decision.Sources[contactIndexes[0]]
		artifact := &decision.Artifacts[targetIndexes[0]]
		linkSourceToTargetTextArtifact(source, artifact)
		if source.Step > 0 && artifact.PrepareStep > 0 && source.Step > artifact.PrepareStep {
			artifact.PrepareStep = source.Step
		}
		if artifact.ConsumeStep <= 0 {
			artifact.ConsumeStep = inferTargetTextConsumeStep(*artifact, len(decision.Plan))
		}
	}

	for i := range decision.Sources {
		if decision.Sources[i].ArtifactID == "" {
			continue
		}
		artifact := findPlanArtifactByID(decision.Artifacts, decision.Sources[i].ArtifactID)
		if artifact == nil || artifact.Kind != planArtifactKindTargetText {
			continue
		}
		linkSourceToTargetTextArtifact(&decision.Sources[i], artifact)
		if artifact.ConsumeStep <= 0 {
			artifact.ConsumeStep = inferTargetTextConsumeStep(*artifact, len(decision.Plan))
		}
	}

	return decision
}

func contactsPlanSourceIndexes(sources []planSource) []int {
	var out []int
	for i, source := range sources {
		if source.Tool == planSourceToolContacts && source.Action == planSourceActionQuery {
			out = append(out, i)
		}
	}
	return out
}

func targetTextPlanArtifactIndexes(artifacts []planArtifact) []int {
	var out []int
	for i, artifact := range artifacts {
		if artifact.Kind == planArtifactKindTargetText {
			out = append(out, i)
		}
	}
	return out
}

func normalizeGenericSourceRefs(artifacts []planArtifact, sources []planSource) []planArtifact {
	if len(sources) != 1 {
		return artifacts
	}
	source := sources[0]
	if source.ID == "" {
		return artifacts
	}
	for i := range artifacts {
		for j, ref := range artifacts[i].SourceRefs {
			if sourceRefSourceID(ref) == "source" {
				artifacts[i].SourceRefs[j] = defaultPlanSourceRef(source)
			}
		}
		artifacts[i].SourceRefs = uniqueNonEmpty(artifacts[i].SourceRefs)
	}
	return artifacts
}

func findPlanArtifactByID(artifacts []planArtifact, id string) *planArtifact {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range artifacts {
		if artifacts[i].ID == id {
			return &artifacts[i]
		}
	}
	return nil
}

func linkSourceToTargetTextArtifact(source *planSource, artifact *planArtifact) {
	if source == nil || artifact == nil || source.ID == "" || artifact.ID == "" {
		return
	}
	if source.ArtifactID == "" {
		source.ArtifactID = artifact.ID
	}
	if !artifactReferencesSource(*artifact, source.ID) {
		artifact.SourceRefs = uniqueNonEmpty(append(artifact.SourceRefs, defaultPlanSourceRef(*source)))
	}
}

func artifactReferencesSource(artifact planArtifact, sourceID string) bool {
	for _, ref := range artifact.SourceRefs {
		if sourceRefReferencesSource(ref, sourceID) {
			return true
		}
	}
	return false
}

func defaultPlanSourceRef(source planSource) string {
	switch source.Tool {
	case planSourceToolContacts:
		return source.ID + ".phone_numbers"
	default:
		return source.ID
	}
}

func inferTargetTextConsumeStep(artifact planArtifact, planLen int) int {
	consume := artifact.ConsumeStep
	if consume <= 0 {
		consume = artifact.PrepareStep + 1
	}
	if artifact.TargetOpenStep > consume {
		consume = artifact.TargetOpenStep
	}
	if consume <= artifact.PrepareStep {
		consume = artifact.PrepareStep + 1
	}
	if planLen > 0 && consume > planLen && planLen > artifact.PrepareStep {
		consume = planLen
	}
	return consume
}

func validatePlanArtifacts(artifacts []planArtifact, planLen int) error {
	seen := map[string]bool{}
	for _, artifact := range normalizePlanArtifacts(artifacts) {
		if artifact.ID == "" {
			return fmt.Errorf("artifact requires id")
		}
		if seen[artifact.ID] {
			return fmt.Errorf("duplicate artifact id %q", artifact.ID)
		}
		seen[artifact.ID] = true
		if artifact.Kind == "" {
			return fmt.Errorf("artifact %q requires kind", artifact.ID)
		}
		if artifact.Kind == planArtifactKindTargetText {
			if artifact.Delivery != planArtifactDeliveryClipboard {
				return fmt.Errorf("target_text artifact %q must use delivery=clipboard", artifact.ID)
			}
			if artifact.PrepareStep <= 0 {
				return fmt.Errorf("target_text artifact %q requires prepare_step", artifact.ID)
			}
			if artifact.ConsumeStep <= 0 {
				return newPlanValidationError(
					planErrTargetTextMissingConsumeStep,
					fmt.Sprintf("target_text artifact %q requires consume_step", artifact.ID),
					targetTextArtifactContractHint(),
				)
			}
			if strings.TrimSpace(artifact.TextTemplate) == "" {
				return fmt.Errorf("target_text artifact %q requires text_template", artifact.ID)
			}
			if strings.TrimSpace(artifact.TargetApp) == "" {
				return fmt.Errorf("target_text artifact %q requires target_app", artifact.ID)
			}
		} else if artifact.Kind == planArtifactKindClipboardPayload {
			if artifact.Delivery != planArtifactDeliveryClipboard {
				return fmt.Errorf("clipboard_payload artifact %q must use delivery=clipboard", artifact.ID)
			}
			if artifact.PrepareStep <= 0 {
				return fmt.Errorf("clipboard_payload artifact %q requires prepare_step", artifact.ID)
			}
		} else {
			return fmt.Errorf("artifact %q has unsupported kind %q", artifact.ID, artifact.Kind)
		}
		if artifact.PrepareStep < 0 || artifact.ConsumeStep < 0 {
			return fmt.Errorf("artifact %q step indexes must be positive", artifact.ID)
		}
		if planLen > 0 {
			if artifact.PrepareStep > planLen {
				return fmt.Errorf("artifact %q prepare_step %d exceeds plan length %d", artifact.ID, artifact.PrepareStep, planLen)
			}
			if artifact.TargetOpenStep > planLen {
				return fmt.Errorf("artifact %q target_open_step %d exceeds plan length %d", artifact.ID, artifact.TargetOpenStep, planLen)
			}
			if artifact.ConsumeStep > planLen {
				return fmt.Errorf("artifact %q consume_step %d exceeds plan length %d", artifact.ID, artifact.ConsumeStep, planLen)
			}
		}
	}
	return nil
}

func validatePlanSources(sources []planSource, artifacts []planArtifact, planLen int) error {
	sources = normalizePlanSources(sources)
	artifacts = normalizePlanArtifacts(artifacts)
	sourceIDs := map[string]bool{}
	artifactIDs := map[string]bool{}
	for _, artifact := range artifacts {
		artifactIDs[artifact.ID] = true
	}
	for _, source := range sources {
		if source.ID == "" {
			return fmt.Errorf("source requires id")
		}
		if sourceIDs[source.ID] {
			return fmt.Errorf("duplicate source id %q", source.ID)
		}
		sourceIDs[source.ID] = true
		if source.Tool == "" {
			return fmt.Errorf("source %q requires tool", source.ID)
		}
		if source.Step <= 0 {
			return fmt.Errorf("source %q requires step", source.ID)
		}
		if planLen > 0 && source.Step > planLen {
			return fmt.Errorf("source %q step %d exceeds plan length %d", source.ID, source.Step, planLen)
		}
		switch source.Tool {
		case planSourceToolContacts:
			if source.Action != planSourceActionQuery {
				return fmt.Errorf("source %q contacts tool must use action=query", source.ID)
			}
			for _, field := range source.Produces {
				switch field {
				case "phone_numbers", "contact_names", "emails":
				default:
					return fmt.Errorf("source %q contacts produces unsupported field %q", source.ID, field)
				}
			}
		default:
			return fmt.Errorf("source %q has unsupported tool %q", source.ID, source.Tool)
		}
		if source.ArtifactID != "" && !artifactIDs[source.ArtifactID] {
			return fmt.Errorf("source %q references unknown artifact_id %q", source.ID, source.ArtifactID)
		}
		for _, artifact := range artifacts {
			if !planSourceLinksArtifact(source, artifact) {
				continue
			}
			if source.Step > artifact.PrepareStep {
				return fmt.Errorf("source %q step %d must be no later than artifact %q prepare_step %d", source.ID, source.Step, artifact.ID, artifact.PrepareStep)
			}
		}
	}
	for _, artifact := range artifacts {
		for _, ref := range artifact.SourceRefs {
			sourceID := sourceRefSourceID(ref)
			if sourceID == "" {
				return fmt.Errorf("artifact %q has invalid source_ref %q", artifact.ID, ref)
			}
			if !sourceIDs[sourceID] {
				return newPlanValidationError(
					planErrSourceRefUndeclared,
					fmt.Sprintf("artifact %q source_ref %q references undeclared source %q", artifact.ID, ref, sourceID),
					targetTextArtifactContractHint(),
				)
			}
		}
	}
	return nil
}

func validateCommittedPlanArtifactContracts(decision plannerDecision) error {
	for _, artifact := range normalizePlanArtifacts(decision.Artifacts) {
		if artifact.Kind != planArtifactKindTargetText {
			continue
		}
		if artifact.TargetOpenStep > 0 && artifact.TargetOpenStep > artifact.ConsumeStep {
			return newPlanValidationError(
				planErrArtifactPreparedAfterTargetOpen,
				fmt.Sprintf("artifact contract violation: target_text artifact %q target_open_step must be no later than consume_step", artifact.ID),
				targetTextArtifactContractHint(),
			)
		}
		if artifact.ConsumeStep <= artifact.PrepareStep {
			return fmt.Errorf("artifact contract violation: target_text artifact %q must consume in a later step than it is prepared", artifact.ID)
		}
		if templateHasPlaceholder(artifact.TextTemplate) && len(templateLiteralSegments(artifact.TextTemplate)) == 0 {
			return newPlanValidationError(
				planErrTargetTextPlaceholderOnly,
				fmt.Sprintf("artifact contract violation: target_text artifact %q text_template cannot be only placeholder(s)", artifact.ID),
				targetTextArtifactContractHint(),
			)
		}
	}
	return nil
}

func validatePhoneWorkflowContracts(decision plannerDecision) error {
	sources := normalizePlanSources(decision.Sources)
	artifacts := normalizePlanArtifacts(decision.Artifacts)
	if len(sources) == 0 || len(artifacts) == 0 {
		return nil
	}
	targetArtifacts := make([]planArtifact, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Kind == planArtifactKindTargetText {
			targetArtifacts = append(targetArtifacts, artifact)
		}
	}
	if len(targetArtifacts) == 0 {
		return nil
	}
	for _, source := range sources {
		if source.Tool != planSourceToolContacts {
			continue
		}
		linked := false
		for _, artifact := range targetArtifacts {
			if planSourceLinksArtifact(source, artifact) {
				linked = true
				break
			}
		}
		if !linked {
			return newPlanValidationError(
				planErrSourceUnlinked,
				fmt.Sprintf("contacts source %q must be linked to a target_text artifact by artifact_id or source_refs", source.ID),
				targetTextArtifactContractHint(),
			)
		}
	}
	return nil
}

func planSourceLinksArtifact(source planSource, artifact planArtifact) bool {
	if strings.TrimSpace(source.ID) == "" || strings.TrimSpace(artifact.ID) == "" {
		return false
	}
	if source.ArtifactID != "" {
		return source.ArtifactID == artifact.ID
	}
	for _, ref := range artifact.SourceRefs {
		if sourceRefReferencesSource(ref, source.ID) {
			return true
		}
	}
	return false
}

func targetTextArtifactContractHint() map[string]any {
	return map[string]any{
		"artifact_kind": "target_text",
		"required_fields": []string{
			"id",
			"kind",
			"delivery",
			"prepare_step",
			"consume_step",
			"text_template",
			"target_app",
		},
		"step_invariants": []string{
			"prepare_step < consume_step",
			"target_open_step is optional diagnostic metadata; if present, it must be no later than consume_step",
			"all Phone Bridge app-side work that can run while Aiden is foreground must happen before open_app/search_launch_app",
		},
		"foreground_app_tools": phoneBridgeAppForegroundToolNames(),
		"runtime_boundary":     "open_app/search_launch_app are rare target-app navigation boundaries. After successful target-app navigation, runtime rejects Phone Bridge app-side tools that require Aiden foreground.",
		"source_refs_rule":     "When a contacts source provides values used by target_text, link it with artifact_id or source_refs such as contact_lookup.phone_numbers. Fixed text may omit source_refs only when it does not consume a source.",
		"template_shape_rule":  "When text_template uses placeholders, it must include non-placeholder literal text too. Placeholder-only templates such as '{{contact_lookup.phone_numbers}}' are invalid because they do not define the complete final text to prepare and later send.",
		"pre_open_boundary":    "Do every reorderable Aiden-foreground app-side tool call before open_app/search_launch_app, then navigate to the target app last.",
		"valid_shape": map[string]any{
			"sources": []map[string]any{{
				"id":          "contact_lookup",
				"tool":        planSourceToolContacts,
				"action":      planSourceActionQuery,
				"step":        1,
				"query":       "contact name",
				"produces":    []string{"phone_numbers"},
				"artifact_id": "message_text",
			}},
			"artifacts": []map[string]any{{
				"id":               "message_text",
				"kind":             planArtifactKindTargetText,
				"delivery":         planArtifactDeliveryClipboard,
				"prepare_step":     1,
				"target_open_step": 2,
				"consume_step":     3,
				"text_template":    "complete final message, including placeholders and user intent/question",
				"source_refs":      []string{"contact_lookup.phone_numbers"},
				"target_app":       "WeChat",
				"target_label":     "chat/contact label",
			}},
		},
	}
}

func templateHasPlaceholder(template string) bool {
	return templatePlaceholderRE.MatchString(template)
}

func templateLiteralSegments(template string) []string {
	template = strings.TrimSpace(template)
	if template == "" {
		return nil
	}
	parts := templatePlaceholderRE.Split(template, -1)
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		part = compactArtifactText(part)
		if part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

func artifactTextMatchesTemplate(text, template string) bool {
	text = compactArtifactText(text)
	template = compactArtifactText(template)
	if text == "" {
		return false
	}
	if !templateHasPlaceholder(template) {
		return text == template
	}
	pattern := artifactTemplatePattern(template)
	if pattern == "" {
		return false
	}
	ok, err := regexp.MatchString(pattern, text)
	if err != nil {
		return false
	}
	return ok
}

func artifactTemplatePattern(template string) string {
	template = compactArtifactText(template)
	if template == "" || !templateHasPlaceholder(template) {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("^")
	last := 0
	matches := templatePlaceholderRE.FindAllStringIndex(template, -1)
	for _, match := range matches {
		if match[0] > last {
			builder.WriteString(regexp.QuoteMeta(template[last:match[0]]))
		}
		builder.WriteString(`.+`)
		last = match[1]
	}
	if last < len(template) {
		builder.WriteString(regexp.QuoteMeta(template[last:]))
	}
	builder.WriteString("$")
	return builder.String()
}

func compactArtifactText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return blankWhitespaceRE.ReplaceAllString(text, " ")
}

func (s roleLoopState) currentStepNumber() int {
	if s.PlanStepIndex < 0 {
		return 0
	}
	return s.PlanStepIndex + 1
}

func (s *roleLoopState) findPlanArtifactState(id string) (*planArtifactState, bool) {
	id = strings.TrimSpace(id)
	if id == "" || s == nil {
		return nil, false
	}
	for i := range s.PlanArtifacts {
		if s.PlanArtifacts[i].ID == id {
			return &s.PlanArtifacts[i], true
		}
	}
	return nil, false
}

func (s roleLoopState) pendingPrepareArtifactsForCurrentStep() []planArtifactState {
	step := s.currentStepNumber()
	if step <= 0 {
		return nil
	}
	var out []planArtifactState
	for _, artifact := range s.PlanArtifacts {
		if (artifact.Kind == planArtifactKindTargetText || artifact.Kind == planArtifactKindClipboardPayload) &&
			artifact.Delivery == planArtifactDeliveryClipboard &&
			artifact.PrepareStep == step &&
			artifact.PreparedText == "" {
			out = append(out, artifact)
		}
	}
	return out
}

func (s roleLoopState) pendingConsumeArtifactsForCurrentStep() []planArtifactState {
	step := s.currentStepNumber()
	if step <= 0 {
		return nil
	}
	var out []planArtifactState
	for _, artifact := range s.PlanArtifacts {
		if artifact.Kind == planArtifactKindTargetText &&
			artifact.Delivery == planArtifactDeliveryClipboard &&
			artifact.ConsumeStep == step &&
			artifact.ConsumedAt.IsZero() {
			out = append(out, artifact)
		}
	}
	return out
}

func (s roleLoopState) unresolvedTargetTextArtifactIDs() []string {
	var ids []string
	for _, artifact := range s.PlanArtifacts {
		if artifact.Kind != planArtifactKindTargetText || artifact.Delivery != planArtifactDeliveryClipboard {
			continue
		}
		if strings.TrimSpace(artifact.PreparedText) == "" || artifact.ConsumedAt.IsZero() {
			ids = append(ids, artifact.ID)
		}
	}
	return uniqueNonEmpty(ids)
}

func (s roleLoopState) unpreparedPlanArtifactIDs(artifacts []planArtifactState) []string {
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.PreparedText) == "" {
			ids = append(ids, artifact.ID)
		}
	}
	return ids
}

type planSourceContract struct {
	ID         string
	ArtifactID string
	Tool       string
	Action     string
	Step       int
	Query      string
	Produces   []string
}

type contactsSourceValues struct {
	ContractID   string
	Query        string
	ContactNames []string
	PhoneNumbers []string
}

type contactsResultContact struct {
	Name         string   `json:"name"`
	PhoneNumbers []string `json:"phone_numbers"`
	Emails       []string `json:"emails"`
}

func (s roleLoopState) pendingSourceContractsForCurrentStep() []planSourceContract {
	contracts := s.sourceContractsForCurrentStep()
	out := make([]planSourceContract, 0, len(contracts))
	for _, contract := range contracts {
		if s.sourceContractSatisfied(contract) {
			continue
		}
		out = append(out, contract)
	}
	return out
}

func (s roleLoopState) sourceContractsForCurrentStep() []planSourceContract {
	step := s.currentStepNumber()
	if step <= 0 {
		return nil
	}
	var out []planSourceContract
	for _, source := range s.PlanSources {
		if source.Step != step {
			continue
		}
		out = append(out, planSourceContractFromState(source))
	}
	return out
}

func (s roleLoopState) sourceContractsAcrossPlan() []planSourceContract {
	out := make([]planSourceContract, 0, len(s.PlanSources))
	for _, source := range s.PlanSources {
		out = append(out, planSourceContractFromState(source))
	}
	return out
}

func planSourceContractFromState(source planSourceState) planSourceContract {
	return planSourceContract{
		ID:         source.ID,
		ArtifactID: source.ArtifactID,
		Tool:       source.Tool,
		Action:     source.Action,
		Step:       source.Step,
		Query:      source.Query,
		Produces:   append([]string{}, source.Produces...),
	}
}

func (s roleLoopState) pendingSourceContractsForArtifact(artifactID string) []planSourceContract {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil
	}
	var out []planSourceContract
	for _, contract := range s.pendingSourceContractsForCurrentStep() {
		if s.sourceContractAppliesToArtifact(contract, artifactID) {
			out = append(out, contract)
		}
	}
	return out
}

func (s roleLoopState) pendingSourceContractsForArtifactAcrossPlan(artifactID string) []planSourceContract {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return nil
	}
	var out []planSourceContract
	for _, source := range s.PlanSources {
		contract := planSourceContractFromState(source)
		if !s.sourceContractAppliesToArtifact(contract, artifactID) {
			continue
		}
		if s.sourceContractSatisfied(contract) {
			continue
		}
		out = append(out, contract)
	}
	return out
}

func (s roleLoopState) sourceContractAppliesToArtifact(contract planSourceContract, artifactID string) bool {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return false
	}
	if contract.ArtifactID != "" {
		return contract.ArtifactID == artifactID
	}
	artifact, ok := s.findPlanArtifactState(artifactID)
	if !ok {
		return false
	}
	for _, ref := range artifact.SourceRefs {
		if sourceRefReferencesSource(ref, contract.ID) {
			return true
		}
	}
	return false
}

func (s roleLoopState) sourceContractSatisfied(contract planSourceContract) bool {
	switch contract.Tool {
	case planSourceToolContacts:
		executions := append([]roleExecutionResult{}, s.ExecutionResults...)
		executions = append(executions, s.StepExecutionResults...)
		for _, execution := range executions {
			if execution.Action == nil || execution.Step == nil || execution.ToolError != nil {
				continue
			}
			if !strings.EqualFold(strings.TrimSpace(execution.Action.Tool), "contacts") {
				continue
			}
			if !contactsToolInputMatchesSource(contract, execution.Action.ToolInput) {
				continue
			}
			if contactsQueryResultHasData(execution.Step.Observation, contract.Produces) {
				return true
			}
		}
	}
	return false
}

func sourceRefSourceID(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if idx := strings.Index(ref, "."); idx > 0 {
		return strings.TrimSpace(ref[:idx])
	}
	return ref
}

func sourceRefReferencesSource(ref, sourceID string) bool {
	ref = strings.TrimSpace(ref)
	sourceID = strings.TrimSpace(sourceID)
	if ref == "" || sourceID == "" {
		return false
	}
	return ref == sourceID || strings.HasPrefix(ref, sourceID+".")
}

func contactsToolInputMatchesSource(contract planSourceContract, input string) bool {
	var args struct {
		Action string `json:"action"`
		Query  string `json:"query"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(args.Action), planSourceActionQuery) {
		return false
	}
	if contract.Query == "" {
		return true
	}
	return strings.TrimSpace(args.Query) == contract.Query
}

func contactsQueryResultHasData(output string, produces []string) bool {
	var payload struct {
		OK       bool                    `json:"ok"`
		Contacts []contactsResultContact `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return false
	}
	if !payload.OK || len(payload.Contacts) == 0 {
		return false
	}
	required := normalizePlanSourceProduces(produces)
	if len(required) == 0 {
		return true
	}
	for _, field := range required {
		switch field {
		case "phone_numbers":
			if !contactsResultHasPhoneNumber(payload.Contacts) {
				return false
			}
		case "contact_names":
			if !contactsResultHasName(payload.Contacts) {
				return false
			}
		case "emails":
			if !contactsResultHasEmail(payload.Contacts) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func contactsResultHasPhoneNumber(contacts []contactsResultContact) bool {
	for _, contact := range contacts {
		if len(uniqueNonEmpty(contact.PhoneNumbers)) > 0 {
			return true
		}
	}
	return false
}

func contactsResultHasName(contacts []contactsResultContact) bool {
	for _, contact := range contacts {
		if strings.TrimSpace(contact.Name) != "" {
			return true
		}
	}
	return false
}

func contactsResultHasEmail(contacts []contactsResultContact) bool {
	for _, contact := range contacts {
		if len(uniqueNonEmpty(contact.Emails)) > 0 {
			return true
		}
	}
	return false
}

func contactsQueryResultValues(output string) []contactsSourceValues {
	var payload struct {
		OK       bool `json:"ok"`
		Contacts []struct {
			Name         string   `json:"name"`
			PhoneNumbers []string `json:"phone_numbers"`
		} `json:"contacts"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil || !payload.OK {
		return nil
	}
	values := make([]contactsSourceValues, 0, len(payload.Contacts))
	for _, contact := range payload.Contacts {
		names := uniqueNonEmpty([]string{contact.Name})
		phones := uniqueNonEmpty(contact.PhoneNumbers)
		if len(names) == 0 && len(phones) == 0 {
			continue
		}
		values = append(values, contactsSourceValues{
			ContactNames: names,
			PhoneNumbers: phones,
		})
	}
	return values
}

func contactsQueryInputValue(input string) string {
	var args struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(input)), &args); err != nil {
		return ""
	}
	return strings.TrimSpace(args.Query)
}

func mergeContactsSourceValues(values []contactsSourceValues) contactsSourceValues {
	var merged contactsSourceValues
	for _, value := range values {
		if merged.ContractID == "" {
			merged.ContractID = value.ContractID
		}
		if merged.Query == "" {
			merged.Query = value.Query
		}
		merged.ContactNames = uniqueNonEmpty(append(merged.ContactNames, value.ContactNames...))
		merged.PhoneNumbers = uniqueNonEmpty(append(merged.PhoneNumbers, value.PhoneNumbers...))
	}
	return merged
}

func (s roleLoopState) contactsSourceValuesForContract(contract planSourceContract) []contactsSourceValues {
	if contract.Tool != planSourceToolContacts {
		return nil
	}
	executions := append([]roleExecutionResult{}, s.ExecutionResults...)
	executions = append(executions, s.StepExecutionResults...)
	var out []contactsSourceValues
	for _, execution := range executions {
		if execution.Action == nil || execution.Step == nil || execution.ToolError != nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(execution.Action.Tool), "contacts") {
			continue
		}
		if !contactsToolInputMatchesSource(contract, execution.Action.ToolInput) {
			continue
		}
		query := contactsQueryInputValue(execution.Action.ToolInput)
		for _, values := range contactsQueryResultValues(execution.Step.Observation) {
			values.ContractID = contract.ID
			if values.Query == "" {
				values.Query = query
			}
			out = append(out, values)
		}
	}
	return out
}

func (s roleLoopState) explicitContactsSourceValuesForArtifact(artifact planArtifactState) []contactsSourceValues {
	var out []contactsSourceValues
	for _, source := range s.PlanSources {
		contract := planSourceContractFromState(source)
		if contract.Tool != planSourceToolContacts || !s.sourceContractAppliesToArtifact(contract, artifact.ID) {
			continue
		}
		out = append(out, s.contactsSourceValuesForContract(contract)...)
	}
	return out
}

func (s roleLoopState) contactsSourceValuesForArtifact(artifact planArtifactState) []contactsSourceValues {
	if artifact.Kind != planArtifactKindTargetText {
		return nil
	}
	explicit := s.explicitContactsSourceValuesForArtifact(artifact)
	if len(explicit) == 0 {
		return nil
	}
	return []contactsSourceValues{mergeContactsSourceValues(explicit)}
}

func (s roleLoopState) missingContactsSourceValuesForClipboardText(artifact planArtifactState, text string) []string {
	return missingContactsSourceValues(s.contactsSourceValuesForArtifact(artifact), text)
}

func missingContactsSourceValues(values []contactsSourceValues, text string) []string {
	if len(values) == 0 {
		return nil
	}
	var required []string
	for _, value := range values {
		required = append(required, value.PhoneNumbers...)
	}
	required = uniqueNonEmpty(required)
	if len(required) == 0 {
		return nil
	}
	var missing []string
	for _, value := range required {
		if !artifactTextContainsValue(text, value) {
			missing = append(missing, value)
		}
	}
	return missing
}

func artifactTextContainsValue(text, value string) bool {
	text = strings.TrimSpace(text)
	value = strings.TrimSpace(value)
	if text == "" || value == "" {
		return false
	}
	if strings.Contains(text, value) {
		return true
	}
	textDigits := digitsOnly(text)
	valueDigits := digitsOnly(value)
	return valueDigits != "" && strings.Contains(textDigits, valueDigits)
}

func digitsOnly(text string) string {
	var builder strings.Builder
	for _, r := range text {
		if r >= '0' && r <= '9' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func planSourceContractIDs(contracts []planSourceContract) []string {
	ids := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		if contract.ID != "" {
			ids = append(ids, contract.ID)
		}
	}
	return ids
}

func formatPlanSourceStates(sources []planSourceState) string {
	if len(sources) == 0 {
		return ""
	}
	lines := make([]string, 0, len(sources))
	for _, source := range sources {
		line := fmt.Sprintf("- id=%s tool=%s action=%s step=%d",
			source.ID,
			source.Tool,
			source.Action,
			source.Step,
		)
		if source.Query != "" {
			line += " query=" + strconv.Quote(source.Query)
		}
		if len(source.Produces) > 0 {
			line += " produces=" + strings.Join(source.Produces, ",")
		}
		if source.ArtifactID != "" {
			line += " artifact_id=" + source.ArtifactID
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (s *roleLoopState) beforeArtifactToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	if s == nil {
		return ToolResult{}, true
	}
	toolName := strings.TrimSpace(call.Spec.Name)
	if s.PlanCommitted && phoneBridgeAppForegroundTool(toolName) && s.targetAppNavigationAlreadyExecuted() {
		return rejectArtifactToolCall("app-side tool " + strconv.Quote(toolName) + " requires Aiden foreground and must run before target-app navigation. Replan so all reorderable Phone Bridge app-side work (" + strings.Join(phoneBridgeAppForegroundToolNames(), ", ") + ") is batched before open_app/search_launch_app; treat open_app as the final boundary before target-app UI automation.")
	}
	if len(s.PlanArtifacts) == 0 && len(s.PlanSources) == 0 && !(s.PlanCommitted && toolName == "clipboard") {
		return ToolResult{}, true
	}
	switch toolName {
	case "clipboard":
		return s.beforeClipboardToolCall(ctx, call)
	case "enter_text_in_field", "enter_text_via_bridge":
		return s.beforeTextEntryToolCall(ctx, call)
	case "prepare_phone_app_workflow":
		return s.beforePreparePhoneWorkflowToolCall(ctx, call)
	case "open_app", "search_launch_app":
		if contracts := s.sourceContractsForCurrentStep(); len(contracts) > 0 {
			target := toolCallTargetAppName(call)
			if appLabelsMatch(target, "contacts") {
				return rejectArtifactToolCall("Contacts data source contract requires contacts action=query before preparing target text; do not open Contacts UI for this data step. Source contract(s): " + strings.Join(planSourceContractIDs(contracts), ", "))
			}
		}
		if pending := s.pendingTargetAppPrepareArtifactsForCall(call); len(pending) > 0 {
			target := toolCallTargetAppName(call)
			if target == "" {
				target = "target app"
			}
			return rejectArtifactToolCall("target-app navigation to " + strconv.Quote(target) + " is blocked until pre-open app contract(s) are satisfied for artifact(s): " + strings.Join(planArtifactStateIDs(pending), ", "))
		}
		return ToolResult{}, true
	case "contacts":
		if pending := s.pendingSourceContractsForCurrentStep(); len(pending) > 0 && !contactsToolInputMatchesAnySource(pending, call.Input) {
			return rejectArtifactToolCall("Contacts source contract requires a matching contacts action=query call. Pending source contract(s): " + strings.Join(planSourceContractIDs(pending), ", "))
		}
		if s.PlanCommitted && len(s.PlanSources) > 0 && !contactsToolInputMatchesAnySource(s.sourceContractsAcrossPlan(), call.Input) {
			return rejectArtifactToolCall("Contacts app-side tool is restricted to declared source contract(s) in this committed plan: " + strings.Join(planSourceContractIDs(s.sourceContractsAcrossPlan()), ", ") + ". Use the target app search/navigation path for non-source contacts.")
		}
		return ToolResult{}, true
	default:
		return ToolResult{}, true
	}
}

func (s roleLoopState) beforePreparePhoneWorkflowToolCall(_ context.Context, call ToolCall) (ToolResult, bool) {
	if !s.PlanCommitted {
		return ToolResult{}, true
	}
	var args struct {
		OpenTargetApp bool   `json:"open_target_app"`
		TargetApp     string `json:"target_app"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil || !args.OpenTargetApp {
		return ToolResult{}, true
	}
	target := strings.TrimSpace(args.TargetApp)
	if s.preparePhoneWorkflowCanOpenTargetApp(target) {
		return ToolResult{}, true
	}
	if target == "" {
		target = "target app"
	}
	return rejectArtifactToolCall("prepare workflow cannot open " + strconv.Quote(target) + " before the committed target-app boundary. Retry this workflow with open_target_app=false to finish Aiden-foreground preparation, then open the target app in its committed open/consume step.")
}

func (s roleLoopState) preparePhoneWorkflowCanOpenTargetApp(targetApp string) bool {
	if !s.PlanCommitted {
		return true
	}
	targetApp = strings.TrimSpace(targetApp)
	if targetApp == "" {
		return true
	}
	step := s.currentStepNumber()
	if step <= 0 {
		return true
	}
	for _, artifact := range s.PlanArtifacts {
		if artifact.Kind != planArtifactKindTargetText ||
			artifact.Delivery != planArtifactDeliveryClipboard ||
			!appLabelsMatch(targetApp, artifact.TargetApp) {
			continue
		}
		boundary := artifact.TargetOpenStep
		if boundary <= 0 {
			boundary = artifact.ConsumeStep
		}
		if boundary > 0 && step < boundary {
			return false
		}
	}
	return true
}

func phoneBridgeAppForegroundTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "clipboard", "calendar", "contacts", "notification":
		return true
	default:
		return false
	}
}

func phoneBridgeAppForegroundToolNames() []string {
	return []string{"clipboard", "calendar", "contacts", "notification"}
}

func (s roleLoopState) targetAppNavigationAlreadyExecuted() bool {
	executions := append([]roleExecutionResult{}, s.ExecutionResults...)
	executions = append(executions, s.StepExecutionResults...)
	for _, execution := range executions {
		if execution.Action == nil || execution.Step == nil || execution.ToolError != nil {
			continue
		}
		switch strings.TrimSpace(execution.Action.Tool) {
		case "open_app", "search_launch_app":
			if toolObservationOK(execution.Step.Observation) {
				return true
			}
		case "prepare_phone_app_workflow":
			if workflowOpenedTargetApp(execution.Step.Observation) {
				return true
			}
		}
	}
	return false
}

func workflowOpenedTargetApp(observation string) bool {
	var payload struct {
		OK              bool `json:"ok"`
		OpenedTargetApp bool `json:"opened_target_app"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(observation)), &payload); err != nil {
		return false
	}
	return payload.OK && payload.OpenedTargetApp
}

func toolObservationOK(observation string) bool {
	observation = strings.TrimSpace(observation)
	if observation == "" {
		return false
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(observation), &payload); err != nil {
		return true
	}
	return payload.OK
}

func contactsToolInputMatchesAnySource(contracts []planSourceContract, input string) bool {
	for _, contract := range contracts {
		if contract.Tool == planSourceToolContacts && contactsToolInputMatchesSource(contract, input) {
			return true
		}
	}
	return false
}

func (s roleLoopState) pendingTargetAppPrepareArtifactsForCall(call ToolCall) []planArtifactState {
	target := toolCallTargetAppName(call)
	if strings.TrimSpace(target) == "" {
		return nil
	}
	step := s.currentStepNumber()
	if step <= 0 {
		return nil
	}
	var out []planArtifactState
	for _, artifact := range s.PlanArtifacts {
		if artifact.Kind != planArtifactKindTargetText ||
			artifact.Delivery != planArtifactDeliveryClipboard ||
			!appLabelsMatch(target, artifact.TargetApp) {
			continue
		}
		if artifact.TargetOpenStep > 0 && step < artifact.TargetOpenStep {
			out = append(out, artifact)
			continue
		}
		if strings.TrimSpace(artifact.PreparedText) == "" {
			out = append(out, artifact)
			continue
		}
		if len(s.pendingSourceContractsForArtifactAcrossPlan(artifact.ID)) > 0 {
			out = append(out, artifact)
		}
	}
	return out
}

func toolCallTargetAppName(call ToolCall) string {
	input := strings.TrimSpace(call.Input)
	if input == "" {
		return ""
	}
	var args struct {
		App  string `json:"app"`
		Name string `json:"name"`
	}
	if strings.HasPrefix(input, "{") {
		if err := json.Unmarshal([]byte(input), &args); err != nil {
			return ""
		}
		if app := strings.TrimSpace(args.App); app != "" {
			return app
		}
		return strings.TrimSpace(args.Name)
	}
	return input
}

func appLabelsMatch(a, b string) bool {
	left := appLabelMatchKeys(a)
	right := appLabelMatchKeys(b)
	for key := range left {
		if _, ok := right[key]; ok {
			return true
		}
	}
	return false
}

func appLabelMatchKeys(label string) map[string]struct{} {
	out := map[string]struct{}{}
	if normalized := normalizeArtifactAppLabel(label); normalized != "" {
		out[normalized] = struct{}{}
	}
	for _, token := range splitArtifactAppLabelTokens(label) {
		token = normalizeArtifactAppLabelText(token)
		if canonical, ok := artifactAppAliasCanonical(token); ok {
			out[canonical] = struct{}{}
		}
	}
	return out
}

func normalizeArtifactAppLabel(label string) string {
	label = normalizeArtifactAppLabelText(label)
	if canonical, ok := artifactAppAliasCanonical(label); ok {
		return canonical
	}
	return label
}

func normalizeArtifactAppLabelText(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" {
		return ""
	}
	label = trimStandaloneAppSuffix(strings.TrimSpace(label))
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "\r", "", "-", "", "_", "")
	return replacer.Replace(strings.TrimSpace(label))
}

func trimStandaloneAppSuffix(label string) string {
	if len(label) <= len("app") || !strings.HasSuffix(label, "app") {
		return label
	}
	rawPrefix := label[:len(label)-len("app")]
	prefix := strings.TrimSpace(rawPrefix)
	if prefix == "" {
		return label
	}
	last, _ := utf8.DecodeLastRuneInString(rawPrefix)
	if last == ' ' || last == '-' || last == '_' || unicode.Is(unicode.Han, last) {
		return prefix
	}
	return label
}

func splitArtifactAppLabelTokens(label string) []string {
	return strings.FieldsFunc(label, func(r rune) bool {
		return unicode.IsSpace(r) ||
			strings.ContainsRune(artifactAppLabelTokenSeparators, r)
	})
}

func artifactAppAliasCanonical(label string) (string, bool) {
	switch label {
	case "wechat", "weixin":
		return "wechat", true
	case "contacts", "contact", "addressbook", "phonebook":
		return "contacts", true
	default:
		return "", false
	}
}

func (s *roleLoopState) afterArtifactToolCall(ctx context.Context, call ToolCall, result ToolResult) ToolResult {
	result = DefaultAfterToolCall(ctx, call, result)
	if s == nil || len(s.PlanArtifacts) == 0 || result.Error != nil {
		return result
	}
	switch strings.TrimSpace(call.Spec.Name) {
	case "clipboard":
		s.afterClipboardToolCall(call, result)
	case "enter_text_in_field", "enter_text_via_bridge":
		s.afterTextEntryToolCall(call, result)
	case "prepare_phone_app_workflow":
		s.afterPreparePhoneWorkflowToolCall(call, result)
	}
	return result
}

func (s *roleLoopState) beforeClipboardToolCall(_ context.Context, call ToolCall) (ToolResult, bool) {
	var args struct {
		Action     string `json:"action"`
		Text       string `json:"text"`
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{}, true
	}
	if !strings.EqualFold(strings.TrimSpace(args.Action), "write") {
		return ToolResult{}, true
	}
	pending := s.pendingPrepareArtifactsForCurrentStep()
	artifactID := strings.TrimSpace(args.ArtifactID)
	if artifactID == "" && len(pending) == 0 && !s.PlanCommitted {
		return ToolResult{}, true
	}
	if artifactID == "" {
		if len(pending) > 0 {
			return rejectArtifactToolCall("clipboard write must include artifact_id for current clipboard artifact contract(s): " + strings.Join(planArtifactStateIDs(pending), ", "))
		}
		return rejectArtifactToolCall("clipboard write in a committed plan must include artifact_id declared by commit_plan artifacts")
	}
	artifact, ok := s.findPlanArtifactState(artifactID)
	if !ok {
		return rejectArtifactToolCall("clipboard artifact_id " + strconv.Quote(artifactID) + " is not declared in commit_plan artifacts")
	}
	if artifact.Delivery != planArtifactDeliveryClipboard ||
		(artifact.Kind != planArtifactKindTargetText && artifact.Kind != planArtifactKindClipboardPayload) {
		return rejectArtifactToolCall("clipboard artifact_id " + strconv.Quote(artifactID) + " is not a clipboard artifact")
	}
	if artifact.PrepareStep > 0 && artifact.PrepareStep != s.currentStepNumber() && !artifactCanPrepareInCurrentStep(*artifact, s.currentStepNumber()) {
		return rejectArtifactToolCall(fmt.Sprintf("clipboard artifact_id %q belongs to prepare_step=%d, but current step is %d", artifactID, artifact.PrepareStep, s.currentStepNumber()))
	}
	if artifact.Kind == planArtifactKindTargetText && artifact.ConsumeStep <= s.currentStepNumber() {
		return rejectArtifactToolCall(fmt.Sprintf("clipboard target_text artifact_id %q must consume in a later step than current step %d", artifactID, s.currentStepNumber()))
	}
	if artifact.Kind == planArtifactKindClipboardPayload && s.currentStepNumber() < len(s.Plan) {
		return rejectArtifactToolCall("clipboard_payload artifact_id " + strconv.Quote(artifactID) + " cannot be prepared before later plan steps; use target_text with a future consume_step for cross-step text delivery")
	}
	if pending := s.pendingSourceContractsForArtifactAcrossPlan(artifactID); len(pending) > 0 {
		return rejectArtifactToolCall("clipboard artifact_id " + strconv.Quote(artifactID) + " cannot be prepared until source contract(s) are satisfied: " + strings.Join(planSourceContractIDs(pending), ", ") + ". Use contacts action=query first.")
	}
	contactsValues := s.contactsSourceValuesForArtifact(*artifact)
	if missing := missingContactsSourceValues(contactsValues, args.Text); len(missing) > 0 {
		return rejectArtifactToolCall("clipboard text for artifact_id " + strconv.Quote(artifactID) + " omits contacts source value(s): " + strings.Join(missing, ", ") + ". Compose the target text from the contacts tool result before opening the target app.")
	}
	if strings.TrimSpace(artifact.TextTemplate) != "" && !artifactTextMatchesTemplate(args.Text, artifact.TextTemplate) &&
		!(len(contactsValues) > 0 && !templateHasPlaceholder(artifact.TextTemplate) && len(missingContactsSourceValues(contactsValues, args.Text)) == 0) {
		return rejectArtifactToolCall("clipboard text does not satisfy text_template for artifact_id " + strconv.Quote(artifactID))
	}
	return ToolResult{}, true
}

func artifactCanPrepareInCurrentStep(artifact planArtifactState, step int) bool {
	if artifact.Kind != planArtifactKindTargetText {
		return false
	}
	if step <= 0 {
		return false
	}
	if artifact.ConsumeStep > 0 && step >= artifact.ConsumeStep {
		return false
	}
	return true
}

func (s *roleLoopState) beforeTextEntryToolCall(_ context.Context, call ToolCall) (ToolResult, bool) {
	var args struct {
		Text            string `json:"text"`
		ArtifactID      string `json:"artifact_id"`
		Mode            string `json:"mode"`
		SendAfterCommit bool   `json:"send_after_commit"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{}, true
	}
	pending := s.pendingConsumeArtifactsForCurrentStep()
	artifactID := strings.TrimSpace(args.ArtifactID)
	if artifactID == "" && len(pending) == 0 {
		return ToolResult{}, true
	}
	if artifactID == "" {
		if normalizeTextInputInteractionMode(args.Mode) == textInputModeSearch && !args.SendAfterCommit {
			return ToolResult{}, true
		}
		return rejectArtifactToolCall("text entry must include artifact_id for current prepared-text contract(s): " + strings.Join(planArtifactStateIDs(pending), ", ") + ` unless it is structured search input with mode:"search" and send_after_commit=false`)
	}
	artifact, ok := s.findPlanArtifactState(artifactID)
	if !ok {
		return rejectArtifactToolCall("text entry artifact_id " + strconv.Quote(artifactID) + " is not declared in commit_plan artifacts")
	}
	if artifact.Kind != planArtifactKindTargetText {
		return rejectArtifactToolCall("text entry artifact_id " + strconv.Quote(artifactID) + " is not a target_text artifact")
	}
	if artifact.ConsumeStep > 0 && artifact.ConsumeStep != s.currentStepNumber() && !s.canConsumePreparedTargetTextArtifactNow(*artifact) {
		return rejectArtifactToolCall(fmt.Sprintf("text entry artifact_id %q belongs to consume_step=%d, but current step is %d", artifactID, artifact.ConsumeStep, s.currentStepNumber()))
	}
	if strings.TrimSpace(artifact.PreparedText) == "" {
		return rejectArtifactToolCall("text entry artifact_id " + strconv.Quote(artifactID) + " has not been prepared by clipboard write yet")
	}
	if compactArtifactText(args.Text) != compactArtifactText(artifact.PreparedText) {
		return rejectArtifactToolCall("text entry text must exactly match prepared text for artifact_id " + strconv.Quote(artifactID))
	}
	return ToolResult{}, true
}

func (s roleLoopState) canConsumePreparedTargetTextArtifactNow(artifact planArtifactState) bool {
	if artifact.Kind != planArtifactKindTargetText ||
		artifact.Delivery != planArtifactDeliveryClipboard ||
		strings.TrimSpace(artifact.PreparedText) == "" ||
		!artifact.ConsumedAt.IsZero() {
		return false
	}
	return s.targetAppNavigationAlreadyExecuted()
}

func (s *roleLoopState) afterClipboardToolCall(call ToolCall, result ToolResult) {
	var args struct {
		Action     string `json:"action"`
		Text       string `json:"text"`
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(args.Action), "write") || strings.TrimSpace(args.ArtifactID) == "" {
		return
	}
	if artifact, ok := s.findPlanArtifactState(args.ArtifactID); ok {
		artifact.PreparedText = strings.TrimSpace(args.Text)
		artifact.PreparedAt = time.Now()
	}
}

func (s *roleLoopState) afterTextEntryToolCall(call ToolCall, result ToolResult) {
	var args struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil || strings.TrimSpace(args.ArtifactID) == "" {
		return
	}
	var payload struct {
		Committed bool `json:"committed"`
		OK        bool `json:"ok"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Output)), &payload); err != nil {
		return
	}
	if !payload.Committed {
		return
	}
	if artifact, ok := s.findPlanArtifactState(args.ArtifactID); ok {
		artifact.ConsumedAt = time.Now()
	}
}

func (s *roleLoopState) afterPreparePhoneWorkflowToolCall(call ToolCall, result ToolResult) {
	var payload struct {
		OK                bool   `json:"ok"`
		TargetApp         string `json:"target_app"`
		TargetText        string `json:"target_text"`
		ClipboardPrepared bool   `json:"clipboard_prepared"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.Output)), &payload); err != nil {
		return
	}
	targetText := strings.TrimSpace(payload.TargetText)
	if !payload.OK || !payload.ClipboardPrepared || targetText == "" {
		return
	}
	artifact := s.preparePhoneWorkflowArtifactForCall(call, payload.TargetApp, targetText)
	if artifact == nil {
		return
	}
	artifact.PreparedText = targetText
	artifact.PreparedAt = time.Now()
}

func (s *roleLoopState) preparePhoneWorkflowArtifactForCall(call ToolCall, targetApp, targetText string) *planArtifactState {
	var args struct {
		ArtifactID string `json:"artifact_id"`
	}
	_ = json.Unmarshal([]byte(call.Input), &args)
	if artifactID := strings.TrimSpace(args.ArtifactID); artifactID != "" {
		if artifact, ok := s.findPlanArtifactState(artifactID); ok && s.preparePhoneWorkflowArtifactMatches(*artifact, targetApp, targetText) {
			return artifact
		}
		return nil
	}
	var match *planArtifactState
	for i := range s.PlanArtifacts {
		if !s.preparePhoneWorkflowArtifactMatches(s.PlanArtifacts[i], targetApp, targetText) {
			continue
		}
		if match != nil {
			return nil
		}
		match = &s.PlanArtifacts[i]
	}
	return match
}

func (s roleLoopState) preparePhoneWorkflowArtifactMatches(artifact planArtifactState, targetApp, targetText string) bool {
	if artifact.Kind != planArtifactKindTargetText ||
		artifact.Delivery != planArtifactDeliveryClipboard ||
		!artifact.ConsumedAt.IsZero() {
		return false
	}
	if targetApp != "" && artifact.TargetApp != "" && !appLabelsMatch(targetApp, artifact.TargetApp) {
		return false
	}
	if artifact.ConsumeStep > 0 && s.currentStepNumber() >= artifact.ConsumeStep {
		return false
	}
	contactsValues := s.contactsSourceValuesForArtifact(artifact)
	if len(contactsValues) > 0 && len(missingContactsSourceValues(contactsValues, targetText)) > 0 {
		return false
	}
	template := strings.TrimSpace(artifact.TextTemplate)
	if template == "" || artifactTextMatchesTemplate(targetText, template) {
		return true
	}
	return len(contactsValues) > 0 && !templateHasPlaceholder(template)
}

func rejectArtifactToolCall(message string) (ToolResult, bool) {
	te := NewToolError(CodeInvalidArguments, message)
	return ToolResult{Output: te.Message, Error: te}, false
}

func planArtifactStateIDs(artifacts []planArtifactState) []string {
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.ID != "" {
			ids = append(ids, artifact.ID)
		}
	}
	return ids
}

func formatPlanArtifactStates(artifacts []planArtifactState) string {
	if len(artifacts) == 0 {
		return ""
	}
	lines := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		status := "pending"
		if artifact.ConsumedAt.IsZero() && strings.TrimSpace(artifact.PreparedText) != "" {
			status = "prepared"
		}
		if !artifact.ConsumedAt.IsZero() {
			status = "consumed"
		}
		line := fmt.Sprintf("- id=%s kind=%s delivery=%s prepare_step=%d target_open_step=%d consume_step=%d status=%s",
			artifact.ID,
			artifact.Kind,
			artifact.Delivery,
			artifact.PrepareStep,
			artifact.TargetOpenStep,
			artifact.ConsumeStep,
			status,
		)
		if template := compactPromptLine(artifact.TextTemplate, 220); template != "" {
			line += " text_template=" + strconv.Quote(template)
		}
		if len(artifact.SourceRefs) > 0 {
			line += " source_refs=" + strings.Join(artifact.SourceRefs, ",")
		}
		if strings.TrimSpace(artifact.PreparedText) != "" {
			line += " prepared_text=" + strconv.Quote(compactPromptLine(artifact.PreparedText, 220))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
