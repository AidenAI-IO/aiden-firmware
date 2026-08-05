package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MemoryPlane interface {
	NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder
	CommitEpisode(ctx context.Context, episode TaskEpisode) error
}

type FilesystemMemoryPlane struct {
	memoryDir  string
	extraction MemoryExtractionConfig
	episodes   *TaskEpisodeStore
	device     *DeviceMemoryStore
	longTerm   *LongTermMemoryStore
	logger     *Logger
}

type MemoryRetrieveRequest struct {
	Input        string
	Attachments  []InputAttachment
	ToolNames    []string
	EpisodeID    string
	DeviceID     string
	CurrentHints CurrentEnvironmentHints
}

type CurrentEnvironmentHints struct {
	ScreenshotWidth  int
	ScreenshotHeight int
	Language         string
	AppName          string
}

type MemoryContext struct {
	Planner  RoleMemoryContext
	Verifier RoleMemoryContext
	Common   RoleMemoryContext
}

type RoleMemoryContext struct {
	SessionSummary   string
	Profile          string
	DeviceProfile    []MemoryHit
	AppProfiles      []MemoryHit
	Procedures       []MemoryHit
	SimilarEpisodes  []MemoryHit
	CalibrationNotes []MemoryHit
	FailureModes     []MemoryHit
	Conflicts        []MemoryHit
}

type MemoryHit struct {
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	Title         string            `json:"title,omitempty"`
	Summary       string            `json:"summary,omitempty"`
	Content       string            `json:"content,omitempty"`
	Priority      int               `json:"priority,omitempty"`
	Confidence    float64           `json:"confidence,omitempty"`
	Tags          []string          `json:"tags,omitempty"`
	Entities      []string          `json:"entities,omitempty"`
	Source        string            `json:"source,omitempty"`
	FilePath      string            `json:"file_path,omitempty"`
	Applicability map[string]string `json:"applicability,omitempty"`
	EvidenceRefs  []MemorySourceRef `json:"evidence_refs,omitempty"`
	// 新增 procedure/导航相关结构化字段，用于 Planner 渲染
	Steps    []ProcedureStep `json:"steps,omitempty"`
	AppName  string          `json:"app_name,omitempty"`
	PageName string          `json:"page_name,omitempty"`
}

const defaultMemoryDeviceID = "default"

type memorySearchQuery struct {
	Terms    []string
	Tags     []string
	Entities []string
	Limit    int
}

type FilesystemMemoryPlaneOption func(*FilesystemMemoryPlane)

// WithMemoryPlaneLongTermStore shares an existing long-term memory store with
// the memory plane instead of creating a second cache for the same directory.
func WithMemoryPlaneLongTermStore(store *LongTermMemoryStore) FilesystemMemoryPlaneOption {
	return func(p *FilesystemMemoryPlane) { p.longTerm = store }
}

func NewFilesystemMemoryPlane(memoryDir string, extraction MemoryExtractionConfig, logger *Logger, opts ...FilesystemMemoryPlaneOption) *FilesystemMemoryPlane {
	if strings.TrimSpace(memoryDir) == "" {
		return nil
	}
	p := &FilesystemMemoryPlane{
		memoryDir:  memoryDir,
		extraction: extraction,
		episodes:   NewTaskEpisodeStore(filepath.Join(memoryDir, "episodes")),
		device:     NewDeviceMemoryStore(filepath.Join(memoryDir, "device")),
		logger:     logger,
	}
	for _, opt := range opts {
		opt(p)
	}
	if p.longTerm == nil {
		p.longTerm = NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"), WithLifecycleDir(filepath.Join(memoryDir, "lifecycle")))
	}
	return p
}

func (p *FilesystemMemoryPlane) LongTerm() *LongTermMemoryStore {
	if p == nil {
		return nil
	}
	return p.longTerm
}

func (p *FilesystemMemoryPlane) NewEpisodeRecorder(req MemoryRetrieveRequest, retrieved MemoryContext) *EpisodeRecorder {
	return NewPersistentEpisodeRecorder(req, retrieved, p.episodes)
}

