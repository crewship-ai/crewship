package orchestrator

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// RetainedEvent carries a stable projection identity, not permission to run
// its side effects. A sink must durably deduplicate before acknowledging it.
type RetainedEvent struct {
	ID       string
	Sequence uint64
	Event    AgentEvent
}

// RetainedEventDecoder rebuilds adapter and scrubber state from sequence 1.
// Use the same decoder version for the whole run. It consumes only records
// validated against the transport's committed checkpoint; callers must never
// forward a speculative terminal record. Reader EOF does not flush this state.
type RetainedEventDecoder struct {
	lines    *runoutput.LineDecoder
	flush    func()
	sequence uint64
	ordinal  uint32
	failed   error
	complete bool
}

func (o *Orchestrator) NewRetainedEventDecoder(run RunState, secrets []string, emit func(RetainedEvent) error) (*RetainedEventDecoder, error) {
	if !ValidRunID(run.ID) || run.StartedAt.IsZero() || run.Output == nil || run.Output.Version != 1 || emit == nil {
		return nil, errors.New("retained event decoder requires durable identity")
	}
	adapter := getAdapter(run.Output.Adapter)
	if adapter.Name() == "" {
		return nil, errors.New("retained event adapter unavailable")
	}
	d := &RetainedEventDecoder{}
	metadataScrubber := scrubber.New()
	metadataScrubber.AddSecretValues(secrets...)
	deliver := func(event AgentEvent) {
		if d.failed != nil {
			return
		}
		// Parser wall-clock timestamps are not durable source timestamps. The
		// admitted run timestamp is a stable anchor until transport timestamps
		// become part of a versioned protocol.
		event.Timestamp = run.StartedAt.UTC().Truncate(time.Millisecond)
		if event.Metadata != nil {
			var err error
			event.Metadata, err = scrubRetainedMetadata(metadataScrubber, event.Metadata)
			if err != nil {
				d.failed = err
				return
			}
		}
		identity := fmt.Sprintf("%s/replay-v1/%d/%d", run.ID, d.sequence, d.ordinal)
		d.ordinal++
		d.failed = emit(RetainedEvent{ID: identity, Sequence: d.sequence, Event: event})
	}
	handler, flush := o.wrapScrubHandler(deliver, secrets)
	d.flush = flush
	parse := adapter.ParseStreamLine
	if factory, ok := adapter.(streamLineParserFactory); ok {
		parse = factory.NewStreamLineParser()
	}
	lines, err := runoutput.NewLineDecoder(16*1024*1024, func(line runoutput.Line) error {
		if d.failed != nil {
			return d.failed
		}
		if line.Stream == "stdout" && adapter.UseStreamJSON() {
			parse(line.Data, handler)
		} else {
			handler(AgentEvent{Type: "text", Content: string(line.Data) + "\n"})
		}
		return d.failed
	})
	if err != nil {
		return nil, err
	}
	d.lines = lines
	return d, nil
}

func (d *RetainedEventDecoder) Push(record runoutput.Record) error {
	if d.failed != nil {
		return d.failed
	}
	d.sequence, d.ordinal = record.Sequence, 0
	if err := d.lines.Push(record); err != nil {
		d.failed = err
		return err
	}
	if d.lines.Complete() {
		d.flush()
		if d.failed == nil {
			d.complete = true
		}
	}
	return d.failed
}

func (d *RetainedEventDecoder) Complete() bool { return d.complete }

// Metadata may contain tool arguments and result bodies. Scrub string values
// and keys after JSON normalization; editing serialized JSON could invalidate
// escapes or collapse different keys silently. Retain exact JSON numbers.
func scrubRetainedMetadata(s *scrubber.Scrubber, value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 1024*1024 {
		return nil, errors.New("retained event metadata unavailable or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var normalized any
	if err = decoder.Decode(&normalized); err != nil {
		return nil, errors.New("retained event metadata invalid")
	}
	var walk func(any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			return s.Scrub(x), nil
		case []any:
			for i, item := range x {
				clean, err := walk(item)
				if err != nil {
					return nil, err
				}
				x[i] = clean
			}
			return x, nil
		case map[string]any:
			out := make(map[string]any, len(x))
			for key, item := range x {
				key = s.Scrub(key)
				if _, exists := out[key]; exists {
					return nil, errors.New("retained metadata keys collide after scrubbing")
				}
				clean, err := walk(item)
				if err != nil {
					return nil, err
				}
				out[key] = clean
			}
			return out, nil
		default:
			return v, nil
		}
	}
	return walk(normalized)
}
