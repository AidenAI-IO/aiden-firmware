package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const BenchmarkMemoryScopeHeader = "benchmark-memory-scope"

type benchmarkMemoryScopeContextKey struct{}

const benchmarkMemoryScopeClosingMessage = "benchmark memory scope is being cleared"

type benchmarkMemoryScopeActivity struct {
	active   int
	closing  bool
	done     chan struct{}
	cleared  chan struct{}
	clearErr error
	nextID   uint64
	cancels  map[uint64]context.CancelFunc
}

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

func (s *Server) beginBenchmarkMemoryScopeActivity(scope string, cancel context.CancelFunc) (func(), bool) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return func() {}, true
	}

	s.benchmarkMemoryScopesMu.Lock()
	if s.benchmarkMemoryScopes == nil {
		s.benchmarkMemoryScopes = make(map[string]*benchmarkMemoryScopeActivity)
	}
	activity := s.benchmarkMemoryScopes[scope]
	if activity == nil {
		activity = &benchmarkMemoryScopeActivity{
			done:    make(chan struct{}),
			cleared: make(chan struct{}),
			cancels: make(map[uint64]context.CancelFunc),
		}
		s.benchmarkMemoryScopes[scope] = activity
	}
	if activity.closing {
		s.benchmarkMemoryScopesMu.Unlock()
		return nil, false
	}
	activity.active++
	activity.nextID++
	id := activity.nextID
	if cancel != nil {
		activity.cancels[id] = cancel
	}
	s.benchmarkMemoryScopesMu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			s.finishBenchmarkMemoryScopeActivity(scope, activity, id)
		})
	}, true
}

func (s *Server) finishBenchmarkMemoryScopeActivity(scope string, activity *benchmarkMemoryScopeActivity, id uint64) {
	s.benchmarkMemoryScopesMu.Lock()
	defer s.benchmarkMemoryScopesMu.Unlock()
	if s.benchmarkMemoryScopes[scope] != activity {
		return
	}
	delete(activity.cancels, id)
	if activity.active > 0 {
		activity.active--
	}
	if activity.closing && activity.active == 0 {
		close(activity.done)
	}
}

func (s *Server) clearBenchmarkMemoryScopeAfterActivities(memoryDir, scope string) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return nil
	}

	s.benchmarkMemoryScopesMu.Lock()
	if s.benchmarkMemoryScopes == nil {
		s.benchmarkMemoryScopes = make(map[string]*benchmarkMemoryScopeActivity)
	}
	activity := s.benchmarkMemoryScopes[scope]
	if activity == nil {
		activity = &benchmarkMemoryScopeActivity{
			done:    make(chan struct{}),
			cleared: make(chan struct{}),
			cancels: make(map[uint64]context.CancelFunc),
		}
		s.benchmarkMemoryScopes[scope] = activity
	}
	if activity.closing {
		cleared := activity.cleared
		s.benchmarkMemoryScopesMu.Unlock()
		<-cleared
		return activity.clearErr
	}
	activity.closing = true
	if activity.active == 0 {
		close(activity.done)
	}
	cancels := make([]context.CancelFunc, 0, len(activity.cancels))
	for _, cancel := range activity.cancels {
		cancels = append(cancels, cancel)
	}
	done := activity.done
	s.benchmarkMemoryScopesMu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	<-done
	err := clearBenchmarkMemoryScope(memoryDir, scope)

	s.benchmarkMemoryScopesMu.Lock()
	activity.clearErr = err
	close(activity.cleared)
	if s.benchmarkMemoryScopes[scope] == activity {
		delete(s.benchmarkMemoryScopes, scope)
	}
	s.benchmarkMemoryScopesMu.Unlock()
	return err
}
