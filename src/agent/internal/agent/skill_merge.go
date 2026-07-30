package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type SkillMergeMode string

const (
	MergeThreeWay SkillMergeMode = "three_way"
	MergeTwoWay   SkillMergeMode = "two_way_no_base"
)

type SkillMergeInput struct {
	Mode      SkillMergeMode
	SkillName string
	Base      string // empty for two-way
	Upstream  string
	Local     string
}

type SkillMergeResult struct {
	Status        string // "merged" or "failed"
	MergedSkillMD string
	Summary       string
}

type SkillMergeModel interface {
	MergeSkill(ctx context.Context, input SkillMergeInput) (*SkillMergeResult, error)
}

type SkillMergeJob struct {
	SkillName    string
	Mode         SkillMergeMode
	Base         string
	Upstream     string
	Local        string
	LocalHash    string
	UpstreamHash string
	BaseHash     string
	MergeKey     string
	UserPath     string
	BasePath     string
	StateDir     string
}

const MaxSkillSize = 32 * 1024

type MergeWorker struct {
	model        SkillMergeModel
	mu           sync.Mutex
	jobs         []SkillMergeJob
	cancel       context.CancelFunc
	done         chan struct{}
	manifestPath string
	onSuccess    func(SkillMergeJob)
}

func NewMergeWorker(model SkillMergeModel, manifestPath string) *MergeWorker {
	return &MergeWorker{
		model:        model,
		done:         make(chan struct{}),
		manifestPath: manifestPath,
	}
}

func (w *MergeWorker) Enqueue(jobs []SkillMergeJob) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.jobs = append(w.jobs, jobs...)
}

func (w *MergeWorker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	go w.run(ctx)
}

func (w *MergeWorker) Stop() {
	if w.cancel == nil {
		return
	}
	w.cancel()
	<-w.done
}

func (w *MergeWorker) Wait() {
	<-w.done
}

func (w *MergeWorker) run(ctx context.Context) {
	defer close(w.done)

	w.mu.Lock()
	jobs := make([]SkillMergeJob, len(w.jobs))
	copy(jobs, w.jobs)
	w.mu.Unlock()

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].SkillName < jobs[j].SkillName
	})

	for _, job := range jobs {
		if ctx.Err() != nil {
			return
		}
		w.processJob(ctx, job)
	}
}

func (w *MergeWorker) processJob(ctx context.Context, job SkillMergeJob) {
	result, err := w.model.MergeSkill(ctx, SkillMergeInput{
		Mode:      job.Mode,
		SkillName: job.SkillName,
		Base:      job.Base,
		Upstream:  job.Upstream,
		Local:     job.Local,
	})
	if err != nil {
		log.Printf("[skill_merge] LLM call failed for %s: %v", job.SkillName, err)
		w.recordFailure(job, err.Error())
		return
	}

	if !mergeResultOK(result, job.SkillName) {
		log.Printf("[skill_merge] validation failed for %s: %s", job.SkillName, result.Summary)
		w.recordFailure(job, result.Summary)
		return
	}

	// Write to temp, then move
	tmpPath := filepath.Join(job.StateDir, "tmp", job.SkillName+".candidate.SKILL.md")
	if err := os.MkdirAll(filepath.Dir(tmpPath), 0o755); err != nil {
		log.Printf("[skill_merge] mkdir tmp: %v", err)
		w.recordFailure(job, err.Error())
		return
	}
	if err := os.WriteFile(tmpPath, []byte(result.MergedSkillMD), 0o644); err != nil {
		log.Printf("[skill_merge] write tmp: %v", err)
		w.recordFailure(job, err.Error())
		return
	}
	defer os.Remove(tmpPath)

	skillFileMu.Lock()
	currentContent, err := os.ReadFile(job.UserPath)
	if err != nil {
		skillFileMu.Unlock()
		log.Printf("[skill_merge] %s: read local before apply failed: %v", job.SkillName, err)
		w.recordFailure(job, err.Error())
		return
	}
	if hashContent(currentContent) != job.LocalHash {
		skillFileMu.Unlock()
		log.Printf("[skill_merge] %s: local changed during merge, skipping apply", job.SkillName)
		w.recordFailure(job, "local changed before apply")
		return
	}

	if err := os.MkdirAll(filepath.Dir(job.UserPath), 0o755); err != nil {
		skillFileMu.Unlock()
		w.recordFailure(job, err.Error())
		return
	}
	if err := moveFile(tmpPath, job.UserPath); err != nil {
		skillFileMu.Unlock()
		log.Printf("[skill_merge] move: %v", err)
		w.recordFailure(job, err.Error())
		return
	}
	skillFileMu.Unlock()

	if err := saveBase(job.BasePath, []byte(job.Upstream)); err != nil {
		log.Printf("[skill_merge] write base: %v", err)
		w.recordFailure(job, err.Error())
		return
	}

	// Update manifest
	manifest := loadManifest(w.manifestPath)
	manifest.Skills[job.SkillName] = ManifestEntry{
		OriginHash:    job.UpstreamHash,
		EffectiveHash: hashContent([]byte(result.MergedSkillMD)),
		Status:        StatusMerged,
		BasePath:      job.BasePath,
		LastSyncedAt:  time.Now().Format(time.RFC3339),
		LastMergedAt:  time.Now().Format(time.RFC3339),
		LastMergedKey: job.MergeKey,
	}
	manifest.UpdatedAt = time.Now().Format(time.RFC3339)
	saveManifest(w.manifestPath, manifest)
	if w.onSuccess != nil {
		w.onSuccess(job)
	}
	log.Printf("[skill_merge] %s merged successfully", job.SkillName)
}