func (p *FilesystemMemoryPlane) CommitEpisode(ctx context.Context, episode TaskEpisode) error {
	if p == nil || p.memoryDir == "" || strings.TrimSpace(episode.UserGoal) == "" {
		return nil
	}
	if err := p.commitEpisodeTrace(ctx, episode); err != nil {
		return err
	}
	p.commitEpisodeMaintenance(ctx, episode)
	return nil
}

func (p *FilesystemMemoryPlane) commitEpisodeTrace(ctx context.Context, episode TaskEpisode) error {
	if p == nil || p.memoryDir == "" || strings.TrimSpace(episode.UserGoal) == "" {
		return nil
	}
	if _, err := p.episodes.AddEpisode(ctx, episode); err != nil {
		return err
	}
	return nil
}

func (p *FilesystemMemoryPlane) commitEpisodeMaintenance(ctx context.Context, episode TaskEpisode) {
	if p == nil || p.memoryDir == "" || strings.TrimSpace(episode.UserGoal) == "" {
		return
	}
	if err := p.extractLongTermLessons(ctx, episode); err != nil && p.logger != nil {
		p.logger.Warn("[memory] episode lesson extraction failed: %v", err)
	}
	if err := p.extractDeviceLessons(ctx, episode); err != nil && p.logger != nil {
		p.logger.Warn("[memory] device lesson extraction failed: %v", err)
	}
	if err := p.updateReferencedMemoryOutcomes(ctx, episode); err != nil && p.logger != nil {
		p.logger.Warn("[memory] referenced memory update failed: %v", err)
	}
}

func (p *FilesystemMemoryPlane) extractLongTermLessons(ctx context.Context, episode TaskEpisode) error {
	if p.longTerm == nil || !episodeHasTaskTrace(episode) {
		return nil
	}
	var item MemoryItem
	if episode.Outcome.Success {
		tools := episodeToolSequence(episode.Events)
		if len(tools) == 0 {
			return nil
		}
		content := fmt.Sprintf("User goal: %s\nVerified tool path: %s\nVerifier reason: %s",
			episode.UserGoal,
			strings.Join(tools, " -> "),
			episode.Outcome.VerifierReason,
		)
		item = MemoryItem{
			Type:             "procedure",
			Priority:         70,
			Confidence:       0.75,
			Tags:             episode.Tags,
			Entities:         episode.Entities,
			Title:            "Verified task path",
			Content:          content,
			EvidenceExcerpts: []string{content},
			SourceRefs:       []MemorySourceRef{{Type: "episode", ID: episode.ID}},
			TTL:              "45d",
			SuccessCount:     1,
		}
	} else {
		reason := episode.Outcome.FailureReason
		if reason == "" {
			reason = firstNonEmptyString(episode.FailureCauses)
		}
		if strings.TrimSpace(reason) == "" {
			reason = "task did not complete"
		}
		content := fmt.Sprintf("User goal: %s\nFailure reason: %s\nAvoid approving completion without fresh observation evidence.",
			episode.UserGoal,
			reason,
		)
		item = MemoryItem{
			Type:             "failure",
			Priority:         80,
			Confidence:       0.8,
			Tags:             episode.Tags,
			Entities:         episode.Entities,
			Title:            "Failed task pattern",
			Content:          content,
			EvidenceExcerpts: []string{content},
			SourceRefs:       []MemorySourceRef{{Type: "episode", ID: episode.ID}},
			TTL:              "60d",
			FailureCount:     1,
		}
	}
	action, existingID, err := p.longTerm.DecideAction(ctx, item)
	if err != nil {
		return err
	}
	switch action {
	case "ignore":
		return nil
	case "supersede":
		_, err = p.longTerm.SupersedeMemory(ctx, existingID, item)
	default:
		_, err = p.longTerm.AddMemory(ctx, item)
	}
	return err
}

