package logging

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Level is the normalized runtime log severity.
type Level string

const (
	Debug Level = "DEBUG"
	Info  Level = "INFO"
	Warn  Level = "WARN"
	Error Level = "ERROR"
)

// Field is a structured key/value attached to one log event.
type Field struct {
	Key   string
	Value any
}

var outputState = struct {
	sync.Mutex
	writer io.Writer
}{writer: os.Stderr}

// SetOutput changes the destination used by direct structured logging helpers.
// It returns a restore function intended for tests and temporary redirection.
func SetOutput(writer io.Writer) func() {
	if writer == nil {
		writer = io.Discard
	}
	outputState.Lock()
	previous := outputState.writer
	outputState.writer = writer
	outputState.Unlock()
	return func() {
		outputState.Lock()
		outputState.writer = previous
		outputState.Unlock()
	}
}

// InstallStandard routes the process-wide standard logger through the unified
// formatter. Existing log.Printf/log.Println call sites therefore produce the
// common line prefix while they are migrated to structured event calls.
func InstallStandard(service string, writer io.Writer) {
	if writer == nil {
		writer = os.Stderr
	}
	log.SetOutput(NewLegacyWriter(writer, service, "runtime", Info))
	log.SetFlags(0)
	log.SetPrefix("")
}

// NewLegacyLogger adapts a standard library *log.Logger to the unified format.
func NewLegacyLogger(writer io.Writer, service, component string, level Level) *log.Logger {
	return log.New(NewLegacyWriter(writer, service, component, level), "", 0)
}

// NewLegacyWriter wraps legacy free-text log messages in the unified prefix.
// Embedded newlines are emitted as separate, fully formatted records; blank
// separator lines are discarded.
func NewLegacyWriter(writer io.Writer, service, component string, level Level) io.Writer {
	if writer == nil {
		writer = io.Discard
	}
	return &legacyWriter{
		writer:    writer,
		service:   NormalizeIdentifier(service, "unknown"),
		component: NormalizeIdentifier(component, "runtime"),
		level:     NormalizeLevel(level),
		now:       time.Now,
	}
}

type legacyWriter struct {
	mu        sync.Mutex
	writer    io.Writer
	service   string
	component string
	level     Level
	now       func() time.Time
}

func (w *legacyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	callerComponent := callerComponent(w.component)
	text := strings.ReplaceAll(string(p), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		record := FormatLegacyAt(w.now(), w.level, w.service, callerComponent, line)
		if _, err := io.WriteString(w.writer, record+"\n"); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// FormatLegacyfAt formats a printf-style legacy call. The event is derived
// from the stable format template while the message contains rendered values.
func FormatLegacyfAt(now time.Time, level Level, service, component, format string, args ...any) string {
	templateMeta := parseLegacy(level, service, component, format)
	renderedMeta := parseLegacy(level, service, component, fmt.Sprintf(format, args...))
	if renderedMeta.level == Info && templateMeta.level != Info {
		renderedMeta.level = templateMeta.level
	}
	if renderedMeta.component == NormalizeIdentifier(component, "runtime") && templateMeta.component != "" {
		renderedMeta.component = templateMeta.component
	}
	event := deriveEvent(templateMeta.message, renderedMeta.level)
	return formatMessageRecord(now, renderedMeta.level, service, renderedMeta.component, event, renderedMeta.message)
}

// FormatLegacyAt formats an already rendered legacy message.
func FormatLegacyAt(now time.Time, level Level, service, component, message string) string {
	meta := parseLegacy(level, service, component, message)
	event := deriveEvent(meta.message, meta.level)
	return formatMessageRecord(now, meta.level, service, meta.component, event, meta.message)
}

// FormatEventAt formats an event with explicit structured fields.
func FormatEventAt(now time.Time, level Level, service, component, event string, fields ...Field) string {
	var builder strings.Builder
	writePrefix(&builder, now, level, service, component, event)
	for _, field := range fields {
		key := NormalizeIdentifier(field.Key, "field")
		builder.WriteByte(' ')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(formatValue(field.Value))
	}
	return builder.String()
}

// LogEvent writes one structured event to the package output.
func LogEvent(level Level, service, component, event string, fields ...Field) error {
	record := FormatEventAt(time.Now(), level, service, component, event, fields...)
	outputState.Lock()
	defer outputState.Unlock()
	_, err := io.WriteString(outputState.writer, record+"\n")
	return err
}

type legacyMeta struct {
	level     Level
	component string
	message   string
}

func parseLegacy(defaultLevel Level, service, fallbackComponent, input string) legacyMeta {
	level := NormalizeLevel(defaultLevel)
	component := NormalizeIdentifier(fallbackComponent, "runtime")
	message := sanitizeMessage(input)

	for strings.HasPrefix(message, "[") {
		end := strings.IndexByte(message, ']')
		if end <= 1 {
			break
		}
		tag := strings.TrimSpace(message[1:end])
		message = strings.TrimSpace(message[end+1:])
		if parsed, ok := ParseLevel(tag); ok {
			level = parsed
			continue
		}
		component = NormalizeIdentifier(tag, component)
	}

	normalizedService := NormalizeIdentifier(service, "unknown")
	lowerMessage := strings.ToLower(message)
	if strings.HasPrefix(lowerMessage, normalizedService+":") {
		message = strings.TrimSpace(message[len(normalizedService)+1:])
	} else if strings.HasPrefix(lowerMessage, normalizedService+" ") {
		message = strings.TrimSpace(message[len(normalizedService):])
	}

	if colon := strings.IndexByte(message, ':'); colon > 0 {
		candidate := strings.TrimSpace(message[:colon])
		if isLegacyComponentPrefix(candidate) {
			component = NormalizeIdentifier(candidate, component)
			message = strings.TrimSpace(message[colon+1:])
		}
	}
	if level == Info {
		level = inferLegacyLevel(message)
	}

	return legacyMeta{level: level, component: component, message: sanitizeMessage(message)}
}

func formatMessageRecord(now time.Time, level Level, service, component, event, message string) string {
	var builder strings.Builder
	writePrefix(&builder, now, level, service, component, event)
	if message != "" {
		builder.WriteString(" message=")
		builder.WriteString(strconv.Quote(message))
	}
	return builder.String()
}

func writePrefix(builder *strings.Builder, now time.Time, level Level, service, component, event string) {
	builder.WriteString(now.UTC().Format("2006-01-02T15:04:05Z"))
	builder.WriteString(" [")
	builder.WriteString(string(NormalizeLevel(level)))
	builder.WriteString("] [")
	builder.WriteString(NormalizeIdentifier(service, "unknown"))
	builder.WriteString("] [")
	builder.WriteString(NormalizeIdentifier(component, "runtime"))
	builder.WriteString("] ")
	builder.WriteString(NormalizeIdentifier(event, "log_message"))
}

func formatValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(typed)
	case string:
		if isSimpleValue(typed) {
			return typed
		}
		return strconv.Quote(sanitizeMessage(typed))
	case error:
		return strconv.Quote(sanitizeMessage(typed.Error()))
	case fmt.Stringer:
		text := sanitizeMessage(typed.String())
		if isSimpleValue(text) {
			return text
		}
		return strconv.Quote(text)
	default:
		text := fmt.Sprint(value)
		if isSimpleValue(text) {
			return text
		}
		return strconv.Quote(sanitizeMessage(text))
	}
}

