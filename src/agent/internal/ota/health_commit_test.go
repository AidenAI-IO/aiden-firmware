package ota

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func prepareHealthCommitTest(t *testing.T) (*updaterTestEnv, *Updater, ABData) {
	t.Helper()
	env := newUpdaterTestEnv(t)
	u := env.updater()
	u.currentSlot = func() (Slot, bool, error) { return SlotB, true, nil }
	u.bootID = func() string { return "commit-boot" }
	ab := FactoryABData()
	if err := ab.SetActive(SlotB, 2, false); err != nil {
		t.Fatal(err)
	}
	if err := u.writeABData(ab); err != nil {
		t.Fatal(err)
	}
	pending := PendingBoot{TargetSlot: "b", TargetVersion: env.version, TargetBuildTime: env.buildTime, Nonce: "health-commit"}
	if err := WritePendingBoot(u.pendingPath(), pending); err != nil {
		t.Fatal(err)
	}
	if err := WriteHealthMarker(u.healthPath(), pending, "b", u.bootID()); err != nil {
		t.Fatal(err)
	}
	env.state.Phase = "pending-reboot"
	env.state.TargetVersion, env.state.TargetBuildTime = env.version, env.buildTime
	env.state.TargetSlot = SlotB
	env.state.PendingBootNonce = pending.Nonce
	env.state.DownloadedHashes = map[string]string{"boot": testHashB}
	env.saveState(t)
	return env, u, ab
}

func TestHealthCommitResumesAfterInterruptedWrite(t *testing.T) {
	for _, point := range []string{"before-write", "partial-write", "sync", "state-publication"} {
		t.Run(point, func(t *testing.T) {
			env, u, previous := prepareHealthCommitTest(t)
			interrupted := errors.New("simulated process interruption")
			originalSync := syncDir
			t.Cleanup(func() { syncDir = originalSync })
			u.writeABData = func(next ABData) error {
				backup, err := os.ReadFile(u.abCommitPath())
				if err != nil || !bytes.Equal(backup, previous.Marshal()) {
					t.Fatalf("write began without valid recovery metadata: %v", err)
				}
				switch point {
				case "before-write":
					panic(interrupted)
				case "partial-write":
					f, err := os.OpenFile(env.miscPath, os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					_, err = f.WriteAt(next.Marshal()[:16], ABMetadataOffset)
					_ = f.Close()
					if err != nil {
						t.Fatal(err)
					}
					panic(interrupted)
				default:
					if err := u.writeABDataFile(next); err != nil {
						t.Fatal(err)
					}
					if point == "sync" {
						panic(interrupted)
					}
					syncDir = func(f *os.File) error {
						state, err := LoadState(u.statePath())
						if err == nil && state.Phase == "committed" {
							panic(interrupted)
						}
						return originalSync(f)
					}
					return nil
				}
			}
			func() {
				defer func() {
					if got := recover(); got != interrupted {
						t.Fatalf("interruption = %v", got)
					}
				}()
				_ = u.ProcessPendingHealthOnce(context.Background())
			}()
			syncDir = originalSync
			if _, err := os.Stat(u.abCommitPath()); err != nil {
				t.Fatal("interrupted commit lost backup:", err)
			}
			if _, err := os.Stat(u.pendingPath()); err != nil {
				t.Fatal("interrupted commit lost pending transaction:", err)
			}
			// Use a new Updater to ensure recovery depends only on persisted data.
			restarted := env.updater()
			restarted.currentSlot = u.currentSlot
			restarted.bootID = u.bootID
			if point == "state-publication" {
				// A committed transaction needs only cleanup, even after reboot
				// when the previous boot's health marker is no longer acceptable.
				restarted.bootID = func() string { return "later-boot" }
				if err := restarted.RecoverPendingData(); err != nil {
					t.Fatal(err)
				}
			}
			if err := restarted.ProcessPendingHealthOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			state, err := LoadState(u.statePath())
			if err != nil || state.Phase != "committed" || state.CurrentVersion != env.version || state.Slots["b"].Partitions["boot"].Hash != testHashB {
				t.Fatalf("recovered state = %+v, err=%v", state, err)
			}
			ab, err := restarted.readABData()
			if err != nil || !ab.Slots[SlotB].SuccessfulBoot || ab.Slots[SlotA] != previous.Slots[SlotA] {
				t.Fatalf("recovered metadata = %+v, err=%v", ab, err)
			}
			for _, path := range []string{u.abCommitPath(), u.pendingPath()} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("recovery did not retire %s: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestHealthCommitBackupFailureDoesNotWriteMisc(t *testing.T) {
	_, u, _ := prepareHealthCommitTest(t)
	originalSync := syncDir
	syncDir = func(*os.File) error { return errors.New("backup sync failed") }
	t.Cleanup(func() { syncDir = originalSync })
	wrote := false
	u.writeABData = func(ABData) error { wrote = true; return nil }
	if err := u.ProcessPendingHealthOnce(context.Background()); err == nil || wrote {
		t.Fatalf("err=%v, wrote misc=%v", err, wrote)
	}
}

func TestHealthCommitRecoveryKeepsBackupOnIOFailure(t *testing.T) {
	_, u, previous := prepareHealthCommitTest(t)
	if err := u.prepareABCommit(previous); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(u.miscPathForAccess(), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("bad!"), ABMetadataOffset)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	u.writeABData = func(ABData) error { return errors.New("misc sync failed") }
	if err := u.RecoverPendingData(); err == nil {
		t.Fatal("recovery succeeded despite I/O failure")
	}
	if _, err := os.Stat(u.abCommitPath()); err != nil {
		t.Fatal("failed recovery discarded backup:", err)
	}
}

func TestHealthCommitRecoveryPreservesBootloaderChanges(t *testing.T) {
	_, u, previous := prepareHealthCommitTest(t)
	if err := u.prepareABCommit(previous); err != nil {
		t.Fatal(err)
	}
	current := previous
	current.Slots[SlotB].TriesRemaining--
	current.LastBoot = SlotB
	if err := u.writeABData(current); err != nil {
		t.Fatal(err)
	}
	if err := u.RecoverPendingData(); err != nil {
		t.Fatal(err)
	}
	if err := u.verifyABCommit(current); err != nil {
		t.Fatal("recovery undid bootloader changes:", err)
	}
}

func TestHealthCommitRecoveryRejectsCorruptBackup(t *testing.T) {
	_, u, previous := prepareHealthCommitTest(t)
	if err := os.WriteFile(u.abCommitPath(), []byte("corrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := u.RecoverPendingData(); err == nil {
		t.Fatal("accepted invalid recovery metadata")
	}
	if err := u.verifyABCommit(previous); err != nil {
		t.Fatal("invalid backup changed misc:", err)
	}
	if _, err := os.Stat(u.abCommitPath()); err != nil {
		t.Fatal("invalid backup was discarded:", err)
	}
}

func TestRollbackRecoversTornHealthCommitFirst(t *testing.T) {
	env, u, previous := prepareHealthCommitTest(t)
	if err := u.prepareABCommit(previous); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(env.miscPath, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("bad!"), ABMetadataOffset)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Rollback("manual recovery"); err != nil {
		t.Fatal(err)
	}
	ab, err := u.readABData()
	if err != nil {
		t.Fatal(err)
	}
	active, ok := ab.ActiveSlot()
	if !ok || active != SlotA || env.reboots != 1 {
		t.Fatalf("rollback active=%v, ok=%v, reboots=%d", active, ok, env.reboots)
	}
}