func (p *FilesystemMemoryPlane) extractDeviceLessons(ctx context.Context, episode TaskEpisode) error {
	if p.device == nil || !episodeHasTaskTrace(episode) {
		return nil
	}
	deviceID := firstNonEmptyString([]string{episode.DeviceScope["device_id"], defaultMemoryDeviceID})
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if err := p.recordDeviceProfile(ctx, episode, deviceID, now); err != nil {
		return err
	}
	if err := p.recordAppProfiles(ctx, episode, deviceID, now); err != nil {
		return err
	}

	if !episode.Outcome.Success {
		return p.recordDeviceFailure(ctx, episode, deviceID, now)
	}

	if err := p.recordVerifiedProcedure(ctx, episode, deviceID, now); err != nil {
		return err
	}
	if err := p.recordNavigationFacts(ctx, episode, deviceID, now); err != nil {
		return err
	}
	if err := p.recordCoordinateCalibration(ctx, episode, deviceID, now); err != nil {
		return err
	}
	return nil
}

func (p *FilesystemMemoryPlane) recordDeviceProfile(ctx context.Context, episode TaskEpisode, deviceID, now string) error {
	screen := inferEpisodeScreen(episode.Events)
	if screen == "" {
		return nil
	}
	_, err := p.device.Upsert(ctx, DeviceMemoryItem{
		ID:         "device_" + safePathName(deviceID),
		Type:       "device_profile",
		Status:     "active",
		Title:      "Observed device profile",
		Content:    "Observed screenshot size " + screen + " during task execution.",
		DeviceID:   deviceID,
		Tags:       []string{"device", "screen"},
		Confidence: 0.7,
		TTL:        "90d",
		UpdatedAt:  now,
		Applicability: map[string]string{
			"screen": screen,
		},
		EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: episode.ID}},
	})
	return err
}

func (p *FilesystemMemoryPlane) recordAppProfiles(ctx context.Context, episode TaskEpisode, deviceID, now string) error {
	pagesByApp := observedPagesByApp(episode.Events)
	toolsByApp := observedToolsByApp(episode.Events)

	// 后备：如果没有从 verifier observed_state 中抽到 app，退回到老的 inferEpisodeApps
	// 逻辑（基于 entities），这样没有 observed_state 的测试仍能写入 app_profile。
	if len(pagesByApp) == 0 && len(toolsByApp) == 0 {
		for _, app := range inferEpisodeApps(episode) {
			toolsByApp[app] = episodeToolSequence(episode.Events)
		}
	}

	if len(pagesByApp) == 0 && len(toolsByApp) == 0 {
		return nil
	}
	allApps := make(map[string]bool)
	for app := range pagesByApp {
		allApps[app] = true
	}
	for app := range toolsByApp {
		allApps[app] = true
	}
	for app := range allApps {
		appID := "app_" + stableMemoryID(app)
		existing, found, err := p.device.Get(ctx, appID)
		if err != nil {
			return err
		}
		if !found {
			existing = DeviceMemoryItem{
				ID:       appID,
				Type:     "app_profile",
				Status:   "active",
				Title:    "App profile: " + app,
				DeviceID: deviceID,
				AppID:    app,
				AppName:  app,
				Entities: []string{app},
				Aliases:  []string{app},
				TTL:      "60d",
			}
		}
		existing.PagesSeen = mergeUniqueStrings(existing.PagesSeen, pagesByApp[app])
		existing.ToolsUsed = mergeUniqueStrings(existing.ToolsUsed, toolsByApp[app])
		existing.Confidence = clampConfidence(existing.Confidence + 0.05)
		if existing.Confidence == 0 {
			existing.Confidence = 0.7
		}
		existing.UpdatedAt = now
		existing.EvidenceRefs = appendUniqueMemoryRef(existing.EvidenceRefs, MemorySourceRef{Type: "episode", ID: episode.ID})

		var content strings.Builder
		if len(existing.PagesSeen) > 0 {
			content.WriteString("Pages observed: ")
			content.WriteString(strings.Join(existing.PagesSeen, ", "))
			content.WriteString("\n")
		}
		if len(existing.ToolsUsed) > 0 {
			content.WriteString("Tools used: ")
			content.WriteString(strings.Join(existing.ToolsUsed, ", "))
			content.WriteString("\n")
		}
		existing.Content = strings.TrimSpace(content.String())
		if existing.Content == "" {
			existing.Content = "App was referenced in task goal or extracted entities: " + app
		}

		if episode.Outcome.Success {
			existing.SuccessCount++
		} else {
			failureNote := firstNonEmptyString([]string{episode.Outcome.FailureReason, "task did not complete"})
			existing.KnownIssues = appendUniqueString(existing.KnownIssues, failureNote)
		}

		if _, err := p.device.Upsert(ctx, existing); err != nil {
			return err
		}
	}
	return nil
}

