package agent

import (
	"context"
	"encoding/json"
	"fmt"
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
)

var templatePlaceholderRE = regexp.MustCompile(`\{\{\s*[^{}]+\s*\}\}`)

type planArtifact struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Delivery     string   `json:"delivery,omitempty"`
	PrepareStep  int      `json:"prepare_step,omitempty"`
	ConsumeStep  int      `json:"consume_step,omitempty"`
	TextTemplate string   `json:"text_template,omitempty"`
	SourceRefs   []string `json:"source_refs,omitempty"`
	TargetApp    string   `json:"target_app,omitempty"`
	TargetLabel  string   `json:"target_label,omitempty"`
}

type planArtifactState struct {
	planArtifact
	PreparedText string
	PreparedAt   time.Time
	ConsumedAt   time.Time
}

func parsePlanArtifacts(raw json.RawMessage) ([]planArtifact, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var artifacts []planArtifact
	if err := json.Unmarshal(raw, &artifacts); err != nil {
		return nil, fmt.Errorf("artifacts must be an array of objects: %w", err)
	}
	return normalizePlanArtifacts(artifacts), nil
}

func normalizePlanArtifacts(artifacts []planArtifact) []planArtifact {
	out := make([]planArtifact, 0, len(artifacts))
	seen := map[string]bool{}
	for _, artifact := range artifacts {
		artifact.ID = strings.TrimSpace(artifact.ID)
		artifact.Kind = normalizePlanArtifactKind(artifact.Kind)
		artifact.Delivery = normalizePlanArtifactDelivery(artifact.Delivery)
		artifact.TextTemplate = strings.TrimSpace(artifact.TextTemplate)
		artifact.SourceRefs = uniqueNonEmpty(artifact.SourceRefs)
		artifact.TargetApp = strings.TrimSpace(artifact.TargetApp)
		artifact.TargetLabel = strings.TrimSpace(artifact.TargetLabel)
		if artifact.ID == "" || artifact.Kind == "" || seen[artifact.ID] {
			continue
		}
		seen[artifact.ID] = true
		out = append(out, artifact)
	}
	return out
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

func initialPlanArtifactStates(artifacts []planArtifact) []planArtifactState {
	artifacts = normalizePlanArtifacts(artifacts)
	out := make([]planArtifactState, 0, len(artifacts))
	for _, artifact := range artifacts {
		out = append(out, planArtifactState{planArtifact: artifact})
	}
	return out
}

func validatePlanArtifacts(artifacts []planArtifact, planLen int) error {
	seen := map[string]bool{}
	for _, artifact := range normalizePlanArtifacts(artifacts) {
		if seen[artifact.ID] {
			return fmt.Errorf("duplicate artifact id %q", artifact.ID)
		}
		seen[artifact.ID] = true
		if artifact.Kind == planArtifactKindTargetText {
			if artifact.Delivery != planArtifactDeliveryClipboard {
				return fmt.Errorf("target_text artifact %q must use delivery=clipboard", artifact.ID)
			}
			if artifact.PrepareStep <= 0 {
				return fmt.Errorf("target_text artifact %q requires prepare_step", artifact.ID)
			}
			if artifact.ConsumeStep <= 0 {
				return fmt.Errorf("target_text artifact %q requires consume_step", artifact.ID)
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
			if artifact.ConsumeStep > planLen {
				return fmt.Errorf("artifact %q consume_step %d exceeds plan length %d", artifact.ID, artifact.ConsumeStep, planLen)
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
		if artifact.ConsumeStep <= artifact.PrepareStep {
			return fmt.Errorf("artifact contract violation: target_text artifact %q must consume in a later step than it is prepared", artifact.ID)
		}
		if !templateHasPlaceholder(artifact.TextTemplate) {
			return fmt.Errorf("artifact contract violation: target_text artifact %q text_template must include at least one {{source}} placeholder for data gathered before target-app navigation", artifact.ID)
		}
		if len(templateLiteralSegments(artifact.TextTemplate)) == 0 {
			return fmt.Errorf("artifact contract violation: target_text artifact %q text_template must include literal surrounding text, not only placeholders", artifact.ID)
		}
	}
	return nil
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
	if text == "" {
		return false
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
	if template == "" || !templateHasPlaceholder(template) || len(templateLiteralSegments(template)) == 0 {
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

func (s roleLoopState) unpreparedPlanArtifactIDs(artifacts []planArtifactState) []string {
	ids := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		if strings.TrimSpace(artifact.PreparedText) == "" {
			ids = append(ids, artifact.ID)
		}
	}
	return ids
}

func (s *roleLoopState) beforeArtifactToolCall(ctx context.Context, call ToolCall) (ToolResult, bool) {
	if s == nil {
		return ToolResult{}, true
	}
	toolName := strings.TrimSpace(call.Spec.Name)
	if len(s.PlanArtifacts) == 0 && !(s.PlanCommitted && toolName == "clipboard") {
		return ToolResult{}, true
	}
	switch toolName {
	case "clipboard":
		return s.beforeClipboardToolCall(ctx, call)
	case "enter_text_in_field", "enter_text_via_bridge":
		return s.beforeTextEntryToolCall(ctx, call)
	case "open_app", "search_launch_app":
		if pending := s.pendingTargetAppPrepareArtifactsForCall(call); len(pending) > 0 {
			target := toolCallTargetAppName(call)
			if target == "" {
				target = "target app"
			}
			return rejectArtifactToolCall("target-app navigation to " + strconv.Quote(target) + " is blocked until current step prepares artifact contract(s): " + strings.Join(planArtifactStateIDs(pending), ", "))
		}
		return ToolResult{}, true
	default:
		return ToolResult{}, true
	}
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
			artifact.PrepareStep != step ||
			artifact.PreparedText != "" {
			continue
		}
		if appLabelsMatch(target, artifact.TargetApp) {
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
	label = strings.TrimSuffix(label, "应用")
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
			strings.ContainsRune("-_/／|,，;；:：()（）[]【】", r)
	})
}

func artifactAppAliasCanonical(label string) (string, bool) {
	switch label {
	case "wechat", "weixin", "微信":
		return "wechat", true
	case "contacts", "contact", "addressbook", "phonebook", "通讯录", "联系人":
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
	if artifact.PrepareStep > 0 && artifact.PrepareStep != s.currentStepNumber() {
		return rejectArtifactToolCall(fmt.Sprintf("clipboard artifact_id %q belongs to prepare_step=%d, but current step is %d", artifactID, artifact.PrepareStep, s.currentStepNumber()))
	}
	if artifact.Kind == planArtifactKindTargetText && artifact.ConsumeStep <= s.currentStepNumber() {
		return rejectArtifactToolCall(fmt.Sprintf("clipboard target_text artifact_id %q must consume in a later step than current step %d", artifactID, s.currentStepNumber()))
	}
	if artifact.Kind == planArtifactKindClipboardPayload && s.currentStepNumber() < len(s.Plan) {
		return rejectArtifactToolCall("clipboard_payload artifact_id " + strconv.Quote(artifactID) + " cannot be prepared before later plan steps; use target_text with a future consume_step for cross-step text delivery")
	}
	if strings.TrimSpace(artifact.TextTemplate) != "" && !artifactTextMatchesTemplate(args.Text, artifact.TextTemplate) {
		return rejectArtifactToolCall("clipboard text does not satisfy text_template for artifact_id " + strconv.Quote(artifactID))
	}
	return ToolResult{}, true
}

func (s *roleLoopState) beforeTextEntryToolCall(_ context.Context, call ToolCall) (ToolResult, bool) {
	var args struct {
		Text       string `json:"text"`
		ArtifactID string `json:"artifact_id"`
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
		return rejectArtifactToolCall("text entry must include artifact_id for current prepared-text contract(s): " + strings.Join(planArtifactStateIDs(pending), ", "))
	}
	artifact, ok := s.findPlanArtifactState(artifactID)
	if !ok {
		return rejectArtifactToolCall("text entry artifact_id " + strconv.Quote(artifactID) + " is not declared in commit_plan artifacts")
	}
	if artifact.Kind != planArtifactKindTargetText {
		return rejectArtifactToolCall("text entry artifact_id " + strconv.Quote(artifactID) + " is not a target_text artifact")
	}
	if artifact.ConsumeStep > 0 && artifact.ConsumeStep != s.currentStepNumber() {
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
		line := fmt.Sprintf("- id=%s kind=%s delivery=%s prepare_step=%d consume_step=%d status=%s",
			artifact.ID,
			artifact.Kind,
			artifact.Delivery,
			artifact.PrepareStep,
			artifact.ConsumeStep,
			status,
		)
		if template := compactPromptLine(artifact.TextTemplate, 220); template != "" {
			line += " text_template=" + strconv.Quote(template)
		}
		if strings.TrimSpace(artifact.PreparedText) != "" {
			line += " prepared_text=" + strconv.Quote(compactPromptLine(artifact.PreparedText, 220))
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
