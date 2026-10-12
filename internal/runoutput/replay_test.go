//go:build unix

package runoutput

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func replayFixture(t *testing.T, finished bool) (string, *Store) {
	t.Helper()
	input := hostFixtureInput(t)
	createHostFixtureDir(t, input)
	store, err := Create(filepath.Join(input.Directory, "output"), DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err = store.Write("stdout", []byte("first\npartial")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Write("stderr", []byte("diagnostic\n")); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Write("stdout", []byte(" tail\nlast without newline")); err != nil {
		t.Fatal(err)
	}
	if finished {
		if err = store.Finish(7, "exited"); err != nil {
			t.Fatal(err)
		}
	}
	return input.Directory, store
}

func TestReplayRetainsStableKeysAcrossLostProjectionAcknowledgements(t *testing.T) {
	dir, _ := replayFixture(t, true)
	type key struct {
		sequence uint64
		index    uint32
	}
	// This map models a downstream transactional unique receipt: a replay
	// after commit/ack loss must deliver the same identity and immutable bytes.
	receipts := map[key]Line{}
	calls := map[key]int{}
	crash := errors.New("controller killed after downstream commit")
	injected := false
	project := func(line Line) error {
		k := key{line.Sequence, line.Index}
		calls[k]++
		if prior, exists := receipts[k]; exists && !reflect.DeepEqual(prior, line) {
			t.Fatalf("replay key changed content: %+v", k)
		}
		receipts[k] = line
		if !injected && string(line.Data) == "partial tail" {
			injected = true
			return crash
		}
		return nil
	}
	terminalCalls := 0
	terminalCommitted := false
	complete := func(snapshot Snapshot) error {
		terminalCalls++
		if snapshot.Result.ExitCode != 7 {
			t.Fatalf("terminal=%+v", snapshot)
		}
		if !terminalCommitted {
			terminalCommitted = true
			return crash
		}
		return nil
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, project, complete); !errors.Is(err, crash) {
		t.Fatalf("first crash=%v", err)
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, project, complete); !errors.Is(err, crash) {
		t.Fatalf("terminal ack crash=%v", err)
	}
	snapshot, err := ReplayHost(t.Context(), dir, 1024, project, complete)
	if err != nil || !snapshot.Complete {
		t.Fatalf("recovery=%+v %v", snapshot, err)
	}
	if len(receipts) != 4 || calls[key{4, 0}] != 2 || terminalCalls != 2 {
		t.Fatalf("receipts=%v calls=%v terminal=%d", receipts, calls, terminalCalls)
	}
	priorCalls := len(calls)
	if _, err = ReplayHost(t.Context(), dir, 1024, func(Line) error { t.Fatal("acknowledged output projected again"); return nil }, func(Snapshot) error { t.Fatal("acknowledged terminal projected again"); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(calls) != priorCalls {
		t.Fatal("projection repeated")
	}
}

func TestReplayDoesNotAcknowledgeIncompleteLineOrInventTerminal(t *testing.T) {
	dir, store := replayFixture(t, false)
	var lines []string
	complete := func(Snapshot) error { t.Fatal("incomplete source completed"); return nil }
	snapshot, err := ReplayHost(t.Context(), dir, 1024, func(line Line) error { lines = append(lines, string(line.Data)); return nil }, complete)
	if err != nil || snapshot.Complete || len(lines) != 3 {
		t.Fatalf("incomplete replay=%+v lines=%v err=%v", snapshot, lines, err)
	}
	if err = store.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = ReplayHost(t.Context(), dir, 1024, func(line Line) error { lines = append(lines, string(line.Data)); return nil }, func(Snapshot) error { return nil })
	if err != nil || !snapshot.Complete || len(lines) != 4 || lines[3] != "last without newline" {
		t.Fatalf("terminal replay=%+v lines=%v err=%v", snapshot, lines, err)
	}
}

func TestReplayRefusesRewrittenAcknowledgedPrefix(t *testing.T) {
	dir, _ := replayFixture(t, true)
	crash := errors.New("lost controller")
	if _, err := ReplayHost(t.Context(), dir, 1024, func(line Line) error {
		if line.Stream == "stderr" {
			return crash
		}
		return nil
	}, func(Snapshot) error { return nil }); !errors.Is(err, crash) {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "output", "events.jsonl")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	old := base64.StdEncoding.EncodeToString([]byte("first\npartial"))
	changed := base64.StdEncoding.EncodeToString([]byte("other\npartial"))
	data = bytes.Replace(data, []byte(old), []byte(changed), 1)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReplayHost(t.Context(), dir, 1024, func(Line) error { t.Fatal("corrupt acknowledged prefix caused new effect"); return nil }, func(Snapshot) error { t.Fatal("corrupt source completed"); return nil }); err == nil {
		t.Fatal("rewritten prefix accepted")
	}
}

func TestHostRetentionRequiresTerminalAcknowledgementAndKeepsClaim(t *testing.T) {
	dir, store := replayFixture(t, false)
	cutoff := time.Now().Add(time.Hour)
	if collected, err := CollectHost(t.Context(), dir, cutoff); err != nil || collected {
		t.Fatalf("incomplete collected=%v %v", collected, err)
	}
	if err := store.Finish(0, "exited"); err != nil {
		t.Fatal(err)
	}
	if collected, err := CollectHost(t.Context(), dir, cutoff); err != nil || collected {
		t.Fatalf("unacknowledged collected=%v %v", collected, err)
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, func(Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if collected, err := CollectHost(t.Context(), dir, time.Now().Add(-time.Hour)); err != nil || collected {
		t.Fatalf("fresh acknowledgement collected=%v %v", collected, err)
	}
	if collected, err := CollectHost(t.Context(), dir, cutoff); err != nil || !collected {
		t.Fatalf("terminal collection=%v %v", collected, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "output")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source not collected: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "replay-ack.json")); err != nil {
		t.Fatal("terminal receipt lost", err)
	}
	if err := os.Mkdir(dir, 0700); !errors.Is(err, os.ErrExist) {
		t.Fatalf("claim tombstone lost: %v", err)
	}
	if collected, err := CollectHost(t.Context(), dir, cutoff); err != nil || collected {
		t.Fatalf("repeated collection=%v %v", collected, err)
	}
}

func TestHostRetentionResumesInterruptedUnlink(t *testing.T) {
	dir, _ := replayFixture(t, true)
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, func(Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ack, err := readReplayAcknowledgement(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCollectionReceipt(dir, ack); err != nil {
		t.Fatal(err)
	}
	collecting := filepath.Join(dir, ".collecting-output")
	if err := os.Rename(filepath.Join(dir, "output"), collecting); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(collecting, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	if collected, err := CollectHost(t.Context(), dir, time.Now().Add(time.Hour)); err != nil || !collected {
		t.Fatalf("interrupted collection=%v %v", collected, err)
	}
	if _, err := os.Stat(collecting); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("collection staging remains: %v", err)
	}
}

func TestHostReplayExcludesConcurrentProjector(t *testing.T) {
	dir, _ := replayFixture(t, true)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	first := true
	go func() {
		_, err := ReplayHost(t.Context(), dir, 1024, func(Line) error {
			if first {
				first = false
				close(entered)
				<-release
			}
			return nil
		}, func(Snapshot) error { return nil })
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := ReplayHost(ctx, dir, 1024, func(Line) error { t.Error("concurrent projector entered"); return nil }, func(Snapshot) error { return nil })
	close(release)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent replay=%v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReplayCleansOnlyPrivateKnownCrashTemps(t *testing.T) {
	dir, _ := replayFixture(t, true)
	for _, name := range []string{".replay-ack-stale", ".collection-stale", ".reservation-stale", "operator-owned"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, func(Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".replay-ack-stale", ".collection-stale", ".reservation-stale"} {
		if _, err := os.Lstat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temporary %s remains: %v", name, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(dir, "operator-owned")); err != nil || string(data) != "stale" {
		t.Fatal("unknown file touched", err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(foreign, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, filepath.Join(dir, ".replay-ack-symlink")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplayHost(t.Context(), dir, 1024, func(Line) error { return nil }, func(Snapshot) error { return nil }); err == nil {
		t.Fatal("temporary symlink accepted")
	}
	if data, err := os.ReadFile(foreign); err != nil || string(data) != "preserve" {
		t.Fatal("symlink target touched", err)
	}
}
