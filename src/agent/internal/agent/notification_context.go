package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"aiden-agent/internal/ble"
)

const (
	defaultNotificationConsumeLimit = 100
	defaultNotificationPendingLimit = 20
	maxNotificationFingerprints     = 4096
	trimNotificationFingerprintsTo  = 3072
	maxNotificationStoredText       = 4096
	maxNotificationStoredApp        = 255
)

// NotificationEventReader is the narrow dependency used by NotificationContext.
// Production passes ble.RequestEvents; tests can provide a deterministic reader.
type NotificationEventReader func(context.Context, string, string, int) (ble.EventPage, error)

// NotificationQuery selects persisted notification records without touching
// the producer or either consumer cursor. An empty Since value means "latest"
// and returns the newest matching records up to Limit.
type NotificationQuery struct {
	Since         string
	Limit         int
	Date          string
	AppIdentifier string
	Text          string
}

// NotificationRecord is the durable envelope around the original BLE event.
// ID remains the producer-local cursor; ContextID is monotonic across BLE
// service generations and is therefore safe for Memory processing cursors.
type NotificationRecord struct {
	ble.NotificationEvent
	ContextID  string `json:"context_id"`
	Generation string `json:"generation,omitempty"`
}

// NotificationContextState is the durable consumer state. Cursors remain
// strings on disk so the file is safe for clients that cannot represent uint64
// precisely in JSON numbers.
type NotificationContextState struct {
	Version       int               `json:"version"`
	Generation    string            `json:"generation,omitempty"`
	SourceCursor  string            `json:"source_cursor,omitempty"`
	MemoryCursor  string            `json:"memory_cursor,omitempty"`
	StoredCursor  string            `json:"stored_cursor,omitempty"`
	OldestEventID string            `json:"oldest_event_id,omitempty"`
	GapCount      int               `json:"gap_count,omitempty"`
	Gaps          []NotificationGap `json:"gaps,omitempty"`
	UpdatedAt     string            `json:"updated_at"`
}

type NotificationGap struct {
	At         string `json:"at"`
	Reason     string `json:"reason"`
	Generation string `json:"generation,omitempty"`
	FromID     string `json:"from_id,omitempty"`
	ToID       string `json:"to_id,omitempty"`
}