func (p *FilesystemMemoryPlane) recordVerifiedProcedure(ctx context.Context, episode TaskEpisode, deviceID, now string) error {
	steps := episodeProcedureSteps(episode.Events)
	if len(steps) == 0 {
		return nil
	}
	toolNames := make([]string, len(steps))
	for i, step := range steps {
		toolNames[i] = step.Tool
	}
	primaryApp, primaryPage := "", ""
	for _, step := range steps {
		if step.AppName != "" {
			primaryApp = step.AppName
		}
		if step.PageName != "" {
			primaryPage = step.PageName
		}
		if primaryApp != "" && primaryPage != "" {
			break
		}
	}
	tags := append([]string(nil), episode.Tags...)
	entities := append([]string(nil), episode.Entities...)
	if primaryPage != "" {
		tags = appendUniqueString(tags, "page:"+primaryPage)
		entities = appendUniqueString(entities, primaryPage)
	}
	procID := "proc_" + stableMemoryID(primaryApp, primaryPage, episode.UserGoal)
	content := fmt.Sprintf("Goal: %q\nVerified tool path: %s\n\nSteps:\n%s",
		truncateForLog(episode.UserGoal, 120),
		strings.Join(toolNames, " → "),
		summarizeProcedureSteps(steps, 8),
	)
	_, err := p.device.Upsert(ctx, DeviceMemoryItem{
		ID:           procID,
		Type:         "procedure",
		Status:       "active",
		Title:        "Verified procedure",
		Content:      content,
		DeviceID:     deviceID,
		AppName:      primaryApp,
		PageName:     primaryPage,
		Tags:         tags,
		Entities:     entities,
		Steps:        steps,
		Confidence:   0.75,
		Priority:     70,
		TTL:          "45d",
		UpdatedAt:    now,
		EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: episode.ID}},
	})
	return err
}

func (p *FilesystemMemoryPlane) recordDeviceFailure(ctx context.Context, episode TaskEpisode, deviceID, now string) error {
	reason := firstNonEmptyString([]string{episode.Outcome.FailureReason, firstNonEmptyString(episode.FailureCauses), "task did not complete"})
	content := fmt.Sprintf("Goal %q failed: %s. Verifier should require fresh evidence before approving similar tasks.", episode.UserGoal, reason)
	_, err := p.device.Upsert(ctx, DeviceMemoryItem{
		ID:           "fail_" + stableMemoryID(episode.UserGoal, reason),
		Type:         "failure",
		Status:       "active",
		Title:        "Observed task failure",
		Content:      content,
		DeviceID:     deviceID,
		Tags:         append([]string(nil), episode.Tags...),
		Entities:     append([]string(nil), episode.Entities...),
		Confidence:   0.8,
		Priority:     80,
		TTL:          "60d",
		UpdatedAt:    now,
		EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: episode.ID}},
	})
	return err
}

