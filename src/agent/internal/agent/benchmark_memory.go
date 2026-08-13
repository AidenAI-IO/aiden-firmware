package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

const BenchmarkMemoryScopeHeader = "benchmark-memory-scope"

type benchmarkMemoryScopeContextKey struct{}

func WithBenchmarkMemoryScope(ctx context.Context, scope string) context.Context {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return ctx
	}
	return context.WithValue(ctx, benchmarkMemoryScopeContextKey{}, scope)
}

func BenchmarkMemoryScopeFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	scope, _ := ctx.Value(benchmarkMemoryScopeContextKey{}).(string)
	return strings.TrimSpace(scope)
}

func benchmarkMemoryScopeDir(memoryDir, scope string) string {
	scope = strings.TrimSpace(scope)
	if strings.TrimSpace(memoryDir) == "" || scope == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(scope))
	return filepath.Join(memoryDir, "benchmark", hex.EncodeToString(sum[:16]))
}

func clearBenchmarkMemoryScope(memoryDir, scope string) error {
	dir := benchmarkMemoryScopeDir(memoryDir, scope)
	if dir == "" {
		return nil
	}
	return os.RemoveAll(dir)
}
