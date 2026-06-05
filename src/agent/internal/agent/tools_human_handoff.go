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
// Unlike blocking callback approaches, this tool returns immediately with instructions
// for the agent to communicate to the user. The agent will naturally wait for the user's
// next message in the conversation flow.
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
	HandoffReasonAuthentication HandoffReason = "authentication"
	HandoffReasonCAPTCHA        HandoffReason = "captcha"
	HandoffReasonVerification   HandoffReason = "verification_code"
	HandoffReasonSensitive      HandoffReason = "sensitive_operation"
	HandoffReasonBlackScreen    HandoffReason = "black_screen"
	HandoffReasonAmbiguous      HandoffReason = "ambiguous_situation"
	HandoffReasonUnsupported    HandoffReason = "unsupported_action"
	HandoffReasonStuck          HandoffReason = "stuck"
	HandoffReasonOther          HandoffReason = "other"
)

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
		`Valid reasons: "authentication" (login/credentials required), "captcha" (CAPTCHA challenge), "verification_code" (SMS/email code needed), ` +
		`"sensitive_operation" (payment/banking/security), "black_screen" (screen not visible), "ambiguous_situation" (unclear what to do), ` +
		`"unsupported_action" (action not possible with available tools), "stuck" (unable to make progress), "other" (specify in details). ` +
		`The "details" field should clearly explain the situation. The "suggested_action" field optionally tells the human what to do. ` +
		`This tool returns immediately with instructions for you to communicate to the user. Wait for the user to complete the task and tell you to continue in their next message. ` +
		`Use this when: you need credentials you don't have, encounter CAPTCHA, need verification codes, face sensitive operations requiring human approval, ` +
		`see a black/blank screen preventing progress, are genuinely stuck after multiple attempts, or the task fundamentally requires human judgment. ` +
		`After the user confirms completion in their next message, take a screenshot to verify the result before continuing.`
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
	validReasons := map[string]bool{
		"authentication":      true,
		"captcha":             true,
		"verification_code":   true,
		"sensitive_operation": true,
		"black_screen":        true,
		"ambiguous_situation": true,
		"unsupported_action":  true,
		"stuck":               true,
		"other":               true,
	}
	if !validReasons[reason] {
		return "", fmt.Errorf("invalid reason: %q, must be one of: authentication, captcha, verification_code, sensitive_operation, black_screen, ambiguous_situation, unsupported_action, stuck, other", args.Reason)
	}

	details := strings.TrimSpace(args.Details)
	suggestedAction := strings.TrimSpace(args.SuggestedAction)

	// Build response message for the agent to communicate to the user
	var response strings.Builder
	response.WriteString("HUMAN_HANDOFF_REQUESTED. You need to ask the user to perform a manual action. ")

	// Add context about why handoff is needed
	response.WriteString(fmt.Sprintf("Reason: %s. ", details))

	// Add suggested action if provided
	if suggestedAction != "" {
		response.WriteString(fmt.Sprintf("Tell the user: \"%s\" ", suggestedAction))
	} else {
		// Provide default instruction based on reason
		switch reason {
		case "authentication":
			response.WriteString("Tell the user to enter their credentials. ")
		case "captcha", "verification_code":
			response.WriteString("Tell the user to complete the verification. ")
		case "sensitive_operation":
			response.WriteString("Tell the user to review and confirm the sensitive operation. ")
		case "black_screen":
			response.WriteString("Tell the user to manually complete the action on the screen. ")
		default:
			response.WriteString("Tell the user to handle this situation manually. ")
		}
	}

	response.WriteString("Then ask the user to tell you when they have completed the task (e.g., say 'done', 'continue', or 'finished'). ")
	response.WriteString("After the user confirms, take a screenshot to verify the current state before proceeding.")

	return response.String(), nil
}