func (p *FilesystemMemoryPlane) recordNavigationFacts(ctx context.Context, episode TaskEpisode, deviceID, now string) error {
	transitions := pageTransitions(episode.Events)
	for _, trans := range transitions {
		fromLabel := trans.FromApp
		if trans.FromPage != "" {
			fromLabel = trans.FromApp + "/" + trans.FromPage
		}
		toLabel := trans.ToApp
		if trans.ToPage != "" {
			toLabel = trans.ToApp + "/" + trans.ToPage
		}
		navID := "nav_" + stableMemoryID(trans.FromApp, trans.FromPage, trans.ToApp, trans.ToPage)
		var contentParts []string
		contentParts = append(contentParts, fmt.Sprintf("%s → %s", fromLabel, toLabel))
		contentParts = append(contentParts, "Tool: "+trans.Tool)
		if trans.Description != "" {
			contentParts = append(contentParts, "Action: "+trans.Description)
		}
		if trans.Coords != "" {
			contentParts = append(contentParts, "Coords: "+trans.Coords)
		}
		if trans.Text != "" {
			contentParts = append(contentParts, "Text: "+truncateForLog(trans.Text, 40))
		}
		content := strings.Join(contentParts, "\n")
		tags := []string{"navigation"}
		if trans.FromApp != "" {
			tags = appendUniqueString(tags, trans.FromApp)
		}
		if trans.ToApp != "" && trans.ToApp != trans.FromApp {
			tags = appendUniqueString(tags, trans.ToApp)
		}
		entities := []string{}
		if trans.FromPage != "" {
			entities = appendUniqueString(entities, trans.FromPage)
		}
		if trans.ToPage != "" && trans.ToPage != trans.FromPage {
			entities = appendUniqueString(entities, trans.ToPage)
		}
		if _, err := p.device.Upsert(ctx, DeviceMemoryItem{
			ID:           navID,
			Type:         "navigation",
			Status:       "active",
			Title:        fmt.Sprintf("%s → %s", fromLabel, toLabel),
			Content:      content,
			DeviceID:     deviceID,
			AppName:      trans.ToApp,
			PageName:     trans.ToPage,
			Tags:         tags,
			Entities:     entities,
			Confidence:   0.7,
			Priority:     65,
			TTL:          "30d",
			UpdatedAt:    now,
			EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: episode.ID}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (p *FilesystemMemoryPlane) recordCoordinateCalibration(ctx context.Context, episode TaskEpisode, deviceID, now string) error {
	if episodeUsesNormalizedCoordinates(episode.Events) {
		_, err := p.device.Upsert(ctx, DeviceMemoryItem{
			ID:           "cal_normalized_coordinates",
			Type:         "calibration",
			Status:       "active",
			Title:        "Prefer normalized coordinates",
			Content:      "A successful task used normalized coordinates; keep preferring normalized coordinates unless current calibration contradicts it.",
			DeviceID:     deviceID,
			Tags:         []string{"calibration", "coordinates"},
			Confidence:   0.8,
			Priority:     75,
			TTL:          "30d",
			UpdatedAt:    now,
			EvidenceRefs: []MemorySourceRef{{Type: "episode", ID: episode.ID}},
		})
		return err
	}
	return nil
}

func (p *FilesystemMemoryPlane) updateReferencedMemoryOutcomes(ctx context.Context, episode TaskEpisode) error {
	refs := uniqueNonEmpty(episode.RetrievedMemoryRefs)
	if len(refs) == 0 {
		return nil
	}
	if p.longTerm != nil {
		if err := p.longTerm.UpdateMemories(ctx, refs, func(item *MemoryItem) {
			updateLongTermMemoryFromEpisode(item, episode)
		}); err != nil {
			return err
		}
	}
	if p.device != nil {
		if err := p.device.UpdateMany(ctx, refs, func(item *DeviceMemoryItem) {
			updateDeviceMemoryFromEpisode(item, episode)
		}); err != nil {
			return err
		}
	}
	return nil
}

func updateLongTermMemoryFromEpisode(item *MemoryItem, episode TaskEpisode) {
	if item == nil || item.Status != "active" {
		return
	}
	now := time.Now().UTC()
	if episode.Outcome.Success {
		if item.Type == "failure" {
			item.Status = "conflicted"
			item.ConflictsWith = appendUniqueString(item.ConflictsWith, episode.ID)
			item.Confidence = clampConfidence(item.Confidence - 0.25)
			return
		}
		item.SuccessCount++
		item.LastValidatedAt = now.Format(time.RFC3339Nano)
		item.Confidence = clampConfidence(item.Confidence + 0.05)
		refreshMemoryExpiry(&item.ExpiresAt, item.TTL, now)
		return
	}
	if shouldPenalizeMemoryType(item.Type) {
		item.FailureCount++
		item.Confidence = clampConfidence(item.Confidence - 0.15)
		if memoryFailureDominates(item.SuccessCount, item.FailureCount) {
			item.Status = "conflicted"
			item.ConflictsWith = appendUniqueString(item.ConflictsWith, episode.ID)
		}
	}
}

func updateDeviceMemoryFromEpisode(item *DeviceMemoryItem, episode TaskEpisode) {
	if item == nil || item.Status != "active" {
		return
	}
	now := time.Now().UTC()
	if episode.Outcome.Success {
		if item.Type == "failure" {
			item.Status = "conflicted"
			item.ConflictsWith = appendUniqueString(item.ConflictsWith, episode.ID)
			item.Confidence = clampConfidence(item.Confidence - 0.25)
			return
		}
		item.SuccessCount++
		item.LastValidatedAt = now.Format(time.RFC3339Nano)
		item.Confidence = clampConfidence(item.Confidence + 0.05)
		refreshMemoryExpiry(&item.ExpiresAt, item.TTL, now)
		return
	}
	if shouldPenalizeMemoryType(item.Type) {
		item.FailureCount++
		item.Confidence = clampConfidence(item.Confidence - 0.15)
		if memoryFailureDominates(item.SuccessCount, item.FailureCount) {
			item.Status = "conflicted"
			item.ConflictsWith = appendUniqueString(item.ConflictsWith, episode.ID)
		}
	}
}


func renderMemoryHitLine(hit MemoryHit) string {
	label := hit.ID
	if label == "" {
		label = hit.Type
	}
	var attrs []string
	if hit.Type != "" {
		attrs = append(attrs, "type="+hit.Type)
	}
	if hit.Confidence > 0 {
		attrs = append(attrs, fmt.Sprintf("confidence=%.2f", hit.Confidence))
	}
	if hit.PageName != "" {
		attrs = append(attrs, "page="+hit.PageName)
	}
	prefix := "[" + label
	if len(attrs) > 0 {
		prefix += " " + strings.Join(attrs, " ")
	}
	prefix += "] "

	// procedure/navigation 有 Steps 时做结构化渲染
	if (hit.Type == "procedure" || hit.Type == "navigation") && len(hit.Steps) > 0 {
		title := firstNonEmptyString([]string{hit.Title, hit.Summary})
		stepSummary := summarizeProcedureSteps(hit.Steps, 5)
		body := title
		if stepSummary != "" {
			body += "\n" + stepSummary
		}
		return prefix + body
	}

	body := firstNonEmptyString([]string{hit.Summary, hit.Content, hit.Title})
	body = strings.ReplaceAll(body, "\n", " ")
	return prefix + truncateForLog(body, 280)
}

func normalizeMemoryContext(value interface{}) MemoryContext {
	switch typed := value.(type) {
	case MemoryContext:
		return typed
	case *MemoryContext:
		if typed != nil {
			return *typed
		}
	case string:
		return MemoryContext{Planner: RoleMemoryContext{SessionSummary: typed}}
	}
	return MemoryContext{}
}

func readTextFileIfExists(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}


func normalizeSearchTerms(values []string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, value := range values {
		for _, token := range splitSearchTerms(value) {
			if token == "" || seen[token] {
				continue
			}
			seen[token] = true
			terms = append(terms, token)
		}
	}
	return terms
}

func splitSearchTerms(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return strings.ContainsRune(" \t\n\r，。,.、；;：:\"'（）()[]【】{}<>/\\|+-=*?!", r)
	})
	var out []string
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		out = append(out, field)
	}
	if len([]rune(value)) <= 32 {
		out = append(out, value)
	}
	runes := []rune(value)
	if len(runes) >= 2 && len(runes) <= 16 {
		for i := 0; i < len(runes)-1; i++ {
			if isCJKRune(runes[i]) || isCJKRune(runes[i+1]) {
				out = append(out, string(runes[i:i+2]))
			}
		}
	}
	return out
}

