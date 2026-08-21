package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"aiden-agent/internal/agent/model"
	"aiden-agent/internal/ble"
	"github.com/tmc/langchaingo/llms"
)

var errNotificationMemoryHold = errors.New("notification memory proposal is on hold")

const (
	notificationMemoryBatchLimit = 20
	notificationMemoryTTL        = 7 * 24 * time.Hour
	notificationLongTermTTL      = 90 * 24 * time.Hour
)

// NotificationMemoryProcessor is the notification-specific input and policy
// adapter for a MemoryWorker. It filters obvious high-risk noise, prepares raw
// notification evidence, and asks MemoryMergeEngine to run the shared
// evidence + top-k + LLM + Apply pipeline. Tests without a model retain a
// deterministic temporary-memory fallback.
type NotificationMemoryProcessor struct {
	context   *NotificationContext
	temporary *LongTermMemoryStore
	longTerm  *LongTermMemoryStore
	merge     *MemoryMergeEngine
	now       func() time.Time
}

var _ MemoryProcessor = (*NotificationMemoryProcessor)(nil)

func NewNotificationMemoryProcessor(notificationContext *NotificationContext, memoryDir string, longTermStore *LongTermMemoryStore, models model.Model) *NotificationMemoryProcessor {
	return newNotificationMemoryProcessorWithGate(notificationContext, memoryDir, longTermStore, models, nil)
}

func newNotificationMemoryProcessorWithGate(notificationContext *NotificationContext, memoryDir string, longTermStore *LongTermMemoryStore, models model.Model, gate *MemoryRunGate) *NotificationMemoryProcessor {
	if longTermStore == nil {
		longTermStore = NewLongTermMemoryStore(filepath.Join(memoryDir, "long_term"))
	}
	var merge *MemoryMergeEngine
	if models != nil {
		merge = NewMemoryMergeEngineWithGate(models, gate)
	}
	return &NotificationMemoryProcessor{
		context:   notificationContext,
		temporary: NewLongTermMemoryStore(filepath.Join(memoryDir, "temporary")),
		longTerm:  longTermStore,
		merge:     merge,
		now:       func() time.Time { return time.Now().UTC() },
	}
}

func (p *NotificationMemoryProcessor) Initialize() error {
	return nil
}

func (p *NotificationMemoryProcessor) NextRunAt(ctx context.Context) (time.Time, error) {
	if p == nil || p.context == nil {
		return time.Time{}, nil
	}
	return p.now(), nil
}

func (p *NotificationMemoryProcessor) ProcessBatch(ctx context.Context, limit int, shouldStop func() bool) (MemoryBatchResult, error) {
	if p == nil || p.context == nil {
		return MemoryBatchResult{}, nil
	}
	if limit <= 0 {
		limit = notificationMemoryBatchLimit
	}
	pending, err := p.context.ReadPending(ctx, limit)
	if err != nil {
		return MemoryBatchResult{}, err
	}
	_, consumeErr := p.context.Consume(ctx, limit)
	if consumeErr == nil {
		pending, err = p.context.ReadPending(ctx, limit)
		if err != nil {
			return MemoryBatchResult{}, err
		}
	} else if len(pending) == 0 {
		// There is no durable work to process while BLE is unavailable.
		// Keep the worker alive, but surface the error for retry/backoff.
		return MemoryBatchResult{HasPending: true}, consumeErr
	}
	processed := append([]NotificationRecord(nil), pending...)
	stopped := false
	for _, record := range coalesceNotificationRecords(pending) {
		if shouldStop != nil && shouldStop() {
			stopped = true
			break
		}
		if err := ctx.Err(); err != nil {
			return MemoryBatchResult{HasPending: true}, err
		}
		if strings.EqualFold(strings.TrimSpace(record.NotificationEvent.Event), "removed") {
			if err := p.removeTemporary(ctx, record); err != nil {
				return MemoryBatchResult{HasPending: true}, err
			}
		} else if !notificationMemoryIgnored(record.NotificationEvent) {
			if err := p.resolveRecord(ctx, record); err != nil {
				if errors.Is(err, errNotificationMemoryHold) {
					return MemoryBatchResult{HasPending: true}, nil
				}
				return MemoryBatchResult{HasPending: true}, err
			}
		}
	}
	if stopped {
		return MemoryBatchResult{HasPending: true}, nil
	}
	if len(processed) > 0 {
		if err := p.context.CommitProcessed(ctx, processed); err != nil {
			return MemoryBatchResult{HasPending: true}, err
		}
	}
	// Keep polling while the worker is alive. The BLE service currently exposes
	// a cursor API rather than an Agent-side push callback.
	return MemoryBatchResult{HasPending: true}, nil
}

