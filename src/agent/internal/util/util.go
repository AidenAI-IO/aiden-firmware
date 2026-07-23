package util

import "fmt"

func UsageMetricInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func STag(tag string, content string) string {
	return fmt.Sprintf("<%s>\n%s\n</%s>", tag, content, tag)
}