func isSimpleValue(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if unicode.IsSpace(r) || r == '"' || r == '\\' || r == '=' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func sanitizeMessage(message string) string {
	message = strings.ReplaceAll(message, "\r\n", "\n")
	message = strings.ReplaceAll(message, "\r", "\n")
	return strings.TrimSpace(message)
}

// NormalizeIdentifier converts service/component/event names to lowercase
// snake_case using only ASCII letters, digits, and underscores.
func NormalizeIdentifier(value, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	var builder strings.Builder
	underscore := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			builder.WriteRune(r)
			underscore = false
			continue
		}
		if builder.Len() > 0 && !underscore {
			builder.WriteByte('_')
			underscore = true
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		result = strings.Trim(strings.ToLower(fallback), "_")
	}
	if result == "" {
		return "unknown"
	}
	return result
}

// NormalizeLevel returns one of the four supported severities.
func NormalizeLevel(level Level) Level {
	if parsed, ok := ParseLevel(string(level)); ok {
		return parsed
	}
	return Info
}

// ParseLevel recognizes legacy and normalized severity names.
func ParseLevel(value string) (Level, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return Debug, true
	case "INFO":
		return Info, true
	case "WARN", "WARNING":
		return Warn, true
	case "ERROR", "ERR", "FATAL":
		return Error, true
	default:
		return "", false
	}
}

func deriveEvent(message string, level Level) string {
	message = strings.TrimSpace(message)
	if message == "" {
		if level == Error {
			return "operation_failed"
		}
		return "log_message"
	}
	if colon := strings.IndexByte(message, ':'); colon > 0 {
		message = message[:colon]
	}
	if percent := strings.IndexByte(message, '%'); percent >= 0 {
		message = message[:percent]
	}
	words := strings.Fields(message)
	if len(words) > 6 {
		words = words[:6]
	}
	for len(words) > 0 {
		last := words[len(words)-1]
		if strings.Contains(last, "=") || containsDigit(last) {
			words = words[:len(words)-1]
			continue
		}
		break
	}
	event := NormalizeIdentifier(strings.Join(words, " "), "")
	if event == "unknown" || event == "" {
		if level == Error {
			return "operation_failed"
		}
		return "log_message"
	}
	return event
}

func containsDigit(value string) bool {
	for _, r := range value {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func isLegacyComponentPrefix(value string) bool {
	if value == "" || value != strings.ToLower(value) || strings.ContainsAny(value, " \t/\\") {
		return false
	}
	knownSingleWord := map[string]bool{
		"shell": true,
	}
	if !strings.ContainsAny(value, "_-.") && !knownSingleWord[value] {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func inferLegacyLevel(message string) Level {
	lower := strings.ToLower(message)
	for _, marker := range []string{" failed", "failure", " error", "panic", "timed out", "timeout"} {
		if strings.Contains(" "+lower, marker) {
			return Warn
		}
	}
	return Info
}

func callerComponent(fallback string) string {
	pcs := make([]uintptr, 24)
	count := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:count])
	for {
		frame, more := frames.Next()
		path := filepath.ToSlash(frame.File)
		if !strings.HasSuffix(path, "/log/log.go") &&
			!strings.Contains(path, "/internal/logging/") &&
			!strings.HasSuffix(path, "/agent/logger.go") {
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if name == "main" {
				name = "daemon"
			}
			return NormalizeIdentifier(name, fallback)
		}
		if !more {
			break
		}
	}
	return NormalizeIdentifier(fallback, "runtime")
}
