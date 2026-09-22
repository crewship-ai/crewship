package sidecar

import "bytes"

// sseUsageObserver retains one event, never the entire response. This keeps
// final usage observable even when a stream exceeds the response-buffer cap.
// Oversized events are discarded through their delimiter without interrupting
// delivery; callers must report usage as unknown if any event was skipped.
type sseUsageObserver struct {
	codec   string
	limit   int
	buf     []byte
	lines   []string
	usage   LLMUsage
	lineLen int // saturated at 2: only empty and CR-only lines delimit events
	lineCR  bool
	discard bool
	skipped int
}

func (s *sseUsageObserver) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		end := len(p)
		if i >= 0 {
			end = i + 1
		}
		part := p[:end]
		s.append(part)
		content := part
		if i >= 0 {
			content = part[:len(part)-1]
		}
		if s.lineLen == 0 && len(content) > 0 {
			s.lineCR = content[0] == '\r'
		}
		s.lineLen = min(2, s.lineLen+len(content))
		if i >= 0 {
			if s.lineLen == 0 || (s.lineLen == 1 && s.lineCR) {
				s.flush()
			}
			s.lineLen, s.lineCR = 0, false
		}
		p = p[end:]
	}
	return n, nil
}

func (s *sseUsageObserver) append(p []byte) {
	if s.discard {
		return
	}
	if len(p) > s.limit-len(s.buf) {
		s.discard = true
		s.buf = s.buf[:0]
		return
	}
	need := len(s.buf) + len(p)
	if need > cap(s.buf) {
		// append's implicit capacity growth may exceed the declared limit.
		capacity := min(s.limit, max(need, max(1024, cap(s.buf)*2)))
		buf := make([]byte, len(s.buf), capacity)
		copy(buf, s.buf)
		s.buf = buf
	}
	s.buf = append(s.buf, p...)
}

func (s *sseUsageObserver) flush() {
	if s.discard {
		s.skipped++
	} else if len(s.buf) > 0 {
		mergeLLMUsageMax(&s.usage, parseLLMUsageSSEWithScratch(s.codec, string(s.buf), &s.lines))
	}
	s.buf = s.buf[:0]
	s.discard = false
}

func (s *sseUsageObserver) finish() LLMUsage {
	// Preserve the previous parser's acceptance of an unterminated final event.
	s.flush()
	if s.skipped > 0 {
		// Partial observations must not masquerade as complete token totals.
		return LLMUsage{Model: s.usage.Model}
	}
	return s.usage
}
