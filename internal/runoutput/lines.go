package runoutput

import (
	"bytes"
	"errors"
)

// Line is untrusted, unsanitized workload output. It must pass through the
// adapter and run-scoped scrubber before entering a journal or UI. Sequence
// and Index identify the line deterministically within an immutable record log.
type Line struct {
	Sequence uint64
	Index    uint32
	Stream   string
	Data     []byte
}

// LineDecoder reconstructs separate stdout/stderr lines from a log starting
// at sequence 1. After reconnect, replay the prefix to reconstruct pending
// bytes and parser state; do not resume at an arbitrary record boundary.
// It never flushes on reader EOF: only a validated exit record ends a stream.
// A callback failure poisons the decoder; start a fresh replay on retry.
type LineDecoder struct {
	max      int
	emit     func(Line) error
	pending  map[string][]byte
	sequence uint64
	complete bool
	failure  error
}

func NewLineDecoder(maxLineBytes int, emit func(Line) error) (*LineDecoder, error) {
	if maxLineBytes < 1 || maxLineBytes > 16*1024*1024 || emit == nil {
		return nil, errors.New("invalid retained line decoder configuration")
	}
	return &LineDecoder{max: maxLineBytes, emit: emit, pending: map[string][]byte{"stdout": nil, "stderr": nil}}, nil
}

func (d *LineDecoder) Complete() bool { return d.complete }

func (d *LineDecoder) Push(record Record) (err error) {
	if d.failure != nil {
		return d.failure
	}
	defer func() {
		if err != nil {
			d.failure = err
		}
	}()
	if d.complete || record.Sequence != d.sequence+1 {
		return errors.New("retained record out of sequence")
	}
	if record.Sequence == 1 && record.Kind != "accepted" {
		return errors.New("retained log missing acceptance")
	}
	var index uint32
	emit := func(stream string, data []byte) error {
		line := Line{Sequence: record.Sequence, Index: index, Stream: stream, Data: bytes.Clone(data)}
		index++
		return d.emit(line)
	}
	switch record.Kind {
	case "accepted":
		if record.Sequence != 1 || len(record.Data) != 0 || record.Stream != "" || record.Reason != "" || record.ExitCode != 0 {
			return errors.New("invalid retained acceptance")
		}
	case "output":
		if record.Sequence == 1 || (record.Stream != "stdout" && record.Stream != "stderr") || len(record.Data) == 0 || len(record.Data) > chunkBytes || record.ExitCode != 0 || record.Reason != "" {
			return errors.New("invalid retained output")
		}
		// Scan each bounded input fragment before copying; a long line cannot
		// allocate an unbounded buffer even when its newline is far away.
		remaining := record.Data
		for len(remaining) > 0 {
			n := bytes.IndexByte(remaining, '\n')
			end := len(remaining)
			if n >= 0 {
				end = n
			}
			if len(d.pending[record.Stream])+end > d.max {
				return errors.New("retained output line exceeds limit")
			}
			d.pending[record.Stream] = append(d.pending[record.Stream], remaining[:end]...)
			if n < 0 {
				break
			}
			if err = emit(record.Stream, d.pending[record.Stream]); err != nil {
				return err
			}
			d.pending[record.Stream] = nil
			remaining = remaining[n+1:]
		}
	case "exit":
		snapshot := Snapshot{Version: 1, Sequence: record.Sequence, Complete: true, Result: &Result{ExitCode: record.ExitCode, Reason: record.Reason}}
		if len(record.Data) != 0 || record.Stream != "" {
			return errors.New("invalid retained exit payload")
		}
		if err = snapshot.Validate(); err != nil {
			return err
		}
		for _, stream := range []string{"stdout", "stderr"} {
			if len(d.pending[stream]) > 0 {
				if err = emit(stream, d.pending[stream]); err != nil {
					return err
				}
				d.pending[stream] = nil
			}
		}
		d.complete = true
	default:
		return errors.New("unknown retained record kind")
	}
	d.sequence = record.Sequence
	return nil
}
