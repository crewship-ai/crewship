package conversation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrSharedLimit means the source transcript exceeds a configured byte or
// message bound. Callers must not silently return a partial transcript.
var ErrSharedLimit = errors.New("shared transcript limit exceeded")

type sharedCountingReader struct {
	ctx context.Context
	r   io.Reader
	n   int64
}

func (r *sharedCountingReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	r.n += int64(n)
	return n, err
}

// ReadShared reads one bounded JSONL snapshot for the public text projection.
// The cap applies to source bytes, including parts and metadata that the API
// later discards. It rejects an oversized transcript before holding all of
// its messages in memory and never returns a partial result. A concurrent
// append can enter this read, but the byte cap still bounds its work.
func (s *Store) ReadShared(ctx context.Context, sessionID string, maxBytes int64, maxMessages int) ([]Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateID(sessionID); err != nil {
		return nil, fmt.Errorf("invalid session ID: %w", err)
	}
	if maxBytes < 1 || maxBytes > 16<<20 || maxMessages < 1 || maxMessages > 1000 {
		return nil, fmt.Errorf("invalid shared read limits")
	}
	f, err := os.Open(filepath.Join(s.basePath, "conversations", sessionID+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return []Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open shared transcript: %w", err)
	}
	defer f.Close()

	// The extra byte distinguishes an exact-limit file from an oversized one.
	// scanner's maximum token is also bounded; no single JSONL row can force
	// an unbounded allocation before a newline appears.
	reader := &sharedCountingReader{ctx: ctx, r: io.LimitReader(f, maxBytes+1)}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxBytes)+2)
	messages := make([]Message, 0)
	rows := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if reader.n > maxBytes {
			return nil, ErrSharedLimit
		}
		rows++
		if rows > maxMessages {
			return nil, ErrSharedLimit
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, fmt.Errorf("shared transcript contains an empty row")
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, fmt.Errorf("decode shared transcript row %d: %w", rows, err)
		}
		messages = append(messages, msg)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if reader.n > maxBytes || errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return nil, ErrSharedLimit
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read shared transcript: %w", err)
	}
	return messages, nil
}
