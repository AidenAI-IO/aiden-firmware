package agent

import (
	"path/filepath"
	"testing"
)

func TestRuntimeMemorySubsystemsShareLongTermStore(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	store := NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")))
	manager := NewMemoryManager(memoryDir, WithLongTermMemoryStore(store))
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, store)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)
	plane, ok := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if !ok {
		t.Fatalf("memory plane type = %T, want *FilesystemMemoryPlane", runtime.memoryPlane)
	}
	if plane.LongTerm() != store {
		t.Fatal("memory plane did not reuse the shared long-term store")
	}
	if manager.longTerm != store {
		t.Fatal("memory manager did not retain the shared long-term store")
	}

	recall, ok := tools.tools["recall_memory"].(*RecallMemoryTool)
	if !ok {
		t.Fatalf("recall_memory tool type = %T, want *RecallMemoryTool", tools.tools["recall_memory"])
	}
	if recall.store != store {
		t.Fatal("memory tools did not reuse the shared long-term store")
	}
}

func TestRuntimeMemorySubsystemsCreateSharedLongTermStoreFallback(t *testing.T) {
	configDir := t.TempDir()
	memoryDir := filepath.Join(configDir, "memory")
	manager := NewMemoryManager(memoryDir)
	tools := NewBuiltinToolSet(HIDConfig{}, AudioConfig{}, SearchConfig{}, ProxyConfig{})
	tools.RegisterMemoryTools(memoryDir, 0, nil)

	runtime := NewRuntimeWithDeps(Config{ConfigDir: configDir}, nil, manager, tools, nil)
	plane, ok := runtime.memoryPlane.(*FilesystemMemoryPlane)
	if !ok {
		t.Fatalf("memory plane type = %T, want *FilesystemMemoryPlane", runtime.memoryPlane)
	}
	store := manager.longTerm
	if store == nil {
		t.Fatal("memory manager long-term store was not initialized")
	}
	if plane.LongTerm() != store {
		t.Fatal("memory plane did not reuse the fallback long-term store")
	}

	recall, ok := tools.tools["recall_memory"].(*RecallMemoryTool)
	if !ok {
		t.Fatalf("recall_memory tool type = %T, want *RecallMemoryTool", tools.tools["recall_memory"])
	}
	if recall.store != store {
		t.Fatal("memory tools did not reuse the fallback long-term store")
	}
}
