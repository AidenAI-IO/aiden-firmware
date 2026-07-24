package agent

import "strings"

const (
	keyboardLayoutQWERTY = "qwerty"
	keyboardLayoutAZERTY = "azerty"
	keyboardLayoutQWERTZ = "qwertz"

	defaultKeyboardLayout = keyboardLayoutQWERTY

	hidModifierLeftShift = 0x02
	hidModifierRightAlt  = 0x40
)

type keyboardLayoutDefinition struct {
	value string
	label string
}

var keyboardLayoutDefinitions = []keyboardLayoutDefinition{
	{value: keyboardLayoutQWERTY, label: "QWERTY"},
	{value: keyboardLayoutAZERTY, label: "AZERTY"},
	{value: keyboardLayoutQWERTZ, label: "QWERTZ"},
}

type hidKeyStroke struct {
	modifier uint8
	usage    uint8
}

var keyboardTapTextKeyMap = map[string]byte{
	"space":      ' ',
	"minus":      '-',
	"equal":      '=',
	"leftbrace":  '[',
	"rightbrace": ']',
	"backslash":  '\\',
	"semicolon":  ';',
	"apostrophe": '\'',
	"grave":      '`',
	"comma":      ',',
	"dot":        '.',
	"slash":      '/',
}

var qwertySymbolKeyStrokes = map[byte]hidKeyStroke{
	' ':  {usage: 0x2c},
	'\n': {usage: 0x28},
	'\r': {usage: 0x28},
	'\t': {usage: 0x2b},
	'-':  {usage: 0x2d},
	'=':  {usage: 0x2e},
	'[':  {usage: 0x2f},
	']':  {usage: 0x30},
	'\\': {usage: 0x31},
	';':  {usage: 0x33},
	'\'': {usage: 0x34},
	'`':  {usage: 0x35},
	',':  {usage: 0x36},
	'.':  {usage: 0x37},
	'/':  {usage: 0x38},
	'!':  {modifier: hidModifierLeftShift, usage: 0x1e},
	'@':  {modifier: hidModifierLeftShift, usage: 0x1f},
	'#':  {modifier: hidModifierLeftShift, usage: 0x20},
	'$':  {modifier: hidModifierLeftShift, usage: 0x21},
	'%':  {modifier: hidModifierLeftShift, usage: 0x22},
	'^':  {modifier: hidModifierLeftShift, usage: 0x23},
	'&':  {modifier: hidModifierLeftShift, usage: 0x24},
	'*':  {modifier: hidModifierLeftShift, usage: 0x25},
	'(':  {modifier: hidModifierLeftShift, usage: 0x26},
	')':  {modifier: hidModifierLeftShift, usage: 0x27},
	'_':  {modifier: hidModifierLeftShift, usage: 0x2d},
	'+':  {modifier: hidModifierLeftShift, usage: 0x2e},
	'{':  {modifier: hidModifierLeftShift, usage: 0x2f},
	'}':  {modifier: hidModifierLeftShift, usage: 0x30},
	'|':  {modifier: hidModifierLeftShift, usage: 0x31},
	':':  {modifier: hidModifierLeftShift, usage: 0x33},
	'"':  {modifier: hidModifierLeftShift, usage: 0x34},
	'~':  {modifier: hidModifierLeftShift, usage: 0x35},
	'<':  {modifier: hidModifierLeftShift, usage: 0x36},
	'>':  {modifier: hidModifierLeftShift, usage: 0x37},
	'?':  {modifier: hidModifierLeftShift, usage: 0x38},
}

var azertyLetterUsageOverrides = map[byte]uint8{
	'a': 0x14,
	'q': 0x04,
	'w': 0x1d,
	'z': 0x1a,
	'm': 0x33,
}