func (w *MergeWorker) recordFailure(job SkillMergeJob, errMsg string) {
	manifest := loadManifest(w.manifestPath)
	entry := manifest.Skills[job.SkillName]
	if entry.OriginHash == "" && job.BaseHash != "" {
		entry.OriginHash = job.BaseHash
	}
	if entry.BasePath == "" {
		entry.BasePath = job.BasePath
	}
	entry.Status = StatusUserModified
	entry.LastFailedMergeKey = job.MergeKey
	entry.LastMergeFailedAt = time.Now().Format(time.RFC3339)
	entry.LastMergeError = truncate(errMsg, 200)
	manifest.Skills[job.SkillName] = entry
	manifest.UpdatedAt = time.Now().Format(time.RFC3339)
	saveManifest(w.manifestPath, manifest)
}

func mergeResultOK(result *SkillMergeResult, expectedName string) bool {
	if result.Status != "merged" {
		return false
	}
	md := result.MergedSkillMD
	if strings.TrimSpace(md) == "" {
		return false
	}
	if len(md) > MaxSkillSize {
		return false
	}
	skill, err := parseSkillFromContent(md)
	if err != nil {
		return false
	}
	if skill.Name != expectedName {
		return false
	}
	if strings.TrimSpace(skill.Description) == "" {
		return false
	}
	if strings.TrimSpace(skill.Instructions) == "" {
		return false
	}
	if !allowedToolsExist(skill.AllowedTools) {
		return false
	}
	return true
}

func allowedToolsExist(tools []string) bool {
	for _, tool := range tools {
		if strings.HasPrefix(tool, "delegate_") {
			continue
		}
		if _, ok := knownToolNames[tool]; !ok {
			return false
		}
	}
	return true
}

var knownToolNames = map[string]struct{}{
	"audio_volume":           {},
	"enter_text":             {},
	"forget_memory":          {},
	"image_diff":             {},
	"keyboard_tap":           {},
	"list_scripts":           {},
	"mouse_click":            {},
	"mouse_move":             {},
	"mouse_scroll":           {},
	toolBridgeOpenApp:        {},
	"quick_action":           {},
	"recall_memory":          {},
	"recall_session_chunks":  {},
	"read_script":            {},
	"request_human_handoff":  {},
	"run_script":             {},
	"save_memory":            {},
	"search_launch_app":      {},
	"screenshot":             {},
	"shell":                  {},
	"skill_list":             {},
	"skill_mark_used":        {},
	"skill_manage":           {},
	"skill_read":             {},
	"touch_gesture":          {},
	"wait_for_stable_screen": {},
	"wheel_nudge":            {},
	"weather":                {},
	"web_scraper":            {},
	"web_search":             {},
	"wikipedia":              {},
	"write_script":           {},
}

func computeMergeKey(mode SkillMergeMode, skillName, baseHash, upstreamHash, localHash string) string {
	data := fmt.Sprintf("%s|%s|%s|%s|%s", mode, skillName, baseHash, upstreamHash, localHash)
	h := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(h[:8])
}

func truncate(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max])
}

func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// Fallback for cross-filesystem moves
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return err
	}
	os.Remove(src)
	return nil
}