type notificationFingerprintFile struct {
	Version      int               `json:"version"`
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

type NotificationContext struct {
	mu           sync.Mutex
	rootDir      string
	eventsDir    string
	statePath    string
	fingerPath   string
	reader       NotificationEventReader
	state        NotificationContextState
	fingerprints map[string]string
}

func NewNotificationContext(memoryDir string, reader NotificationEventReader) (*NotificationContext, error) {
	if strings.TrimSpace(memoryDir) == "" {
		return nil, fmt.Errorf("notification memory directory is required")
	}
	if reader == nil {
		reader = func(ctx context.Context, since, generation string, limit int) (ble.EventPage, error) {
			return ble.RequestEvents(ctx, configuredBLEServiceSocketPath(), since, generation, limit)
		}
	}
	root := filepath.Join(memoryDir, "notifications")
	c := &NotificationContext{
		rootDir:      root,
		eventsDir:    filepath.Join(root, "events"),
		statePath:    filepath.Join(root, "state.json"),
		fingerPath:   filepath.Join(root, "fingerprints.json"),
		reader:       reader,
		state:        NotificationContextState{Version: 1},
		fingerprints: map[string]string{},
	}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *NotificationContext) State() NotificationContextState {
	if c == nil {
		return NotificationContextState{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneNotificationContextState(c.state)
}

// Consume fetches a page from ble_service, appends accepted events durably,
// and advances SourceCursor only after each event is synced to disk.
func (c *NotificationContext) Consume(ctx context.Context, limit int) ([]NotificationRecord, error) {
	if c == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = defaultNotificationConsumeLimit
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	since := normalizeCursor(c.state.SourceCursor)
	generation := strings.TrimSpace(c.state.Generation)
	page, err := c.reader(ctx, since, generation, limit)
	if err != nil {
		return nil, err
	}
	if page.ResetRequired {
		c.recordGapLocked("ble_generation_reset", generation, since, page.OldestID)
		generation = page.Generation
		c.state.Generation = generation
		c.state.SourceCursor = "0"
		if err := c.persistLocked(); err != nil {
			return nil, err
		}
		page, err = c.reader(ctx, "0", generation, limit)
		if err != nil {
			return nil, err
		}
	}
	if page.Truncated {
		c.recordGapLocked("ble_ring_truncated", generation, since, page.OldestID)
	}
	if page.Generation != "" {
		c.state.Generation = page.Generation
	}
	if page.OldestID != "" {
		c.state.OldestEventID = page.OldestID
	}

	accepted := make([]NotificationRecord, 0, len(page.Events))
	for _, event := range page.Events {
		if err := ctx.Err(); err != nil {
			return accepted, err
		}
		event = sanitizeNotificationEvent(event)
		fingerprint := notificationEventFingerprint(event)
		if _, exists := c.fingerprints[fingerprint]; exists {
			if cursor, ok := parseNotificationCursor(event.ID); ok && cursor > parseCursorOrZero(c.state.SourceCursor) {
				c.state.SourceCursor = strconv.FormatUint(cursor, 10)
			}
			continue
		}
		contextCursor := parseCursorOrZero(c.state.StoredCursor) + 1
		record := NotificationRecord{
			NotificationEvent: event,
			ContextID:         strconv.FormatUint(contextCursor, 10),
			Generation:        c.state.Generation,
		}
		if err := c.appendEventLocked(record); err != nil {
			return accepted, err
		}
		c.fingerprints[fingerprint] = record.ContextID
		pruneNotificationFingerprints(c.fingerprints)
		accepted = append(accepted, record)
		c.state.StoredCursor = record.ContextID
		if cursor, ok := parseNotificationCursor(event.ID); ok {
			c.state.SourceCursor = strconv.FormatUint(cursor, 10)
		}
		c.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if len(page.Events) == 0 && page.LastID != "" {
		if cursor, ok := parseNotificationCursor(page.LastID); ok && cursor > parseCursorOrZero(c.state.SourceCursor) {
			c.state.SourceCursor = strconv.FormatUint(cursor, 10)
		}
	}
	if err := c.persistLocked(); err != nil {
		return accepted, err
	}
	return accepted, nil
}

// Query returns persisted notification records. It is intentionally separate
// from Consume and ReadPending: shell/diagnostic callers can inspect the
// original event log without contacting ble_service or advancing state.
func (c *NotificationContext) Query(ctx context.Context, query NotificationQuery) ([]NotificationRecord, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return queryNotificationEvents(ctx, c.eventsDir, query)
}

// QueryNotificationRecords is the read-only entry point for diagnostics and
// shell commands. memoryDir is the agent memory directory, not the data root.
func QueryNotificationRecords(ctx context.Context, memoryDir string, query NotificationQuery) ([]NotificationRecord, error) {
	if strings.TrimSpace(memoryDir) == "" {
		return nil, fmt.Errorf("notification memory directory is required")
	}
	return queryNotificationEvents(ctx, filepath.Join(memoryDir, "notifications", "events"), query)
}

func queryNotificationEvents(ctx context.Context, eventsDir string, query NotificationQuery) ([]NotificationRecord, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if query.Limit <= 0 {
		query.Limit = defaultNotificationConsumeLimit
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	since := parseCursorOrZero(query.Since)
	if strings.TrimSpace(query.Since) != "" {
		if _, ok := parseNotificationCursor(query.Since); !ok {
			return nil, fmt.Errorf("notification query since must be a non-negative decimal cursor")
		}
	}
	date := strings.TrimSpace(query.Date)
	if date != "" {
		if _, err := time.Parse("2006-01-02", date); err != nil {
			return nil, fmt.Errorf("notification query date must be YYYY-MM-DD: %w", err)
		}
	}
	app := strings.ToLower(strings.TrimSpace(query.AppIdentifier))
	textQuery := strings.ToLower(strings.TrimSpace(query.Text))

	entries, err := os.ReadDir(eventsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	all := make([]NotificationRecord, 0)
	for _, name := range files {
		records, err := readNotificationRecordFile(filepath.Join(eventsDir, name))
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if err := ctx.Err(); err != nil {
				return all, err
			}
			cursor, ok := parseNotificationCursor(record.ContextID)
			if query.Since != "" && (!ok || cursor <= since) {
				continue
			}
			if date != "" && notificationEventDate(record.NotificationEvent) != date {
				continue
			}
			if app != "" && !strings.EqualFold(strings.TrimSpace(record.AppIdentifier), app) {
				continue
			}
			if textQuery != "" && !notificationEventContains(record.NotificationEvent, textQuery) {
				continue
			}
			all = append(all, record)
		}
	}
	// Files are date-sharded, but cursor order is the source of truth.
	sort.SliceStable(all, func(i, j int) bool {
		left, lok := parseNotificationCursor(all[i].ContextID)
		right, rok := parseNotificationCursor(all[j].ContextID)
		if lok && rok && left != right {
			return left < right
		}
		return all[i].ReceivedAt < all[j].ReceivedAt
	})
	if query.Since == "" && len(all) > query.Limit {
		all = all[len(all)-query.Limit:]
	} else if len(all) > query.Limit {
		all = all[:query.Limit]
	}
	return all, nil
}

func notificationEventDate(event ble.NotificationEvent) string {
	if ts, err := time.Parse(time.RFC3339Nano, event.ReceivedAt); err == nil {
		return ts.UTC().Format("2006-01-02")
	}
	return ""
}

func notificationEventContains(event ble.NotificationEvent, query string) bool {
	for _, value := range []string{event.Source, event.AppIdentifier, event.Title, event.Subtitle, event.Message, event.Category, event.Event} {
		if strings.Contains(strings.ToLower(value), query) {
			return true
		}
	}
	return false
}

// ReadPending reads persisted events after MemoryCursor. It never contacts
// ble_service and is safe to call while the producer is disconnected.
func (c *NotificationContext) ReadPending(ctx context.Context, limit int) ([]NotificationRecord, error) {
	if c == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if limit <= 0 {
		limit = defaultNotificationPendingLimit
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	return c.readPendingLocked(ctx, parseCursorOrZero(c.state.MemoryCursor), limit)
}

// CommitProcessed advances MemoryCursor after the corresponding Memory write
// succeeds. The caller must pass the contiguous batch returned by ReadPending.
func (c *NotificationContext) CommitProcessed(ctx context.Context, events []NotificationRecord) error {
	if c == nil || len(events) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ensureLoadedLocked(); err != nil {
		return err
	}
	current := parseCursorOrZero(c.state.MemoryCursor)
	for _, event := range events {
		cursor, ok := parseNotificationCursor(event.ContextID)
		if !ok || cursor <= current {
			continue
		}
		if cursor != current+1 {
			return fmt.Errorf("notification memory cursor gap: current=%d next=%d", current, cursor)
		}
		current = cursor
	}
	if current == parseCursorOrZero(c.state.MemoryCursor) {
		return nil
	}
	c.state.MemoryCursor = strconv.FormatUint(current, 10)
	c.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return c.persistLocked()
}

func (c *NotificationContext) readPendingLocked(ctx context.Context, cursor uint64, limit int) ([]NotificationRecord, error) {
	entries, err := os.ReadDir(c.eventsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl") {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	result := make([]NotificationRecord, 0, limit)
	for _, name := range files {
		records, err := readNotificationRecordFile(filepath.Join(c.eventsDir, name))
		if err != nil {
			return nil, err
		}
		for _, event := range records {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			eventCursor, ok := parseNotificationCursor(event.ContextID)
			if !ok || eventCursor <= cursor {
				continue
			}
			result = append(result, event)
			if len(result) >= limit {
				break
			}
		}
		if len(result) >= limit {
			break
		}
	}
	return result, nil
}

func (c *NotificationContext) appendEventLocked(event NotificationRecord) error {
	if err := os.MkdirAll(c.eventsDir, 0o755); err != nil {
		return err
	}
	name := notificationEventFileName(event.NotificationEvent)
	path := filepath.Join(c.eventsDir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err == nil {
		_, err = file.Write(append(data, '\n'))
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func readNotificationRecordFile(path string) ([]NotificationRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	if !bytes.HasSuffix(data, []byte{'\n'}) {
		lastNewline := bytes.LastIndexByte(data, '\n')
		tail := data[lastNewline+1:]
		var record NotificationRecord
		if err := json.Unmarshal(tail, &record); err != nil {
			if err := os.WriteFile(path, data[:lastNewline+1], 0o644); err != nil {
				return nil, fmt.Errorf("repair incomplete notification record %s: %w", path, err)
			}
			data = data[:lastNewline+1]
		} else {
			data = append(data, '\n')
			if err := os.WriteFile(path, data, 0o644); err != nil {
				return nil, fmt.Errorf("repair notification record newline %s: %w", path, err)
			}
		}
	}
	lines := bytes.Split(data, []byte{'\n'})
	result := make([]NotificationRecord, 0)
	for _, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record NotificationRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("decode notification record %s: %w", path, err)
		}
		result = append(result, record)
	}
	return result, nil
}

func (c *NotificationContext) load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensureLoadedLocked(); err != nil {
		return err
	}
	changed, err := c.recoverFromEventLogLocked()
	if err != nil || !changed {
		return err
	}
	return c.persistLocked()
}

func (c *NotificationContext) ensureLoadedLocked() error {
	if c.state.Version == 0 {
		c.state.Version = 1
	}
	if data, err := os.ReadFile(c.statePath); err == nil {
		var state NotificationContextState
		if err := json.Unmarshal(data, &state); err != nil {
			return fmt.Errorf("decode notification context state: %w", err)
		}
		if state.Version == 0 {
			state.Version = 1
		}
		c.state = state
	} else if !os.IsNotExist(err) {
		return err
	}
	if data, err := os.ReadFile(c.fingerPath); err == nil {
		var file notificationFingerprintFile
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("decode notification fingerprints: %w", err)
		}
		if file.Fingerprints != nil {
			c.fingerprints = file.Fingerprints
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if c.fingerprints == nil {
		c.fingerprints = map[string]string{}
	}
	pruneNotificationFingerprints(c.fingerprints)
	return nil
}

// recoverFromEventLogLocked rebuilds the dedupe window and durable cursors
// from event shards after a process restart or partial state write.
func (c *NotificationContext) recoverFromEventLogLocked() (bool, error) {
	entries, err := os.ReadDir(c.eventsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	fingerprintCount := len(c.fingerprints)
	storedCursor := parseCursorOrZero(c.state.StoredCursor)
	latestCursor := storedCursor
	var latest NotificationRecord
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		records, err := readNotificationRecordFile(filepath.Join(c.eventsDir, entry.Name()))
		if err != nil {
			return false, err
		}
		for _, record := range records {
			cursor, ok := parseNotificationCursor(record.ContextID)
			if !ok {
				continue
			}
			c.fingerprints[notificationEventFingerprint(record.NotificationEvent)] = record.ContextID
			if cursor > latestCursor {
				latestCursor = cursor
				latest = record
			}
		}
	}
	pruneNotificationFingerprints(c.fingerprints)
	if latestCursor == storedCursor {
		return len(c.fingerprints) != fingerprintCount, nil
	}
	c.state.StoredCursor = strconv.FormatUint(latestCursor, 10)
	c.state.SourceCursor = normalizeCursor(latest.ID)
	if latest.Generation != "" {
		c.state.Generation = latest.Generation
	}
	c.state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	return true, nil
}

func (c *NotificationContext) persistLocked() error {
	if err := os.MkdirAll(c.rootDir, 0o755); err != nil {
		return err
	}
	if c.state.Version == 0 {
		c.state.Version = 1
	}
	state, err := json.MarshalIndent(c.state, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.statePath, append(state, '\n'), 0o644); err != nil {
		return err
	}
	fingerprints, err := json.MarshalIndent(notificationFingerprintFile{Version: 1, Fingerprints: c.fingerprints}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(c.fingerPath, append(fingerprints, '\n'), 0o644)
}

func (c *NotificationContext) recordGapLocked(reason, generation, fromID, toID string) {
	c.state.GapCount++
	c.state.Gaps = append(c.state.Gaps, NotificationGap{
		At:         time.Now().UTC().Format(time.RFC3339Nano),
		Reason:     reason,
		Generation: generation,
		FromID:     fromID,
		ToID:       toID,
	})
	if len(c.state.Gaps) > 32 {
		c.state.Gaps = append([]NotificationGap(nil), c.state.Gaps[len(c.state.Gaps)-32:]...)
	}
}

func sanitizeNotificationEvent(event ble.NotificationEvent) ble.NotificationEvent {
	event.ID = strings.TrimSpace(event.ID)
	event.Source = truncateNotificationText(strings.TrimSpace(event.Source), maxNotificationStoredText)
	event.SourceID = truncateNotificationText(strings.TrimSpace(event.SourceID), maxNotificationStoredText)
	event.SourceEventID = truncateNotificationText(strings.TrimSpace(event.SourceEventID), maxNotificationStoredText)
	event.DeviceID = truncateNotificationText(strings.TrimSpace(event.DeviceID), maxNotificationStoredText)
	event.AppIdentifier = truncateNotificationText(strings.TrimSpace(event.AppIdentifier), maxNotificationStoredApp)
	event.Title = truncateNotificationText(strings.TrimSpace(event.Title), maxNotificationStoredText)
	event.Subtitle = truncateNotificationText(strings.TrimSpace(event.Subtitle), maxNotificationStoredText)
	event.Message = truncateNotificationText(strings.TrimSpace(event.Message), maxNotificationStoredText)
	event.Category = truncateNotificationText(strings.TrimSpace(event.Category), maxNotificationStoredText)
	event.Date = truncateNotificationText(strings.TrimSpace(event.Date), maxNotificationStoredText)
	event.MetadataError = truncateNotificationText(strings.TrimSpace(event.MetadataError), maxNotificationStoredText)
	for i := range event.Flags {
		event.Flags[i] = truncateNotificationText(strings.TrimSpace(event.Flags[i]), 128)
	}
	return event
}

func truncateNotificationText(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func notificationEventFingerprint(event ble.NotificationEvent) string {
	key := strings.Join([]string{event.DeviceID, event.Source, event.SourceEventID}, "\x00")
	if event.SourceEventID == "" {
		key = strings.Join([]string{event.DeviceID, event.Source, event.SourceID, strconv.FormatUint(uint64(event.NotificationUID), 10), event.Event, event.AppIdentifier, event.Title, event.Message, event.ReceivedAt}, "\x00")
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func notificationEventFileName(event ble.NotificationEvent) string {
	if ts, err := time.Parse(time.RFC3339Nano, event.ReceivedAt); err == nil {
		return ts.UTC().Format("2006-01-02") + ".jsonl"
	}
	return "unknown.jsonl"
}

func parseNotificationCursor(value string) (uint64, bool) {
	parsed, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	return parsed, err == nil
}

func parseCursorOrZero(value string) uint64 {
	parsed, _ := parseNotificationCursor(value)
	return parsed
}

func normalizeCursor(value string) string {
	if parsed, ok := parseNotificationCursor(value); ok {
		return strconv.FormatUint(parsed, 10)
	}
	return "0"
}

func cloneNotificationContextState(state NotificationContextState) NotificationContextState {
	state.Gaps = append([]NotificationGap(nil), state.Gaps...)
	return state
}

func pruneNotificationFingerprints(fingerprints map[string]string) {
	if len(fingerprints) <= maxNotificationFingerprints {
		return
	}
	type fingerprintCursor struct {
		fingerprint string
		cursor      uint64
	}
	ordered := make([]fingerprintCursor, 0, len(fingerprints))
	for fingerprint, contextID := range fingerprints {
		ordered = append(ordered, fingerprintCursor{fingerprint: fingerprint, cursor: parseCursorOrZero(contextID)})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].cursor < ordered[j].cursor })
	removeCount := len(ordered) - trimNotificationFingerprintsTo
	for _, item := range ordered[:removeCount] {
		delete(fingerprints, item.fingerprint)
	}
}
