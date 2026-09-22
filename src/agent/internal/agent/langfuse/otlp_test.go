package langfuse

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// otlpTestServer captures the request the client sends and replies with the
// given status and body. The OTLP response body is optional, so tests that do
// not care about it can return an empty body.
func otlpTestServer(t *testing.T, status int, body string) (*httptest.Server, *http.Request, *[]byte) {
	t.Helper()
	captured := &[]byte{}
	request := &http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*request = *r.Clone(context.Background())
		data, _ := io.ReadAll(r.Body)
		*captured = data
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, request, captured
}

func decodeOTLPRequest(t *testing.T, body []byte) otlpRequest {
	t.Helper()
	var payload otlpRequest
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode otlp request: %v", err)
	}
	return payload
}

func firstOTLPSpan(t *testing.T, payload otlpRequest) otlpSpan {
	t.Helper()
	if len(payload.ResourceSpans) != 1 {
		t.Fatalf("resourceSpans count = %d, want 1", len(payload.ResourceSpans))
	}
	spans := payload.ResourceSpans[0].ScopeSpans
	if len(spans) != 1 || len(spans[0].Spans) != 1 {
		t.Fatalf("scope spans = %#v, want one span", spans)
	}
	return spans[0].Spans[0]
}

func attributeMap(attributes []otlpAttribute) map[string]otlpValue {
	out := map[string]otlpValue{}
	for _, attribute := range attributes {
		out[attribute.Key] = attribute.Value
	}
	return out
}

func TestExportSpansSendsOTLPRequest(t *testing.T) {
	server, request, captured := otlpTestServer(t, http.StatusOK, "")
	client := NewClient(Config{BaseURL: server.URL, PublicKey: "pk-test", SecretKey: "sk-test"})

	err := client.ExportSpans(context.Background(), []Span{{
		TraceID:   strings.Repeat("a", 32),
		SpanID:    strings.Repeat("b", 16),
		Name:      "agent-run",
		Type:      TypeAgent,
		StartTime: time.Unix(0, 0).UTC(),
		EndTime:   time.Unix(0, int64(time.Millisecond)).UTC(),
	}})
	if err != nil {
		t.Fatalf("ExportSpans() error = %v", err)
	}

	if request.URL.Path != tracesPath {
		t.Fatalf("request path = %q, want %q", request.URL.Path, tracesPath)
	}
	if got := request.Header.Get(ingestionVersionHeader); got != ingestionVersionV4 {
		t.Fatalf("%s = %q, want %q", ingestionVersionHeader, got, ingestionVersionV4)
	}
	if user, pass, ok := request.BasicAuth(); !ok || user != "pk-test" || pass != "sk-test" {
		t.Fatalf("basic auth = (%q, %q, %v), want pk-test/sk-test", user, pass, ok)
	}

	payload := decodeOTLPRequest(t, *captured)
	resourceAttributes := attributeMap(payload.ResourceSpans[0].Resource.Attributes)
	if resourceAttributes["service.name"].StringValue != serviceName {
		t.Fatalf("service.name = %q, want %q", resourceAttributes["service.name"].StringValue, serviceName)
	}
	if name := payload.ResourceSpans[0].ScopeSpans[0].Scope.Name; name != scopeName {
		t.Fatalf("scope name = %q, want %q", name, scopeName)
	}
	span := firstOTLPSpan(t, payload)
	if span.TraceID != strings.Repeat("a", 32) || span.SpanID != strings.Repeat("b", 16) {
		t.Fatalf("span ids = %q/%q", span.TraceID, span.SpanID)
	}
	if span.Kind != 1 {
		t.Fatalf("span kind = %d, want 1", span.Kind)
	}
}

