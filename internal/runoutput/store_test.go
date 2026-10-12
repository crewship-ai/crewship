//go:build unix

package runoutput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInvalidCheckpointDoesNotPublishTerminal(t *testing.T) {
	dir, s := testStore(t, DefaultLimit)
	if err := s.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cp checkpoint
	if err = json.Unmarshal(data, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Sequence++ // Metadata claims a missing committed record.
	data, err = json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "checkpoint.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	published := false
	_, done, err := ReadCommitted(dir, 0, func(record Record) error {
		if record.Kind == "exit" {
			published = true
		}
		return nil
	})
	if err == nil || done || published {
		t.Fatalf("invalid terminal escaped: done=%v published=%v err=%v", done, published, err)
	}
}

func testStore(t *testing.T, limit int64) (string, *Store) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	store, err := Create(dir, limit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return dir, store
}
func TestReplayAfterReaderLoss(t *testing.T) {
	dir, s := testStore(t, DefaultLimit)
	if _, err := s.Write("stdout", []byte("first\n")); err != nil {
		t.Fatal(err)
	}
	var first bytes.Buffer
	cursor, done, err := ReadCommitted(dir, 0, func(r Record) error { first.Write(r.Data); return nil })
	if err != nil || done || first.String() != "first\n" {
		t.Fatalf("initial read: %q %v %v", first.String(), done, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err = Follow(ctx, dir, cursor, func(Record) error { t.Fatal("cancelled reader consumed output"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = s.Write("stderr", []byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(7, "exited"); err != nil {
		t.Fatal(err)
	}
	var recovered bytes.Buffer
	var exit int
	_, done, err = ReadCommitted(dir, cursor, func(r Record) error {
		recovered.Write(r.Data)
		if r.Kind == "exit" {
			exit = r.ExitCode
		}
		return nil
	})
	if err != nil || !done || recovered.String() != "second\n" || exit != 7 {
		t.Fatalf("recovered=%q exit=%d done=%v err=%v", recovered.String(), exit, done, err)
	}
	if _, err = Create(dir, DefaultLimit); !errors.Is(err, ErrClaimed) {
		t.Fatalf("duplicate claim=%v", err)
	}
}
func TestUncommittedTailNeverReplayed(t *testing.T) {
	dir, s := testStore(t, DefaultLimit)
	if _, err := s.Write("stdout", []byte("committed")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("{truncated crash tail"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	var out bytes.Buffer
	_, done, err := ReadCommitted(dir, 0, func(r Record) error { out.Write(r.Data); return nil })
	if err != nil || done || out.String() != "committed" {
		t.Fatalf("out=%q done=%v err=%v", out.String(), done, err)
	}
}
func TestQuotaKeepsRoomForExplicitTerminal(t *testing.T) {
	dir, s := testStore(t, terminalReserve+chunkBytes*2)
	if _, err := s.Write("stdout", bytes.Repeat([]byte("x"), chunkBytes*4)); !errors.Is(err, ErrLimit) {
		t.Fatalf("quota=%v", err)
	}
	if err := s.Finish(-1, "output_limit"); err != nil {
		t.Fatal(err)
	}
	var reason string
	_, done, err := ReadCommitted(dir, 0, func(r Record) error {
		if r.Kind == "exit" {
			reason = r.Reason
		}
		return nil
	})
	if err != nil || !done || reason != "output_limit" {
		t.Fatalf("reason=%s done=%v err=%v", reason, done, err)
	}
}

func TestFailedCheckpointDoesNotPublishUncommittedOutput(t *testing.T) {
	dir, s := testStore(t, DefaultLimit)
	if err := os.Mkdir(filepath.Join(dir, "checkpoint.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write("stdout", []byte("uncommitted")); err == nil {
		t.Fatal("checkpoint failure hidden")
	}
	if err := s.Finish(0, "exited"); err == nil {
		t.Fatal("invented completion after failed commit")
	}
	var output bytes.Buffer
	_, done, err := ReadCommitted(dir, 0, func(record Record) error { output.Write(record.Data); return nil })
	if err != nil || done || output.Len() != 0 {
		t.Fatalf("output=%q done=%v err=%v", output.String(), done, err)
	}
}

func TestInvalidInputDoesNotCorruptCommittedStore(t *testing.T) {
	dir, s := testStore(t, DefaultLimit)
	if _, err := s.Write("unknown", []byte("bad")); err == nil {
		t.Fatal("invalid stream accepted")
	}
	if err := s.Finish(900, "exited"); err == nil {
		t.Fatal("invalid exit accepted")
	}
	if _, _, err := ReadCommitted(dir, 99, func(Record) error { t.Fatal("future cursor delivered data"); return nil }); err == nil {
		t.Fatal("future cursor accepted")
	}
	if _, err := s.Write("stdout", []byte("valid")); err != nil {
		t.Fatal(err)
	}
	if err := s.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	_, done, err := ReadCommitted(dir, 0, func(Record) error { return nil })
	if err != nil || !done {
		t.Fatalf("valid retry lost: %v %v", done, err)
	}
}