type notificationMemoryProposal struct {
	Actions []notificationMemoryAction `json:"actions"`
}

type notificationMemoryAction struct {
	Action         string   `json:"action"`
	Scope          string   `json:"scope"`
	MemoryID       string   `json:"memory_id,omitempty"`
	MemoryRevision int      `json:"memory_revision,omitempty"`
	Type           string   `json:"type,omitempty"`
	Title          string   `json:"title,omitempty"`
	Content        string   `json:"content,omitempty"`
	Tags           []string `json:"tags,omitempty"`
	Entities       []string `json:"entities,omitempty"`
	ExpiresAt      string   `json:"expires_at,omitempty"`
	SourceEventIDs []string `json:"source_event_ids,omitempty"`
}

func (p *NotificationMemoryProcessor) resolveRecord(ctx context.Context, record NotificationRecord) error {
	if p.merge == nil {
		return p.persistTemporary(ctx, record)
	}
	var proposal notificationMemoryProposal
	err := p.merge.Merge(ctx, MemoryMergeRequest{
		Search: func(ctx context.Context) ([]MemoryMergeReference, error) {
			return p.searchRelatedNotificationMemories(ctx, record)
		},
		BuildMessages: func(refs []MemoryMergeReference) ([]llms.MessageContent, error) {
			payload, err := json.Marshal(map[string]any{
				"notification":     record,
				"related_memories": refs,
			})
			if err != nil {
				return nil, err
			}
			prompt := "You consolidate a notification into user memory. Use only the notification evidence. Return JSON only with actions. " +
				"action must be ignore, add, update, reinforce, remove, promote, or hold; scope must be temporary or long_term. " +
				"Do not retain OTPs, marketing, secrets, or one-off noise. For temporary conclusions use an expiry. " +
				"If an existing memory is updated, include its exact memory_id and memory_revision.\n\n" + string(payload)
			return []llms.MessageContent{
				llms.TextParts(llms.ChatMessageTypeSystem, "You produce safe, concise notification memory proposals. Output strict JSON."),
				llms.TextParts(llms.ChatMessageTypeHuman, prompt),
			}, nil
		},
		MaxTokens: 1400,
		Timeout:   45 * time.Second,
		Apply: func(ctx context.Context, raw string, references []MemoryMergeReference) error {
			if err := json.Unmarshal([]byte(raw), &proposal); err != nil {
				return fmt.Errorf("parse notification memory proposal: %w", err)
			}
			return p.applyNotificationProposal(ctx, record, proposal, references)
		},
	})
	if err != nil {
		return err
	}
	return nil
}

