package netproxy

import (
	"strings"
	"testing"
)

func TestParseRejectsDuplicateScheme(t *testing.T) {
	_, err := Parse("http://http://proxy.example:7893", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want duplicate scheme error")
	}
	if !strings.Contains(err.Error(), "duplicate scheme") {
		t.Fatalf("Parse() error = %v, want duplicate scheme", err)
	}
}

func TestParseRejectsEmbeddedProxyAssignmentWithNonBreakingSpace(t *testing.T) {
	_, err := Parse("http://proxy.example:7893\u00a0http_proxy=http://proxy.example:7893", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want whitespace error")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("Parse() error = %v, want whitespace", err)
	}
}

func TestParseRejectsQuotedMultipleAssignments(t *testing.T) {
	_, err := Parse("http://proxy.example:7893 http_proxy=http://proxy.example:7893", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want whitespace error")
	}
	if !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("Parse() error = %v, want whitespace", err)
	}
}

func TestParseRejectsMissingHost(t *testing.T) {
	_, err := Parse("http://", "http", "https", "socks5")
	if err == nil {
		t.Fatal("Parse() error = nil, want missing host error")
	}
	if !strings.Contains(err.Error(), "expected absolute proxy URL") {
		t.Fatalf("Parse() error = %v, want absolute URL guidance", err)
	}
}
