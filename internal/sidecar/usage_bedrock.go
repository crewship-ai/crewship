package sidecar

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
)

// Observe AWS eventstream without buffering a response. Prelude/message CRCs
// prevent damaged frames from becoming billing evidence. Oversized frames are
// discarded by length; delivery to the client is independent of observation.
const bedrockUsageFrameLimit = 64 << 10

type bedrockUsageObserver struct {
	prelude   [12]byte
	used      int
	frame     []byte
	remaining uint32
	discard   bool
	invalid   bool
	broken    bool
	usage     LLMUsage
	seenUsage bool
}

func (s *bedrockUsageObserver) Write(p []byte) (int, error) {
	n := len(p)
	if s.broken {
		return n, nil
	}
	for len(p) > 0 {
		if s.remaining == 0 {
			take := min(len(p), 12-s.used)
			copy(s.prelude[s.used:], p[:take])
			s.used += take
			p = p[take:]
			if s.used != 12 {
				continue
			}
			total := binary.BigEndian.Uint32(s.prelude[:4])
			headers := binary.BigEndian.Uint32(s.prelude[4:8])
			if total < 16 || headers > total-16 || crc32.ChecksumIEEE(s.prelude[:8]) != binary.BigEndian.Uint32(s.prelude[8:]) {
				s.invalid = true
				s.broken = true
				// A damaged length cannot safely resynchronize. Consume future writes
				// without retaining bytes; the client still receives the original stream.
				s.discard = true
				s.remaining = ^uint32(0)
				return n, nil
			}
			s.remaining = total - 12
			s.discard = total > bedrockUsageFrameLimit
			if s.discard {
				s.invalid = true
			} else {
				if cap(s.frame) < int(total) {
					s.frame = make([]byte, 0, int(total))
				}
				s.frame = append(s.frame[:0], s.prelude[:]...)
			}
		}
		take := int(min(uint64(len(p)), uint64(s.remaining)))
		if !s.discard {
			s.frame = append(s.frame, p[:take]...)
		}
		p = p[take:]
		s.remaining -= uint32(take)
		if s.remaining != 0 {
			continue
		}
		if !s.discard {
			s.observeFrame()
		}
		s.used = 0
	}
	return n, nil
}

func (s *bedrockUsageObserver) observeFrame() {
	frame := s.frame
	end := len(frame) - 4
	if crc32.ChecksumIEEE(frame[:end]) != binary.BigEndian.Uint32(frame[end:]) {
		s.invalid = true
		return
	}
	headers := int(binary.BigEndian.Uint32(frame[4:8]))
	payload := frame[12+headers : end]
	// Most frames are text/tool deltas. Avoid JSON allocations for those;
	// token metadata appears only at the end of the inference response.
	if !bytes.Contains(payload, []byte(`"usage"`)) {
		return
	}
	// ConverseStream's metadata event body carries usage. Other events have
	// no usage and do not alter the observation.
	observed := parseBedrockUsagePayload(payload)
	if observed.UsageIncomplete {
		s.invalid = true
	} else {
		s.seenUsage = true
	}
	mergeLLMUsageMax(&s.usage, observed)
}

func (s *bedrockUsageObserver) finish() (LLMUsage, bool) {
	return s.usage, s.seenUsage && !s.invalid && s.used == 0 && s.remaining == 0
}

// Missing counters are unknown, not zero. Preserve valid observed counters as
// lower bounds while refusing to call a partial/negative payload precise.
func parseBedrockUsagePayload(payload []byte) LLMUsage {
	var body struct {
		Usage *struct {
			Input  *int64 `json:"inputTokens"`
			Output *int64 `json:"outputTokens"`
			Read   *int64 `json:"cacheReadInputTokens"`
			Write  *int64 `json:"cacheWriteInputTokens"`
		} `json:"usage"`
	}
	out := LLMUsage{}
	if json.Unmarshal(payload, &body) != nil || body.Usage == nil {
		out.UsageIncomplete = true
		return out
	}
	u := body.Usage
	out.UsageIncomplete = u.Input == nil || u.Output == nil
	count := func(value *int64) int64 {
		if value == nil {
			return 0
		}
		if *value < 0 {
			out.UsageIncomplete = true
			return 0
		}
		return *value
	}
	out.InputTokens = count(u.Input)
	out.OutputTokens = count(u.Output)
	out.CachedInputTokens = count(u.Read)
	out.CacheCreationTokens = count(u.Write)
	return out
}
