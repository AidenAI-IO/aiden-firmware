package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeviceMemoryAppProfilePathUsesASCIIID(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "device")
	store := NewDeviceMemoryStore(root)

	apps := []string{"微信", "支付宝"}
	for _, app := range apps {
		id := "app_" + stableMemoryID(app)
		if _, err := store.Upsert(ctx, DeviceMemoryItem{
			ID:     id,
			Type:   "app_profile",
			Status: "active",
			AppID:  app,
		}); err != nil {
			t.Fatalf("Upsert(%s): %v", app, err)
		}
		path := filepath.Join(root, "apps", id+".yaml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("expected ASCII app profile path %s: %v", path, err)
		}
		if !strings.Contains(string(data), "app_id: "+app) {
			t.Fatalf("app profile should preserve original app_id in YAML:\n%s", data)
		}
	}

	files, err := filepath.Glob(filepath.Join(root, "apps", "*.yaml"))
	if err != nil {
		t.Fatalf("glob app profiles: %v", err)
	}
	if len(files) != len(apps) {
		t.Fatalf("files=%#v, want %d", files, len(apps))
	}
	for _, path := range files {
		name := filepath.Base(path)
		if strings.HasPrefix(name, "_") {
			t.Fatalf("app profile path should not be underscore-derived: %s", name)
		}
	}
}

func TestDeviceMemorySearchReflectsUpsertAfterCache(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(filepath.Join(t.TempDir(), "device"))
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:       "proc_old",
		Type:     "procedure",
		Status:   "active",
		Title:    "Old procedure",
		Content:  "old path",
		Tags:     []string{"cache-test"},
		Priority: 10,
	}); err != nil {
		t.Fatalf("Upsert(old): %v", err)
	}
	if _, err := store.Search(ctx, DeviceMemoryQuery{Tags: []string{"cache-test"}, Limit: 10}); err != nil {
		t.Fatalf("initial Search(): %v", err)
	}

	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID:       "proc_new",
		Type:     "procedure",
		Status:   "active",
		Title:    "New procedure",
		Content:  "new path",
		Tags:     []string{"cache-test"},
		Priority: 90,
	}); err != nil {
		t.Fatalf("Upsert(new): %v", err)
	}

	hits, err := store.Search(ctx, DeviceMemoryQuery{Tags: []string{"cache-test"}, Limit: 10})
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(hits) == 0 || hits[0].ID != "proc_new" {
		t.Fatalf("Search() returned stale hits: %#v", hits)
	}
}

func TestDeviceMemorySearchTreatsLegacyMissingStatusAsActive(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "device")
	path := filepath.Join(root, "procedures", "legacy.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(): %v", err)
	}
	if err := os.WriteFile(path, []byte("id: legacy\ntype: procedure\ntitle: Legacy path\ncontent: open legacy settings\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(): %v", err)
	}

	hits, err := NewDeviceMemoryStore(root).Search(ctx, DeviceMemoryQuery{Terms: []string{"legacy"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "legacy" {
		t.Fatalf("legacy memory with omitted status was hidden: %#v", hits)
	}
}

func TestDeviceMemorySearchRetainsAliasOnlyEntityMatch(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(filepath.Join(t.TempDir(), "device"))
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID: "alias_only", Type: "procedure", Status: deviceMemoryStatusActive,
		Title: "Verified route", Aliases: []string{"QA Notes"},
	}); err != nil {
		t.Fatalf("Upsert(): %v", err)
	}
	hits, err := store.Search(ctx, DeviceMemoryQuery{Entities: []string{"QA Notes"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "alias_only" {
		t.Fatalf("alias-only Search() hits = %#v", hits)
	}
}

func TestDeviceMemorySearchRetainsAppIDOnlyEntityMatch(t *testing.T) {
	ctx := context.Background()
	store := NewDeviceMemoryStore(filepath.Join(t.TempDir(), "device"))
	if _, err := store.Upsert(ctx, DeviceMemoryItem{
		ID: "app_id_only", Type: "app_profile", Status: deviceMemoryStatusActive,
		AppID: "com.example.qa-notes",
	}); err != nil {
		t.Fatalf("Upsert(): %v", err)
	}
	hits, err := store.Search(ctx, DeviceMemoryQuery{Entities: []string{"com.example.qa-notes"}, Limit: 5})
	if err != nil {
		t.Fatalf("Search(): %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "app_id_only" {
		t.Fatalf("AppID-only Search() hits = %#v", hits)
	}
}