func isCJKRune(r rune) bool {
	return (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3400 && r <= 0x4dbf)
}

func episodeHasTaskTrace(episode TaskEpisode) bool {
	for _, event := range episode.Events {
		if event.Type == runEventToolCall || event.Type == "tool_result" {
			return true
		}
	}
	return !episode.Outcome.Success && strings.TrimSpace(episode.Outcome.FailureReason) != ""
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func memoryHitApplicable(hit MemoryHit, req MemoryRetrieveRequest) bool {
	if len(hit.Applicability) == 0 {
		return true
	}
	for key, want := range hit.Applicability {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "device", "device_id":
			if req.DeviceID != "" && !strings.EqualFold(req.DeviceID, want) {
				return false
			}
		case "language", "lang":
			if req.CurrentHints.Language != "" && !strings.EqualFold(req.CurrentHints.Language, want) {
				return false
			}
		case "screen", "resolution":
			screen := ""
			if req.CurrentHints.ScreenshotWidth > 0 && req.CurrentHints.ScreenshotHeight > 0 {
				screen = fmt.Sprintf("%dx%d", req.CurrentHints.ScreenshotWidth, req.CurrentHints.ScreenshotHeight)
			}
			if screen != "" && !strings.EqualFold(screen, want) {
				return false
			}
		case "app", "app_id", "app_name":
			if req.CurrentHints.AppName != "" && !strings.EqualFold(req.CurrentHints.AppName, want) {
				return false
			}
		}
	}
	return true
}