func (p *NotificationMemoryProcessor) searchRelatedNotificationMemories(ctx context.Context, record NotificationRecord) ([]MemoryMergeReference, error) {
	event := record.NotificationEvent
	query := MemoryQuery{Tags: notificationMemoryTags(event), Entities: compactNotificationEntities(event), Limit: 4}
	temporaryRefs := make([]MemoryMergeReference, 0, 4)
	longTermRefs := make([]MemoryMergeReference, 0, 4)
	if p.temporary != nil {
		results, err := p.temporary.Search(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			temporaryRefs = append(temporaryRefs, memoryResultMergeReference("temporary", result))
		}
	}
	if p.longTerm != nil {
		results, err := p.longTerm.Search(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, result := range results {
			longTermRefs = append(longTermRefs, memoryResultMergeReference("long_term", result))
		}
	}
	refs := make([]MemoryMergeReference, 0, 8)
	for index := 0; index < 4; index++ {
		if index < len(temporaryRefs) {
			refs = append(refs, temporaryRefs[index])
		}
		if index < len(longTermRefs) {
			refs = append(refs, longTermRefs[index])
		}
	}
	return refs, nil
}

func memoryResultMergeReference(scope string, result MemoryResult) MemoryMergeReference {
	return MemoryMergeReference{Scope: scope, ID: result.ID, Type: result.Type, Status: result.Status, Title: result.Title, Summary: result.Summary, Content: result.Content, Tags: result.Tags, Entities: result.Entities, Revision: result.Revision, ExpiresAt: result.ExpiresAt, Priority: result.Priority, Confidence: result.Confidence, SourceRefs: result.SourceRefs, EvidenceRefs: result.EvidenceRefs}
}

func (p *NotificationMemoryProcessor) applyNotificationProposal(ctx context.Context, record NotificationRecord, proposal notificationMemoryProposal, refs []MemoryMergeReference) error {
	if len(proposal.Actions) > 3 {
		return fmt.Errorf("notification proposal has %d actions; maximum is 3", len(proposal.Actions))
	}
	for actionIndex, action := range proposal.Actions {
		action.Action = strings.ToLower(strings.TrimSpace(action.Action))
		action.Scope = strings.ToLower(strings.TrimSpace(action.Scope))
		switch action.Action {
		case "ignore", "hold":
			if action.Scope != "" && action.Scope != "temporary" && action.Scope != "long_term" {
				return fmt.Errorf("notification proposal has invalid scope %q for %s", action.Scope, action.Action)
			}
			if action.Action == "hold" {
				return errNotificationMemoryHold
			}
			continue
		case "add", "update", "reinforce", "remove", "promote":
		default:
			return fmt.Errorf("notification proposal has unsupported action %q", action.Action)
		}
		if action.Scope != "temporary" && action.Scope != "long_term" {
			return fmt.Errorf("notification proposal has invalid scope %q", action.Scope)
		}
		if action.Action == "promote" {
			if action.Scope != "long_term" || strings.TrimSpace(action.MemoryID) == "" || action.MemoryRevision <= 0 {
				return fmt.Errorf("notification promote proposal requires long_term scope, temporary memory_id, and memory_revision")
			}
			ref, ok := findNotificationReference(action.MemoryID, "temporary", refs)
			if !ok || effectiveMergeReferenceRevision(ref) != action.MemoryRevision {
				return fmt.Errorf("notification promote proposal references unknown or stale temporary memory %q", action.MemoryID)
			}
			if p.longTerm == nil || p.temporary == nil {
				return fmt.Errorf("notification promote stores are not configured")
			}
			item := MemoryItem{ID: "mem_notification_" + record.ContextID, Type: firstNonEmptyString([]string{action.Type, ref.Type, "fact"}), TimeScope: "long_term", Title: firstNonEmptyString([]string{action.Title, ref.Title}), Content: firstNonEmptyString([]string{action.Content, ref.Content, ref.Summary}), ExpiresAt: p.now().Add(notificationLongTermTTL).Format(time.RFC3339Nano), Tags: mergeUniqueStrings(ref.Tags, action.Tags), Entities: mergeUniqueStrings(ref.Entities, action.Entities), SourceRefs: ref.SourceRefs, EvidenceRefs: ref.EvidenceRefs, EvidenceExcerpts: []string{firstNonEmptyString([]string{action.Content, ref.Content, ref.Summary, record.NotificationEvent.Message, record.NotificationEvent.Title})}}
			if strings.TrimSpace(item.Content) == "" {
				return fmt.Errorf("notification promote proposal requires content")
			}
			if _, err := p.longTerm.ApplyMemoryIntent(ctx, MemoryIntent{Item: item}); err != nil {
				return err
			}
			if _, err := p.temporary.ApplyMemoryIntent(ctx, MemoryIntent{Item: MemoryItem{ID: action.MemoryID}, ExpectedRevision: action.MemoryRevision, Remove: true}); err != nil {
				return err
			}
			continue
		}
		if action.Action == "remove" && strings.TrimSpace(action.MemoryID) == "" {
			return fmt.Errorf("notification remove proposal requires memory_id")
		}
		if action.Action == "add" && strings.TrimSpace(action.MemoryID) != "" {
			return fmt.Errorf("notification add proposal must not provide memory_id")
		}
		var existingRef MemoryMergeReference
		if action.Action == "update" || action.Action == "reinforce" || action.Action == "remove" {
			if action.MemoryRevision <= 0 {
				return fmt.Errorf("notification %s proposal requires a positive memory_revision", action.Action)
			}
			if !notificationProposalReferencesMemory(action, refs) {
				return fmt.Errorf("notification proposal references unknown or stale memory %q", action.MemoryID)
			}
			if action.Action != "remove" {
				existingRef, _ = findNotificationReference(action.MemoryID, action.Scope, refs)
			}
		}
		if action.Action != "remove" && action.Action != "reinforce" && strings.TrimSpace(action.Content) == "" {
			return fmt.Errorf("notification %s proposal requires content", action.Action)
		}
		if action.Action != "remove" && !validNotificationMemoryType(action.Type) {
			return fmt.Errorf("notification proposal has invalid memory type %q", action.Type)
		}
		if err := validateNotificationProposalSourceEvents(record, action.SourceEventIDs); err != nil {
			return err
		}
		id := strings.TrimSpace(action.MemoryID)
		if id == "" {
			id = "tmp_notification_" + record.ContextID
			if action.Scope == "long_term" {
				id = "mem_notification_" + record.ContextID
			}
			if len(proposal.Actions) > 1 {
				id = fmt.Sprintf("%s_%d", id, actionIndex+1)
			}
		}
		expiresAt, err := p.notificationProposalExpiry(action)
		if err != nil {
			return err
		}
		item := MemoryItem{ID: id, Type: firstNonEmptyString([]string{action.Type, existingRef.Type, "fact"}), TimeScope: action.Scope, Title: firstNonEmptyString([]string{action.Title, existingRef.Title}), Content: firstNonEmptyString([]string{action.Content, existingRef.Content}), Tags: mergeUniqueStrings(mergeUniqueStrings(notificationMemoryTags(record.NotificationEvent), existingRef.Tags), action.Tags), Entities: mergeUniqueStrings(mergeUniqueStrings(compactNotificationEntities(record.NotificationEvent), existingRef.Entities), action.Entities), ExpiresAt: expiresAt, SourceRefs: []MemorySourceRef{{Type: "notification", ID: record.ContextID, EventIDs: mergeUniqueStrings(notificationMemoryEvidenceIDs(record), action.SourceEventIDs)}}, EvidenceExcerpts: []string{firstNonEmptyString([]string{action.Content, existingRef.Content, record.NotificationEvent.Message, record.NotificationEvent.Title})}}
		if action.Action == "remove" {
			item = MemoryItem{ID: id}
		}
		store := p.temporary
		if action.Scope == "long_term" {
			store = p.longTerm
		}
		if store == nil {
			return fmt.Errorf("notification memory store is not configured for scope %s", action.Scope)
		}
		_, err = store.ApplyMemoryIntent(ctx, MemoryIntent{Item: item, ExpectedRevision: action.MemoryRevision, Remove: action.Action == "remove"})
		if err != nil {
			return err
		}
	}
	return nil
}

func validNotificationMemoryType(memoryType string) bool {
	switch strings.ToLower(strings.TrimSpace(memoryType)) {
	case "", "fact", "preference", "rule", "profile":
		return true
	default:
		return false
	}
}

func findNotificationReference(id, scope string, refs []MemoryMergeReference) (MemoryMergeReference, bool) {
	for _, ref := range refs {
		if ref.ID == strings.TrimSpace(id) && ref.Scope == scope && ref.Status == "active" {
			return ref, true
		}
	}
	return MemoryMergeReference{}, false
}

func (p *NotificationMemoryProcessor) notificationProposalExpiry(action notificationMemoryAction) (string, error) {
	if action.Scope != "temporary" {
		expiresAt := strings.TrimSpace(action.ExpiresAt)
		if expiresAt == "" {
			expiresAt = p.now().Add(notificationLongTermTTL).Format(time.RFC3339Nano)
		}
		return expiresAt, nil
	}
	expiresAt := strings.TrimSpace(action.ExpiresAt)
	if expiresAt == "" {
		expiresAt = p.now().Add(notificationMemoryTTL).Format(time.RFC3339Nano)
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil {
		return "", fmt.Errorf("temporary notification memory expires_at must be RFC3339: %w", err)
	}
	if !parsed.After(p.now()) {
		return "", fmt.Errorf("temporary notification memory expires_at must be in the future")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func validateNotificationProposalSourceEvents(record NotificationRecord, sourceEventIDs []string) error {
	allowed := map[string]bool{}
	for _, id := range []string{record.NotificationEvent.ID, record.NotificationEvent.SourceEventID} {
		if id = strings.TrimSpace(id); id != "" {
			allowed[id] = true
		}
	}
	for _, id := range sourceEventIDs {
		if id = strings.TrimSpace(id); id != "" && !allowed[id] {
			return fmt.Errorf("notification proposal references unrelated source event %q", id)
		}
	}
	return nil
}

func notificationProposalReferencesMemory(action notificationMemoryAction, refs []MemoryMergeReference) bool {
	for _, ref := range refs {
		if ref.ID != strings.TrimSpace(action.MemoryID) || ref.Scope != action.Scope || ref.Status != "active" {
			continue
		}
		return action.MemoryRevision == effectiveMergeReferenceRevision(ref)
	}
	return false
}

func effectiveMergeReferenceRevision(ref MemoryMergeReference) int {
	if ref.Revision > 0 {
		return ref.Revision
	}
	return 1
}

func coalesceNotificationRecords(records []NotificationRecord) []NotificationRecord {
	if len(records) < 2 {
		return records
	}
	last := make(map[string]int, len(records))
	for index, record := range records {
		last[notificationRecordIdentity(record)] = index
	}
	result := make([]NotificationRecord, 0, len(last))
	for index, record := range records {
		if last[notificationRecordIdentity(record)] == index {
			result = append(result, record)
		}
	}
	return result
}

func notificationRecordIdentity(record NotificationRecord) string {
	event := record.NotificationEvent
	if event.DeviceID == "" && event.Source == "" && event.SourceID == "" && event.NotificationUID == 0 {
		return "context\x00" + record.ContextID
	}
	identity := strings.Join([]string{event.DeviceID, event.Source, event.SourceID, fmt.Sprintf("%d", event.NotificationUID)}, "\x00")
	return identity
}

func (p *NotificationMemoryProcessor) persistTemporary(ctx context.Context, record NotificationRecord) error {
	event := record.NotificationEvent
	app := strings.TrimSpace(event.AppIdentifier)
	if app == "" && strings.TrimSpace(event.Title) == "" && strings.TrimSpace(event.Message) == "" {
		return nil
	}
	title := strings.TrimSpace(event.Title)
	if title == "" {
		title = app
	}
	if title == "" {
		title = "通知"
	}
	content := fmt.Sprintf("收到来自 %s 的通知（原始记录 context_id=%s）", app, record.ContextID)
	now := p.now()
	item := MemoryItem{
		ID:         "tmp_notification_" + record.ContextID,
		Type:       "fact",
		TimeScope:  "temporary",
		Priority:   40,
		Confidence: 0.7,
		Title:      title,
		Content:    content,
		Tags:       notificationMemoryTags(event),
		Entities:   compactNotificationEntities(event),
		CreatedAt:  now.Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(notificationMemoryTTL).Format(time.RFC3339Nano),
		SourceRefs: []MemorySourceRef{{
			Type:     "notification",
			ID:       record.ContextID,
			EventIDs: notificationMemoryEvidenceIDs(record),
		}},
		EvidenceExcerpts: []string{content},
	}
	if p.temporary == nil {
		return fmt.Errorf("temporary memory store is not configured")
	}
	_, err := p.temporary.ApplyMemoryIntent(ctx, MemoryIntent{Item: item})
	return err
}

func (p *NotificationMemoryProcessor) removeTemporary(ctx context.Context, record NotificationRecord) error {
	if p.temporary == nil {
		return fmt.Errorf("temporary memory store is not configured")
	}
	memories, err := p.temporary.Search(ctx, MemoryQuery{Limit: 1000})
	if err != nil {
		return err
	}
	removed := false
	for _, memory := range memories {
		if !notificationMemoryReferencesRecord(memory.SourceRefs, record) {
			continue
		}
		if _, err := p.temporary.ApplyMemoryIntent(ctx, MemoryIntent{Item: MemoryItem{ID: memory.ID}, ExpectedRevision: effectiveMemoryResultRevision(memory), Remove: true}); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return nil
	}
	_, err = p.temporary.ApplyMemoryIntent(ctx, MemoryIntent{Item: MemoryItem{ID: "tmp_notification_" + record.ContextID}, Remove: true})
	return err
}

func effectiveMemoryResultRevision(memory MemoryResult) int {
	if memory.Revision > 0 {
		return memory.Revision
	}
	return 1
}

func notificationMemoryReferencesRecord(refs []MemorySourceRef, record NotificationRecord) bool {
	identities := notificationMemoryStableIdentityIDs(record.NotificationEvent)
	identities = append(identities, strings.TrimSpace(record.NotificationEvent.ID), strings.TrimSpace(record.NotificationEvent.SourceEventID))
	identities = mergeUniqueStrings(nil, identities)
	if len(identities) == 0 {
		return false
	}
	for _, ref := range refs {
		if ref.Type != "notification" {
			continue
		}
		for _, eventID := range ref.EventIDs {
			for _, identity := range identities {
				if eventID == identity {
					return true
				}
			}
		}
	}
	return false
}

func notificationMemoryEvidenceIDs(record NotificationRecord) []string {
	ids := []string{record.NotificationEvent.ID, record.NotificationEvent.SourceEventID}
	ids = append(ids, notificationMemoryStableIdentityIDs(record.NotificationEvent)...)
	return mergeUniqueStrings(nil, ids)
}

func notificationMemoryStableIdentityIDs(event ble.NotificationEvent) []string {
	return notificationEventStableIdentityIDs(event)
}

func (p *NotificationMemoryProcessor) logBatchError(err error) {}

func notificationMemoryIgnored(event ble.NotificationEvent) bool {
	if strings.EqualFold(strings.TrimSpace(event.Event), "removed") {
		return true
	}
	value := strings.ToLower(strings.Join([]string{event.AppIdentifier, event.Title, event.Message, event.Category}, " "))
	for _, marker := range []string{"验证码", "verification code", "one-time password", "otp", "营销", "促销", "promotion", "advertisement", "广告", "银行", "bank", "支付授权", "payment authorization", "安全告警", "security alert", "健康", "health", "法律", "legal", "私人聊天", "private message", "私信"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return strings.TrimSpace(event.Title) == "" && strings.TrimSpace(event.Message) == ""
}

func notificationMemoryTags(event ble.NotificationEvent) []string {
	tags := []string{"notification"}
	if app := strings.TrimSpace(event.AppIdentifier); app != "" {
		tags = append(tags, app)
	}
	return tags
}

func compactNotificationEntities(event ble.NotificationEvent) []string {
	if value := strings.TrimSpace(event.AppIdentifier); value != "" {
		return []string{value}
	}
	return nil
}
