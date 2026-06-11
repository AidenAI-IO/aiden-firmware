package agent

import (
	"testing"
)

func TestParseEnterPlanMetaToolCall(t *testing.T) {
	parser := &FunctionAgent{Tools: appendLoopMetaTools(nil), OutputKey: "output"}
	actions, finish, err := parser.ParseOutput(enterPlanModeToolCall())
	if err != nil {
		t.Fatalf("ParseOutput() error = %v", err)
	}
	if finish != nil {
		t.Fatalf("unexpected finish: %#v", finish)
	}
	if len(actions) != 1 {
		t.Fatalf("actions = %#v", actions)
	}
	if !isLoopMetaTool(actions[0].Tool) {
		t.Fatalf("tool %q is not meta", actions[0].Tool)
	}
}
