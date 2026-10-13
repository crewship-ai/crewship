//go:build unix

package runoutput

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureCompletesWithoutAnyReader(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	result, err := Capture(t.Context(), dir, []string{"sh", "-c", "printf first; printf second >&2; exit 7"}, DefaultLimit)
	if err != nil || result.ExitCode != 7 || result.Reason != "exited" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var stdout, stderr bytes.Buffer
	var code int
	_, done, err := ReadCommitted(dir, 0, func(r Record) error {
		if r.Stream == "stdout" {
			stdout.Write(r.Data)
		}
		if r.Stream == "stderr" {
			stderr.Write(r.Data)
		}
		if r.Kind == "exit" {
			code = r.ExitCode
		}
		return nil
	})
	if err != nil || !done || code != 7 || stdout.String() != "first" || stderr.String() != "second" {
		t.Fatalf("out=%q errout=%q code=%d done=%v err=%v", stdout.String(), stderr.String(), code, done, err)
	}
	marker := filepath.Join(filepath.Dir(dir), "duplicate")
	if _, err = Capture(t.Context(), dir, []string{"touch", marker}, DefaultLimit); !errors.Is(err, ErrClaimed) {
		t.Fatalf("relaunch accepted: %v", err)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("duplicate command executed")
	}
}
func TestCaptureStopsOnOutputLimit(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "run")
	result, err := Capture(ctx, dir, []string{"sh", "-c", "while :; do printf 0123456789012345678901234567890123456789; done"}, terminalReserve+chunkBytes*2)
	if err != nil || result.Reason != "output_limit" || ctx.Err() != nil {
		t.Fatalf("result=%+v err=%v ctx=%v", result, err, ctx.Err())
	}
}

func TestCaptureOutlivesCancelledReader(t *testing.T) {
	root := t.TempDir()
	dir, release := filepath.Join(root, "run"), filepath.Join(root, "release")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	type outcome struct {
		result Result
		err    error
	}
	completed := make(chan outcome, 1)
	go func() {
		result, err := Capture(ctx, dir, []string{"sh", "-c", `printf before; while [ ! -f "$1" ]; do sleep 0.02; done; printf after`, "fixture", release}, DefaultLimit)
		completed <- outcome{result, err}
	}()
	readerCtx, disconnect := context.WithCancel(ctx)
	defer disconnect()
	var cursor uint64
	err := Follow(readerCtx, dir, 0, func(record Record) error {
		cursor = record.Sequence
		if string(record.Data) == "before" {
			disconnect()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reader disconnect=%v", err)
	}
	if err = os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-completed:
		if got.err != nil || got.result.ExitCode != 0 || got.result.Reason != "exited" {
			t.Fatalf("capture=%+v err=%v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("reader loss stranded producer")
	}
	var replay bytes.Buffer
	_, done, err := ReadCommitted(dir, cursor, func(record Record) error { replay.Write(record.Data); return nil })
	if err != nil || !done || replay.String() != "after" {
		t.Fatalf("replay=%q done=%v err=%v", replay.String(), done, err)
	}
}