var azertySymbolKeyStrokes = map[byte]hidKeyStroke{
	' ':  {usage: 0x2c},
	'\n': {usage: 0x28},
	'\r': {usage: 0x28},
	'\t': {usage: 0x2b},
	'&':  {usage: 0x1e},
	'~':  {modifier: hidModifierRightAlt, usage: 0x1f},
	'"':  {usage: 0x20},
	'#':  {modifier: hidModifierRightAlt, usage: 0x20},
	'\'': {usage: 0x21},
	'{':  {modifier: hidModifierRightAlt, usage: 0x21},
	'(':  {usage: 0x22},
	'[':  {modifier: hidModifierRightAlt, usage: 0x22},
	'-':  {usage: 0x23},
	'|':  {modifier: hidModifierRightAlt, usage: 0x23},
	'`':  {modifier: hidModifierRightAlt, usage: 0x24},
	'_':  {usage: 0x25},
	'\\': {modifier: hidModifierRightAlt, usage: 0x25},
	'^':  {modifier: hidModifierRightAlt, usage: 0x26},
	'@':  {modifier: hidModifierRightAlt, usage: 0x27},
	')':  {usage: 0x2d},
	']':  {modifier: hidModifierRightAlt, usage: 0x2d},
	'=':  {usage: 0x2e},
	'+':  {modifier: hidModifierLeftShift, usage: 0x2e},
	'}':  {modifier: hidModifierRightAlt, usage: 0x2e},
	'$':  {usage: 0x30},
	'*':  {usage: 0x31},
	'%':  {modifier: hidModifierLeftShift, usage: 0x34},
	',':  {usage: 0x10},
	'?':  {modifier: hidModifierLeftShift, usage: 0x10},
	';':  {usage: 0x36},
	'.':  {modifier: hidModifierLeftShift, usage: 0x36},
	':':  {usage: 0x37},
	'/':  {modifier: hidModifierLeftShift, usage: 0x37},
	'!':  {usage: 0x38},
	'<':  {usage: 0x64},
	'>':  {modifier: hidModifierLeftShift, usage: 0x64},
}

var qwertzSymbolKeyStrokes = map[byte]hidKeyStroke{
	'!':  {modifier: hidModifierLeftShift, usage: 0x1e},
	'"':  {modifier: hidModifierLeftShift, usage: 0x1f},
	'$':  {modifier: hidModifierLeftShift, usage: 0x21},
	'%':  {modifier: hidModifierLeftShift, usage: 0x22},
	'&':  {modifier: hidModifierLeftShift, usage: 0x23},
	'/':  {modifier: hidModifierLeftShift, usage: 0x24},
	'{':  {modifier: hidModifierRightAlt, usage: 0x24},
	'(':  {modifier: hidModifierLeftShift, usage: 0x25},
	'[':  {modifier: hidModifierRightAlt, usage: 0x25},
	')':  {modifier: hidModifierLeftShift, usage: 0x26},
	']':  {modifier: hidModifierRightAlt, usage: 0x26},
	'=':  {modifier: hidModifierLeftShift, usage: 0x27},
	'}':  {modifier: hidModifierRightAlt, usage: 0x27},
	'?':  {modifier: hidModifierLeftShift, usage: 0x2d},
	'\\': {modifier: hidModifierRightAlt, usage: 0x2d},
	'+':  {usage: 0x30},
	'*':  {modifier: hidModifierLeftShift, usage: 0x30},
	'~':  {modifier: hidModifierRightAlt, usage: 0x30},
	'#':  {usage: 0x31},
	'\'': {modifier: hidModifierLeftShift, usage: 0x31},
	',':  {usage: 0x36},
	';':  {modifier: hidModifierLeftShift, usage: 0x36},
	'.':  {usage: 0x37},
	':':  {modifier: hidModifierLeftShift, usage: 0x37},
	'-':  {usage: 0x38},
	'_':  {modifier: hidModifierLeftShift, usage: 0x38},
	'<':  {usage: 0x64},
	'>':  {modifier: hidModifierLeftShift, usage: 0x64},
	'|':  {modifier: hidModifierRightAlt, usage: 0x64},
	'@':  {modifier: hidModifierRightAlt, usage: 0x14},
}

