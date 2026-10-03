// Package runoutput stores append-only execution output independently of a
// controller connection. It is a transport, not an authorization boundary:
// deployment must prevent the workload from rewriting the store if provenance
// is required. A captured exit code is not proof of application success.
package runoutput

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const chunkBytes = 16 * 1024
const terminalReserve = 4096
const DefaultLimit = 64 * 1024 * 1024
const MaxLimit = 1024 * 1024 * 1024

var ErrLimit = errors.New("run output limit reached")
var ErrClaimed = errors.New("run output directory already claimed")

type Record struct {
	Sequence uint64 `json:"sequence"`
	Kind     string `json:"kind"`
	Stream   string `json:"stream,omitempty"`
	Data     []byte `json:"data,omitempty"`
	ExitCode int    `json:"exit_code"`
	Reason   string `json:"reason,omitempty"`
}
type checkpoint struct {
	Version  int    `json:"version"`
	Bytes    int64  `json:"bytes"`
	Sequence uint64 `json:"sequence"`
	Terminal bool   `json:"terminal"`
}

type Store struct {
	mu         sync.Mutex
	dir        string
	file       *os.File
	limit      int64
	checkpoint checkpoint
	failed     error
}

// Create claims an execution identity exactly once. Existing directories are
// never reused for launching a command, including an incomplete earlier claim.
func Create(dir string, limit int64) (*Store, error) {
	if limit < terminalReserve+chunkBytes*2 || limit > MaxLimit {
		return nil, fmt.Errorf("output limit outside supported range")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, ErrClaimed
		}
		return nil, err
	}
	// Persist the claim before allowing a caller to spawn work.
	if err := syncDir(filepath.Dir(dir)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, file: f, limit: limit, checkpoint: checkpoint{Version: 1}}
	if err = s.appendLocked(Record{Kind: "accepted"}, false); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func (s *Store) appendLocked(record Record, terminal bool) error {
	if s.checkpoint.Terminal {
		return errors.New("run output is already terminal")
	}
	record.Sequence = s.checkpoint.Sequence + 1
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	ceiling := s.limit - terminalReserve
	if terminal {
		ceiling = s.limit
	}
	if s.checkpoint.Bytes+int64(len(data)) > ceiling {
		return ErrLimit
	}
	if _, err = s.file.Write(data); err != nil {
		return err
	}
	if err = s.file.Sync(); err != nil {
		return err
	}
	next := checkpoint{Version: 1, Bytes: s.checkpoint.Bytes + int64(len(data)), Sequence: record.Sequence, Terminal: terminal}
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	temp := filepath.Join(s.dir, "checkpoint.tmp")
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(encoded)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temp, filepath.Join(s.dir, "checkpoint.json")); err != nil {
		return err
	}
	if err = syncDir(s.dir); err != nil {
		return err
	}
	s.checkpoint = next
	return nil
}
func (s *Store) Write(stream string, data []byte) (int, error) {
	if stream != "stdout" && stream != "stderr" {
		return 0, errors.New("invalid output stream")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return 0, s.failed
	}
	n := 0
	for len(data) > 0 {
		size := min(len(data), chunkBytes)
		if err := s.appendLocked(Record{Kind: "output", Stream: stream, Data: data[:size]}, false); err != nil {
			s.failed = err
			return n, err
		}
		data = data[size:]
		n += size
	}
	return n, nil
}
func (s *Store) Finish(code int, reason string) error {
	if code < -1 || code > 255 || reason == "" || len(reason) > 512 {
		return errors.New("invalid terminal result")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// A quota refusal has not modified the file; its reserved terminal record
	// remains writable. An I/O/commit failure has uncertain durability and must
	// not be repaired by inventing a terminal record over an ambiguous tail.
	if s.failed != nil && !errors.Is(s.failed, ErrLimit) {
		return s.failed
	}
	err := s.appendLocked(Record{Kind: "exit", ExitCode: code, Reason: reason}, true)
	if err != nil {
		s.failed = err
	}
	return err
}
func (s *Store) Close() error { return s.file.Close() }

// ReadCommitted visits only records covered by the durable checkpoint. The
// callback may receive a record again after a reader crash; consumers persist
// their own cursor and must make their downstream projection idempotent.
type readCursor struct {
	sequence uint64
	offset   int64
	terminal bool
}

func ReadCommitted(dir string, after uint64, visit func(Record) error) (uint64, bool, error) {
	cursor := readCursor{}
	last, done, err := readCommitted(dir, after, &cursor, visit)
	return last, done, err
}

func readCommitted(dir string, after uint64, cursor *readCursor, visit func(Record) error) (uint64, bool, error) {
	f, err := os.Open(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		return after, false, err
	}
	var cp checkpoint
	err = json.NewDecoder(io.LimitReader(f, 4096)).Decode(&cp)
	f.Close()
	if err != nil {
		return after, false, err
	}
	if cp.Version != 1 || cp.Sequence == 0 || cp.Bytes <= 0 || cp.Bytes > MaxLimit || cp.Sequence < after || cp.Sequence < cursor.sequence || cp.Bytes < cursor.offset {
		return after, false, errors.New("invalid run output checkpoint")
	}
	events, err := os.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return after, false, err
	}
	defer events.Close()
	info, err := events.Stat()
	if err != nil {
		return after, false, err
	}
	if info.Size() < cp.Bytes {
		return after, false, errors.New("run output shorter than committed checkpoint")
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(events, cursor.offset, cp.Bytes-cursor.offset), 64*1024)
	seq := cursor.sequence
	last := after
	terminal := cursor.terminal
	for {
		line, err := reader.ReadSlice('\n')
		if err == io.EOF && len(line) == 0 {
			break
		}
		if err != nil {
			return last, false, fmt.Errorf("read committed run output: %w", err)
		}
		var record Record
		if err = json.Unmarshal(line, &record); err != nil {
			return last, false, err
		}
		seq++
		if record.Sequence != seq || terminal || (seq == 1 && record.Kind != "accepted") {
			return last, false, errors.New("invalid run output sequence")
		}
		switch record.Kind {
		case "accepted":
			if seq != 1 {
				return last, false, errors.New("accepted record is not first")
			}
		case "output":
			if (record.Stream != "stdout" && record.Stream != "stderr") || len(record.Data) == 0 || len(record.Data) > chunkBytes {
				return last, false, errors.New("invalid run output payload")
			}
		case "exit":
			if record.ExitCode < -1 || record.ExitCode > 255 || record.Reason == "" {
				return last, false, errors.New("invalid terminal record")
			}
			terminal = true
		default:
			return last, false, errors.New("unknown run output record")
		}
		if seq > after {
			if err = visit(record); err != nil {
				return last, false, err
			}
			last = seq
		}
		cursor.sequence, cursor.terminal = seq, terminal
		cursor.offset += int64(len(line))
	}
	if seq != cp.Sequence || terminal != cp.Terminal {
		return last, false, errors.New("run output checkpoint mismatch")
	}
	return last, terminal, nil
}

// Follow cancellation affects only this reader; it never signals the producer.
func Follow(ctx context.Context, dir string, after uint64, visit func(Record) error) error {
	cursor := readCursor{}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, done, err := readCommitted(dir, after, &cursor, visit)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		after = next
		if done {
			return nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