func ttlExpiresAt(now time.Time, ttl string) string {
	d, ok := parseRetentionDuration(ttl)
	if !ok || d <= 0 {
		return ""
	}
	return now.Add(d).UTC().Format(time.RFC3339Nano)
}

func stableMemoryID(parts ...string) string {
	hash := sha1.New()
	for _, part := range parts {
		hash.Write([]byte(strings.TrimSpace(part)))
		hash.Write([]byte{0})
	}
	sum := hash.Sum(nil)
	return hex.EncodeToString(sum[:6])
}

func inferEpisodeScreen(events []TaskEpisodeEvent) string {
	for i := len(events) - 1; i >= 0; i-- {
		raw := firstNonEmptyString([]string{events[i].RawObservation, events[i].Observation})
		if raw == "" {
			continue
		}
		var result struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil {
			continue
		}
		if result.Width > 0 && result.Height > 0 {
			return fmt.Sprintf("%dx%d", result.Width, result.Height)
		}
	}
	return ""
}

func inferEpisodeApps(episode TaskEpisode) []string {
	seen := map[string]bool{}
	var apps []string
	for _, value := range episode.Entities {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(value, "App") || strings.Contains(value, "app") || strings.Contains(value, "APP") {
			if !seen[value] {
				seen[value] = true
				apps = append(apps, value)
			}
		}
	}
	for _, event := range episode.Events {
		if event.ObservedState == nil {
			continue
		}
		app := strings.TrimSpace(event.ObservedState.AppName)
		if app == "" || seen[app] {
			continue
		}
		seen[app] = true
		apps = append(apps, app)
	}
	return apps
}

func episodeUsesNormalizedCoordinates(events []TaskEpisodeEvent) bool {
	for _, event := range events {
		if event.Type != runEventToolCall {
			continue
		}
		input := strings.ToLower(event.ToolInput)
		if strings.Contains(input, `"coord_space":"normalized"`) ||
			strings.Contains(input, `"coord_space": "normalized"`) {
			return true
		}
	}
	return false
}

func shouldPenalizeMemoryType(memoryType string) bool {
	switch memoryType {
	case "procedure", "calibration", "app_profile", "device_profile", "task_episode_summary", "fact", "rule":
		return true
	default:
		return false
	}
}

func memoryFailureDominates(successCount int, failureCount int) bool {
	return failureCount >= 2 && failureCount > successCount
}

func clampConfidence(value float64) float64 {
	if value <= 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func refreshMemoryExpiry(expiresAt *string, ttl string, now time.Time) {
	if expiresAt == nil {
		return
	}
	if refreshed := ttlExpiresAt(now, ttl); refreshed != "" {
		*expiresAt = refreshed
	}
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func mergeUniqueStrings(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, v := range b {
		out = appendUniqueString(out, v)
	}
	return out
}

func appendUniqueMemoryRef(refs []MemorySourceRef, ref MemorySourceRef) []MemorySourceRef {
	for _, existing := range refs {
		if existing.Type == ref.Type && existing.ID == ref.ID {
			return refs
		}
	}
	return append(refs, ref)
}