func keyboardLayoutKeyStroke(layout string, ch byte) (hidKeyStroke, bool) {
	normalized, _ := normalizeKeyboardLayout(layout)
	switch normalized {
	case keyboardLayoutAZERTY:
		return azertyKeyStroke(ch)
	case keyboardLayoutQWERTZ:
		return qwertzKeyStroke(ch)
	default:
		return qwertyKeyStroke(ch)
	}
}

func normalizeKeyboardLayout(value string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return defaultKeyboardLayout, true
	}
	for _, layout := range keyboardLayoutDefinitions {
		if normalized == layout.value {
			return layout.value, true
		}
	}
	return defaultKeyboardLayout, false
}

func keyboardLayoutValuesText() string {
	values := make([]string, 0, len(keyboardLayoutDefinitions))
	for _, layout := range keyboardLayoutDefinitions {
		values = append(values, layout.value)
	}
	return strings.Join(values[:len(values)-1], ", ") + ", or " + values[len(values)-1]
}

func keyboardLayoutTapKeyStroke(layout, key string) (hidKeyStroke, bool) {
	if len(key) == 1 {
		return keyboardLayoutKeyStroke(layout, key[0])
	}
	if ch, ok := keyboardTapTextKeyMap[key]; ok {
		return keyboardLayoutKeyStroke(layout, ch)
	}
	usage, ok := hidKeyboardMap[key]
	return hidKeyStroke{usage: usage}, ok
}

func qwertyKeyStroke(ch byte) (hidKeyStroke, bool) {
	switch {
	case ch >= 'a' && ch <= 'z':
		return hidKeyStroke{usage: 0x04 + (ch - 'a')}, true
	case ch >= 'A' && ch <= 'Z':
		return hidKeyStroke{modifier: hidModifierLeftShift, usage: 0x04 + (ch - 'A')}, true
	case ch >= '1' && ch <= '9':
		return hidKeyStroke{usage: 0x1e + (ch - '1')}, true
	case ch == '0':
		return hidKeyStroke{usage: 0x27}, true
	}

	stroke, ok := mapKeyboardSymbol(ch, qwertySymbolKeyStrokes)
	return stroke, ok
}

func azertyKeyStroke(ch byte) (hidKeyStroke, bool) {
	if ch >= 'A' && ch <= 'Z' {
		stroke, ok := azertyKeyStroke(ch + ('a' - 'A'))
		stroke.modifier |= hidModifierLeftShift
		return stroke, ok
	}
	if ch >= 'a' && ch <= 'z' {
		if usage, ok := azertyLetterUsageOverrides[ch]; ok {
			return hidKeyStroke{usage: usage}, true
		}
		return qwertyKeyStroke(ch)
	}
	if ch >= '1' && ch <= '9' {
		return hidKeyStroke{modifier: hidModifierLeftShift, usage: 0x1e + (ch - '1')}, true
	}
	if ch == '0' {
		return hidKeyStroke{modifier: hidModifierLeftShift, usage: 0x27}, true
	}

	return mapKeyboardSymbol(ch, azertySymbolKeyStrokes)
}

func qwertzKeyStroke(ch byte) (hidKeyStroke, bool) {
	if ch >= 'A' && ch <= 'Z' {
		stroke, ok := qwertzKeyStroke(ch + ('a' - 'A'))
		stroke.modifier |= hidModifierLeftShift
		return stroke, ok
	}
	if ch == 'y' {
		return hidKeyStroke{usage: 0x1d}, true
	}
	if ch == 'z' {
		return hidKeyStroke{usage: 0x1c}, true
	}
	if (ch >= 'a' && ch <= 'x') || (ch >= '0' && ch <= '9') || ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t' {
		return qwertyKeyStroke(ch)
	}

	return mapKeyboardSymbol(ch, qwertzSymbolKeyStrokes)
}

func mapKeyboardSymbol(ch byte, symbols map[byte]hidKeyStroke) (hidKeyStroke, bool) {
	stroke, ok := symbols[ch]
	return stroke, ok
}