func TestExportSpansEncodesAttributeTypes(t *testing.T) {
	server, _, captured := otlpTestServer(t, http.StatusOK, "")
	client := NewClient(Config{BaseURL: server.URL, PublicKey: "pk-test", SecretKey: "sk-test"})

	err := client.ExportSpans(context.Background(), []Span{{
		TraceID: strings.Repeat("a", 32),
		SpanID:  strings.Repeat("b", 16),
		Name:    "agent-run",
		Type:    TypeSpan,
		Trace: TraceContext{
			Name: "trace-name",
			Tags: []string{"alpha", "beta"},
			Metadata: map[string]interface{}{
				"empty":  "",
				"":       "blank-key",
				"flag":   true,
				"count":  7,
				"ratio":  0.5,
				"nested": map[string]interface{}{"key": "value"},
				"list":   []string{"x", "y"},
			},
		},
		Metadata: map[string]interface{}{
			"empty":  "",
			"number": float64(3),
		},
	}})
	if err != nil {
		t.Fatalf("ExportSpans() error = %v", err)
	}

	span := firstOTLPSpan(t, decodeOTLPRequest(t, *captured))
	attributes := attributeMap(span.Attributes)

	if _, ok := attributes["langfuse.trace.metadata.empty"]; ok {
		t.Fatal("empty metadata value was not skipped")
	}
	if _, ok := attributes["langfuse.observation.metadata.empty"]; ok {
		t.Fatal("empty observation metadata value was not skipped")
	}
	if _, ok := attributes["langfuse.trace.metadata."]; ok {
		t.Fatal("blank metadata key was not skipped")
	}

	flag := attributes["langfuse.trace.metadata.flag"]
	if flag.BoolValue == nil || !*flag.BoolValue {
		t.Fatalf("bool metadata = %#v, want true", flag)
	}
	count := attributes["langfuse.trace.metadata.count"]
	// Integers are encoded as JSON numbers: Langfuse drops observations whose
	// intValue arrives as a string, even though OTLP/JSON normally encodes int64
	// fields as strings.
	if count.IntValue == nil || *count.IntValue != 7 {
		t.Fatalf("int metadata = %#v, want 7", count)
	}
	ratio := attributes["langfuse.trace.metadata.ratio"]
	if ratio.DoubleValue == nil || *ratio.DoubleValue != 0.5 {
		t.Fatalf("float metadata = %#v, want 0.5", ratio)
	}
	nested := attributes["langfuse.trace.metadata.nested"]
	if nested.StringValue != `{"key":"value"}` {
		t.Fatalf("nested metadata = %q, want JSON string", nested.StringValue)
	}
	list := attributes["langfuse.trace.metadata.list"]
	if list.StringValue != `["x","y"]` {
		t.Fatalf("array metadata = %q, want JSON string", list.StringValue)
	}
	observationNumber := attributes["langfuse.observation.metadata.number"]
	if observationNumber.DoubleValue == nil || *observationNumber.DoubleValue != 3 {
		t.Fatalf("observation metadata number = %#v, want 3", observationNumber)
	}

	tags, ok := attributes["langfuse.trace.tags"]
	if !ok || tags.ArrayValue == nil {
		t.Fatalf("trace tags attribute = %#v, want arrayValue", tags)
	}
	values := tags.ArrayValue.Values
	if len(values) != 2 || values[0].StringValue != "alpha" || values[1].StringValue != "beta" {
		t.Fatalf("trace tag values = %#v, want [alpha beta]", values)
	}
	if attributes["langfuse.trace.name"].StringValue != "trace-name" {
		t.Fatalf("trace name = %q, want trace-name", attributes["langfuse.trace.name"].StringValue)
	}
}

func TestExportSpansSkipsEmptyTraceContext(t *testing.T) {
	server, _, captured := otlpTestServer(t, http.StatusOK, "")
	client := NewClient(Config{BaseURL: server.URL, PublicKey: "pk-test", SecretKey: "sk-test"})

	if err := client.ExportSpans(context.Background(), []Span{{Name: "agent-run", TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16)}}); err != nil {
		t.Fatalf("ExportSpans() error = %v", err)
	}

	span := firstOTLPSpan(t, decodeOTLPRequest(t, *captured))
	for _, key := range []string{"langfuse.trace.name", "langfuse.user.id", "langfuse.session.id", "langfuse.release", "langfuse.version", "langfuse.environment", "langfuse.trace.tags"} {
		for _, attribute := range span.Attributes {
			if attribute.Key == key {
				t.Fatalf("empty trace context emitted %s", key)
			}
		}
	}
}

func TestExportSpansSurfacesRejectedSpans(t *testing.T) {
	server, _, _ := otlpTestServer(t, http.StatusOK, `{"partialSuccess":{"rejectedSpans":2,"errorMessage":"bad span"}}`)
	client := NewClient(Config{BaseURL: server.URL, PublicKey: "pk-test", SecretKey: "sk-test"})

	err := client.ExportSpans(context.Background(), []Span{{Name: "agent-run", TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16)}})
	var rejected *RejectedSpansError
	if !errors.As(err, &rejected) {
		t.Fatalf("ExportSpans() error = %v, want RejectedSpansError", err)
	}
	if rejected.Count != 2 || !strings.Contains(rejected.Error(), "bad span") {
		t.Fatalf("RejectedSpansError = %#v", rejected)
	}
}

func TestExportSpansRequiresConfiguration(t *testing.T) {
	client := NewClient(Config{})
	err := client.ExportSpans(context.Background(), []Span{{Name: "agent-run"}})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ExportSpans() error = %v, want not configured", err)
	}
}
