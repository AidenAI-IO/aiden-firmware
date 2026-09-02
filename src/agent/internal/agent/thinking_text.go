package agent

import (
	"bytes"
	"strings"
)

var (
	thinkingOpenTags  = []string{"<think>", "<thinking>"}
	thinkingCloseTags = []string{"</think>", "</thinking>"}
)

// normalizeTaggedThinkingText separates provider text that is wrapped in
// think tags. Some OpenAI-compatible gateways omit the opening tag and send
// only the closing tag, so everything before an orphan close is treated as
// reasoning as well.
func normalizeTaggedThinkingText(source string) (visible, reasoning string, found bool) {
	var visibleParts, reasoningParts []string
	remaining := source
	for len(remaining) > 0 {
		openAt, openTag := indexFoldAny(remaining, thinkingOpenTags)
		closeAt, closeTag := indexFoldAny(remaining, thinkingCloseTags)
		switch {
		case openAt < 0 && closeAt < 0:
			visibleParts = append(visibleParts, remaining)
			remaining = ""
		case closeAt >= 0 && (openAt < 0 || closeAt < openAt):
			reasoningParts = append(reasoningParts, remaining[:closeAt])
			remaining = remaining[closeAt+len(closeTag):]
			found = true
		case openAt >= 0:
			visibleParts = append(visibleParts, remaining[:openAt])
			remaining = remaining[openAt+len(openTag):]
			found = true
			closeAt, closeTag = indexFoldAny(remaining, thinkingCloseTags)
			if closeAt < 0 {
				reasoningParts = append(reasoningParts, remaining)
				remaining = ""
				continue
			}
			reasoningParts = append(reasoningParts, remaining[:closeAt])
			remaining = remaining[closeAt+len(closeTag):]
		}
	}
	return strings.TrimSpace(strings.Join(visibleParts, "")), strings.TrimSpace(strings.Join(reasoningParts, "\n")), found
}

func indexFoldAny(source string, tags []string) (index int, tag string) {
	index = -1
	for _, candidate := range tags {
		if at := indexFold(source, candidate); at >= 0 && (index < 0 || at < index) {
			index = at
			tag = candidate
		}
	}
	return index, tag
}

func indexFold(source, needle string) int {
	return strings.Index(strings.ToLower(source), strings.ToLower(needle))
}

// taggedThinkingStream filters a content stream while preserving incremental
// delivery after a thinking block closes. Text before an orphan closing tag is
// buffered because it cannot be known to be visible until the response ends.
type taggedThinkingStream struct {
	downstream      func([]byte) error
	reasoningStream func([]byte) error
	pending         bytes.Buffer
	inThinking      bool
	foundTag        bool
}

func newTaggedThinkingStream(downstream func([]byte) error, reasoning ...func([]byte) error) *taggedThinkingStream {
	var reasoningStream func([]byte) error
	if len(reasoning) > 0 {
		reasoningStream = reasoning[0]
	}
	return &taggedThinkingStream{downstream: downstream, reasoningStream: reasoningStream}
}

func (s *taggedThinkingStream) Write(chunk []byte) error {
	if s == nil || len(chunk) == 0 {
		return nil
	}
	if s.downstream == nil && s.reasoningStream == nil {
		return nil
	}
	s.pending.Write(chunk)
	return s.process(false)
}

func (s *taggedThinkingStream) Finish() error {
	if s == nil || (s.downstream == nil && s.reasoningStream == nil) {
		return nil
	}
	if s.inThinking {
		if err := s.emitReasoning(s.pending.Bytes()); err != nil {
			return err
		}
		s.pending.Reset()
		return nil
	}
	// If no tag appeared, the whole response was ordinary visible content.
	if !s.foundTag && s.pending.Len() > 0 {
		return s.emit(s.pending.Bytes())
	}
	return nil
}

// FlushVisible switches a stream back to ordinary content when the provider
// has already supplied reasoning through its dedicated field. This avoids
// delaying correctly separated visible deltas just because reasoning was
// enabled for the request.
func (s *taggedThinkingStream) FlushVisible() error {
	if s == nil || s.downstream == nil || s.pending.Len() == 0 {
		return nil
	}
	err := s.emit(s.pending.Bytes())
	s.pending.Reset()
	s.inThinking = false
	s.foundTag = true
	return err
}

func (s *taggedThinkingStream) process(final bool) error {
	for s.pending.Len() > 0 {
		text := s.pending.String()
		if s.inThinking {
			at, tag := indexFoldAny(text, thinkingCloseTags)
			if at < 0 {
				// An explicit opening tag makes the pending text unambiguously
				// reasoning. Keep only a possible partial closing tag buffered.
				safe := len(text) - maxThinkingTagPrefixLen(text, thinkingCloseTags)
				if safe > 0 {
					if err := s.emitReasoning([]byte(text[:safe])); err != nil {
						return err
					}
					s.pending.Next(safe)
				}
				return nil
			}
			if err := s.emitReasoning([]byte(text[:at])); err != nil {
				return err
			}
			s.pending.Next(at + len(tag))
			s.pending = *bytes.NewBuffer(bytes.TrimLeft(s.pending.Bytes(), " \t\r\n"))
			s.inThinking = false
			s.foundTag = true
			continue
		}

		openAt, openTag := indexFoldAny(text, thinkingOpenTags)
		closeAt, closeTag := indexFoldAny(text, thinkingCloseTags)
		switch {
		case openAt >= 0 && (closeAt < 0 || openAt < closeAt):
			if openAt > 0 {
				if err := s.emit(s.pending.Bytes()[:openAt]); err != nil {
					return err
				}
			}
			s.pending.Next(openAt + len(openTag))
			s.inThinking = true
			s.foundTag = true
		case closeAt >= 0:
			// No opening tag: this is the malformed gateway format observed in
			// production. Everything before the close is reasoning.
			if err := s.emitReasoning([]byte(text[:closeAt])); err != nil {
				return err
			}
			s.pending.Next(closeAt + len(closeTag))
			s.pending = *bytes.NewBuffer(bytes.TrimLeft(s.pending.Bytes(), " \t\r\n"))
			s.inThinking = false
			s.foundTag = true
		case s.foundTag:
			if s.pending.Len() > 0 {
				if err := s.emit(s.pending.Bytes()); err != nil {
					return err
				}
				s.pending.Reset()
			}
		case final:
			if s.pending.Len() > 0 {
				if err := s.emit(s.pending.Bytes()); err != nil {
					return err
				}
				s.pending.Reset()
			}
		default:
			return nil
		}
		if !s.inThinking && s.foundTag && s.pending.Len() > 0 {
			if err := s.emit(s.pending.Bytes()); err != nil {
				return err
			}
			s.pending.Reset()
		}
	}
	return nil
}

func (s *taggedThinkingStream) emit(chunk []byte) error {
	if len(chunk) == 0 || s.downstream == nil {
		return nil
	}
	return s.downstream(chunk)
}

func (s *taggedThinkingStream) emitReasoning(chunk []byte) error {
	if len(chunk) == 0 || s.reasoningStream == nil {
		return nil
	}
	return s.reasoningStream(chunk)
}

func maxThinkingTagPrefixLen(source string, tags []string) int {
	max := 0
	for _, tag := range tags {
		limit := len(tag) - 1
		if limit <= 0 || limit > len(source) {
			continue
		}
		for n := 1; n <= limit; n++ {
			if strings.EqualFold(source[len(source)-n:], tag[:n]) && n > max {
				max = n
			}
		}
	}
	return max
}
