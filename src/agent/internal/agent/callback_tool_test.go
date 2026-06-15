package agent

import (
	"context"
	"testing"
)

func TestCallbackToolPassesThroughVisualObservation(t *testing.T) {
	visual := &stubTool{name: "screenshot", visual: true, output: "{}"}
	wrapped := &callbackTool{inner: visual}

	v, ok := any(wrapped).(visualObservationTool)
	if !ok {
		t.Fatalf("callbackTool does not implement visualObservationTool")
	}
	if !v.ReturnsVisualObservation() {
		t.Fatalf("expected ReturnsVisualObservation() to pass through as true")
	}

	nonVisual := &stubTool{name: "shell", visual: false, output: "{}"}
	wrappedNonVisual := &callbackTool{inner: nonVisual}
	if v2, ok := any(wrappedNonVisual).(visualObservationTool); !ok || v2.ReturnsVisualObservation() {
		t.Fatalf("expected non-visual tool to remain non-visual after wrapping")
	}
}

func TestCallbackToolCallDelegates(t *testing.T) {
	inner := &stubTool{name: "x", output: "result"}
	wrapped := &callbackTool{inner: inner}
	got, err := wrapped.Call(context.Background(), "input-1")
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got != "result" {
		t.Fatalf("unexpected output: %q", got)
	}
	if len(inner.inputs) != 1 || inner.inputs[0] != "input-1" {
		t.Fatalf("inner tool not invoked with expected input: %#v", inner.inputs)
	}
}

func TestCallbackToolForwardsStructuredSchema(t *testing.T) {
	inner := &structuredStubTool{
		stubTool: stubTool{name: "structured", output: "ok"},
		schema: objectArgsSchema(map[string]any{
			"value": stringArgSchema("Value."),
		}, "value"),
	}
	wrapped := &callbackTool{inner: inner}
	props := wrapped.ArgsSchema()["properties"].(map[string]any)
	if _, ok := props["value"]; !ok {
		t.Fatalf("forwarded schema missing value: %#v", props)
	}
}
