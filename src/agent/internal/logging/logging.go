// Package logging emits the Agent's unified log records:
//
//	<UTC timestamp> [LEVEL][service][component] <event> [fields...]
//
// Message-style records come from LogMessage and its Debugf/Infof/Warnf/Errorf
// wrappers; they always carry the event name "log_message" and a quoted message
// field. Event-style records come from LogEvent and carry an explicit event name
// with key/value fields. The caller always states the severity: nothing is
// inferred from the message text.
package logging

import (
	"fmt"
	"io"
	"os"
	"regexp"
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

// messageEvent is the event name carried by printf-style message records. The
// caller states the severity explicitly, so no event is derived from the text.
const messageEvent = "log_message"

// Field is a structured key/value attached to one log event.
type Field struct {
	Key   string
	Value any
}

var outputState = struct {
	sync.Mutex
	writer  io.Writer
	minimum Level
}{writer: os.Stderr, minimum: Debug}

var structuredLinePattern = regexp.MustCompile(
	`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z \[(DEBUG|INFO|WARN|ERROR)\]\[[a-z0-9_]+\]\[[a-z0-9_]+\] [a-z0-9_]+(?: |$)`,
)

// SetOutput changes the destination used by the logging helpers.
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

// SetMinimumLevel changes the minimum severity emitted by the logging helpers.
// It returns a restore function for tests and scoped callers.
func SetMinimumLevel(level Level) func() {
	outputState.Lock()
	previous := outputState.minimum
	outputState.minimum = NormalizeLevel(level)
	outputState.Unlock()
	return func() {
		outputState.Lock()
		outputState.minimum = previous
		outputState.Unlock()
	}
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

// FormatMessageAt formats an already rendered message with an explicit
// severity. Unlike the removed legacy adapters it never inspects the text.
func FormatMessageAt(now time.Time, level Level, service, component, message string) string {
	return formatMessageRecord(now, level, service, component, messageEvent, sanitizeMessage(message))
}

// LogMessage writes one printf-style message at an explicit severity. The
// caller supplies severity, service, and component; nothing is inferred from
// the message text.
func LogMessage(level Level, service, component, format string, args ...any) error {
	outputState.Lock()
	defer outputState.Unlock()
	if !levelAllowed(level, outputState.minimum) {
		return nil
	}
	record := FormatMessageAt(time.Now(), level, service, component, fmt.Sprintf(format, args...))
	_, err := io.WriteString(outputState.writer, record+"\n")
	return err
}

// Debugf logs a printf-style message at DEBUG severity.
func Debugf(service, component, format string, args ...any) {
	_ = LogMessage(Debug, service, component, format, args...)
}

// Infof logs a printf-style message at INFO severity.
func Infof(service, component, format string, args ...any) {
	_ = LogMessage(Info, service, component, format, args...)
}

// Warnf logs a printf-style message at WARN severity.
func Warnf(service, component, format string, args ...any) {
	_ = LogMessage(Warn, service, component, format, args...)
}

// Errorf logs a printf-style message at ERROR severity.
func Errorf(service, component, format string, args ...any) {
	_ = LogMessage(Error, service, component, format, args...)
}

// Fatalf logs a printf-style message at ERROR severity and exits with status 1,
// matching the standard library behavior it replaces.
func Fatalf(service, component, format string, args ...any) {
	_ = LogMessage(Error, service, component, format, args...)
	os.Exit(1)
}

// LogEvent writes one structured event to the package output.
func LogEvent(level Level, service, component, event string, fields ...Field) error {
	outputState.Lock()
	defer outputState.Unlock()
	if !levelAllowed(level, outputState.minimum) {
		return nil
	}
	record := FormatEventAt(time.Now(), level, service, component, event, fields...)
	_, err := io.WriteString(outputState.writer, record+"\n")
	return err
}

// IsStructuredLine reports whether line already uses the common event format.
func IsStructuredLine(line string) bool {
	return structuredLinePattern.MatchString(line)
}

func levelAllowed(level, minimum Level) bool {
	rank := func(value Level) int {
		switch NormalizeLevel(value) {
		case Debug:
			return 0
		case Info:
			return 1
		case Warn:
			return 2
		case Error:
			return 3
		default:
			return 1
		}
	}
	return rank(level) >= rank(minimum)
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
	builder.WriteString("][")
	builder.WriteString(NormalizeIdentifier(service, "unknown"))
	builder.WriteString("][")
	builder.WriteString(NormalizeIdentifier(component, "runtime"))
	builder.WriteString("] ")
	builder.WriteString(NormalizeIdentifier(event, messageEvent))
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

// ParseLevel recognizes the supported severity names.
func ParseLevel(value string) (Level, bool) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "DEBUG":
		return Debug, true
	case "INFO":
		return Info, true
	case "WARN":
		return Warn, true
	case "ERROR":
		return Error, true
	default:
		return "", false
	}
}
