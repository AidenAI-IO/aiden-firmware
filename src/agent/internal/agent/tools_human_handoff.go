package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// HumanHandoffTool allows the agent to request human intervention when it encounters
// situations that require human judgment, input, or actions beyond its capabilities.
//
// Inspired by Open-AutoGLM's take_over mechanism, this tool enables the agent to
// gracefully hand off control to a human operator in scenarios such as:
// - Authentication/login requiring credentials
// - CAPTCHA or verification code input
// - Sensitive operations (payment, banking)
// - Ambiguous situations requiring human judgment
// - Black screen or inaccessible UI elements
//
// Unlike blocking callback approaches, this tool returns immediately with a structured
// handoff marker. The agent will naturally wait for the user's next message in the
// conversation flow.
type HumanHandoffTool struct{}

// HumanHandoffRequest contains details about why human intervention is needed.
type HumanHandoffRequest struct {
	// Reason is a brief category of why handoff is needed
	Reason string `json:"reason"`

	// Details provides specific context about the situation
	Details string `json:"details"`

	// SuggestedAction optionally suggests what the human should do
	SuggestedAction string `json:"suggested_action,omitempty"`
}

// HandoffReason defines common categories for human handoff requests
type HandoffReason string

const (
	HandoffReasonAuthentication         HandoffReason = "authentication"
	HandoffReasonLoginMethodSelection   HandoffReason = "login_method_selection"
	HandoffReasonCAPTCHA                HandoffReason = "captcha"
	HandoffReasonVerification           HandoffReason = "verification_code"
	HandoffReasonSensitive              HandoffReason = "sensitive_operation"
	HandoffReasonRedirectConfirmation   HandoffReason = "redirect_confirmation"
	HandoffReasonPermissionConfirmation HandoffReason = "permission_confirmation"
	HandoffReasonBlackScreen            HandoffReason = "black_screen"
	HandoffReasonAmbiguous              HandoffReason = "ambiguous_situation"
	HandoffReasonUnsupported            HandoffReason = "unsupported_action"
	HandoffReasonStuck                  HandoffReason = "stuck"
	HandoffReasonOther                  HandoffReason = "other"
)

var validHandoffReasonValues = []string{
	string(HandoffReasonAuthentication),
	string(HandoffReasonLoginMethodSelection),
	string(HandoffReasonCAPTCHA),
	string(HandoffReasonVerification),
	string(HandoffReasonSensitive),
	string(HandoffReasonRedirectConfirmation),
	string(HandoffReasonPermissionConfirmation),
	string(HandoffReasonBlackScreen),
	string(HandoffReasonAmbiguous),
	string(HandoffReasonUnsupported),
	string(HandoffReasonStuck),
	string(HandoffReasonOther),
}

func normalizeHandoffReason(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, valid := range validHandoffReasonValues {
		if value == valid {
			return value
		}
	}
	return string(HandoffReasonOther)
}

func isValidHandoffReason(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, valid := range validHandoffReasonValues {
		if value == valid {
			return true
		}
	}
	return false
}

// NewHumanHandoffTool creates a new HumanHandoffTool
func NewHumanHandoffTool() *HumanHandoffTool {
	return &HumanHandoffTool{}
}

func (t *HumanHandoffTool) Name() string {
	return "request_human_handoff"
}

func (t *HumanHandoffTool) Description() string {
	return `Request human intervention when you encounter a situation that requires human judgment, credentials, or actions beyond your capabilities. ` +
		`Input JSON: {"reason": "authentication", "details": "Login screen requires password", "suggested_action": "Please enter your credentials"}. ` +
		`Valid reasons: "authentication" (login/credentials required), "login_method_selection" (the user must choose a login method), ` +
		`"captcha" (CAPTCHA challenge), "verification_code" (SMS/email code needed), "sensitive_operation" (payment/banking/security), ` +
		`"redirect_confirmation" (system/app redirect confirmation dialog), "permission_confirmation" (permission dialog confirmation), ` +
		`"black_screen" (screen not visible), "ambiguous_situation" (unclear what to do), ` +
		`"unsupported_action" (action not possible with available tools), "stuck" (unable to make progress), "other" (specify in details). ` +
		`The "details" field should clearly explain the situation. The "suggested_action" field optionally tells the human what to do. ` +
		`This tool returns immediately with a structured handoff marker. Wait for the user to complete the task and tell you to continue in their next message. ` +
		`Use this when: you need credentials you don't have, encounter CAPTCHA, need verification codes, face sensitive operations requiring human approval, ` +
		`need the user to choose a login method, need a system/app redirect or permission dialog confirmed, see a black/blank screen preventing progress, ` +
		`are genuinely stuck after multiple attempts, or the task fundamentally requires human judgment. ` +
		`After the user confirms completion in their next message, take a screenshot to verify the result before continuing.`
}

func (t *HumanHandoffTool) ArgsSchema() map[string]any {
	return objectArgsSchema(map[string]any{
		"reason": stringEnumArgSchema(
			"Why human handoff is needed.",
			string(HandoffReasonAuthentication),
			string(HandoffReasonLoginMethodSelection),
			string(HandoffReasonCAPTCHA),
			string(HandoffReasonVerification),
			string(HandoffReasonSensitive),
			string(HandoffReasonRedirectConfirmation),
			string(HandoffReasonPermissionConfirmation),
			string(HandoffReasonBlackScreen),
			string(HandoffReasonAmbiguous),
			string(HandoffReasonUnsupported),
			string(HandoffReasonStuck),
			string(HandoffReasonOther),
		),
		"details":          stringArgSchema("Specific context explaining the situation."),
		"suggested_action": stringArgSchema("Optional instruction for what the human should do."),
	}, "reason", "details")
}

func (t *HumanHandoffTool) Call(ctx context.Context, input string) (string, error) {
	var args struct {
		Reason          string `json:"reason"`
		Details         string `json:"details"`
		SuggestedAction string `json:"suggested_action"`
	}

	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}

	// Validate required fields. Trim first so whitespace-only values are
	// rejected as missing rather than slipping through to the reason allowlist.
	if strings.TrimSpace(args.Reason) == "" {
		return "", fmt.Errorf("reason is required")
	}
	if strings.TrimSpace(args.Details) == "" {
		return "", fmt.Errorf("details is required")
	}

	// Normalize reason
	reason := strings.ToLower(strings.TrimSpace(args.Reason))
	if !isValidHandoffReason(reason) {
		return "", fmt.Errorf("invalid reason: %q, must be one of: %s", args.Reason, strings.Join(validHandoffReasonValues, ", "))
	}

	details := strings.TrimSpace(args.Details)
	suggestedAction := strings.TrimSpace(args.SuggestedAction)

	response := struct {
		Status          string `json:"status"`
		Reason          string `json:"reason"`
		Details         string `json:"details"`
		SuggestedAction string `json:"suggested_action,omitempty"`
	}{
		Status:          "HUMAN_HANDOFF_REQUESTED",
		Reason:          reason,
		Details:         details,
		SuggestedAction: suggestedAction,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
