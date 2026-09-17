package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"aiden-agent/internal/backup"
)

func TestVerifyPlainArchiveNeedsNoPassword(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "agent/memory"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent/memory/profile.md"), []byte("saved data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := backup.NewPlanner(backup.Roots{Userdata: root, SD: filepath.Join(root, "sd")}).Plan(context.Background(), backup.PlanOptions{
		Mode: backup.ModeSameDevice, Components: []backup.ComponentID{backup.ComponentAgentMemory},
		Source: backup.SourceIdentity{HardwareID: "board-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	material := backup.NewPlainMaterial(plan.Manifest.CreatedAt)
	name := filepath.Join(root, "backup.aiden-backup")
	file, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.WriteArchive(context.Background(), file, plan, material, nil); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := run([]string{"--json", "verify", name}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"ok": true`)) && !bytes.Contains(out.Bytes(), []byte(`"ok":true`)) {
		t.Fatalf("output=%s", out.String())
	}
	if bytes.Contains(errOut.Bytes(), []byte("passphrase")) {
		t.Fatal("unexpected password prompt")
	}
}
