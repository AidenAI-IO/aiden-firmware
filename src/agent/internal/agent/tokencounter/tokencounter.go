package tokencounter

import (
	"unicode"
	"unicode/utf8"
)

// EstimateImageTokens is the number of tokens estimated for a single image, because in our agent, we usally use fixed size image, so we can use a constant value to estimate the tokens.
const EstimateImageTokens = 1000

func EstimateTextTokens(content string) int {
	if len(content) == 0 {
		return 0
	}
	cjkTokens := 0
	nonCJKBytes := 0
	for _, r := range content {
		if isCJK(r) {
			cjkTokens++
		} else {
			nonCJKBytes += utf8.RuneLen(r)
		}
	}
	return cjkTokens + (nonCJKBytes+3)/4
}

// isCJK reports whether r belongs to a CJK script (Han ideographs plus the
// Japanese and Korean syllabaries), which tokenizers split at roughly one token
// per character rather than the chars/4 ratio that holds for Latin text.
func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}
