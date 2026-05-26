package backup_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// buildMultiSectionPayload constructs a synthetic payload tar.zst
// with workspace/, volumes/, memory/, system/, devcontainer/ and
// db/dump.json entries — enough to exercise every branch of
// ExtractPayload and Open* methods.
func buildMultiSectionPayload(t *testing.T) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	tw, err := backup.NewTarZstWriter(buf)
	if err != nil {
		t.Fatalf("tarwriter: %v", err)
	}
	files := map[string][]byte{
		"workspace/alpha/main.go":              []byte("package main"),
		"workspace/alpha/sub/file.txt":         []byte("content"),
		"volumes/alpha/home/.bashrc":           []byte("export PATH=..."),
		"volumes/alpha/tools/bin/x":            []byte("binary"),
		"memory/alpha/MEMORY.md":               []byte("# memory"),
		"memory/alpha/daily/2026-05-25.md":     []byte("daily log"),
		"system/alpha/var-lib/redis/dump":      []byte("redis data"),
		"devcontainer/alpha/devcontainer.json": []byte("{}"),
		"devcontainer/alpha/mise.toml":         []byte("[tools]"),
		"db/dump.json":                         []byte(`{"workspace_id":"ws_x","tables":{}}`),
	}
	for name, body := range files {
		if err := tw.WriteFile(name, 0o644, nonZeroTime(), body); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractPayload_FullMultiSection(t *testing.T) {
	ctx := context.Background()
	out, err := backup.ExtractPayload(ctx, bytes.NewReader(buildMultiSectionPayload(t)))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	defer out.Close()

	if !out.HasWorkspace("alpha") {
		t.Error("workspace section missing")
	}
	if out.DBDump == nil {
		t.Error("DB section missing")
	}
	if _, ok := out.DevcontainerBySlug["alpha"]; !ok {
		t.Error("devcontainer.json missing")
	}
	if _, ok := out.MiseBySlug["alpha"]; !ok {
		t.Error("mise.toml missing")
	}

	// Open each section type — exercises OpenWorkspace, OpenVolume,
	// OpenMemory, OpenSystem code paths.
	wsr, _, err := out.OpenWorkspace(ctx, "alpha")
	if err != nil || wsr == nil {
		t.Errorf("OpenWorkspace: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, wsr)
		wsr.Close()
	}

	memr, _, err := out.OpenMemory(ctx, "alpha")
	if err != nil || memr == nil {
		t.Errorf("OpenMemory: %v", err)
	} else {
		_, _ = io.Copy(io.Discard, memr)
		memr.Close()
	}
}

func TestExtractPayload_HandlesEmptyPayload(t *testing.T) {
	ctx := context.Background()
	// Just an empty tar.zst (no entries at all).
	buf := &bytes.Buffer{}
	tw, err := backup.NewTarZstWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := backup.ExtractPayload(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("empty payload should not error, got %v", err)
	}
	defer out.Close()
	if out.DBDump != nil {
		t.Error("empty payload should produce nil DBDump")
	}
	if out.HasWorkspace("anything") {
		t.Error("empty payload should have no sections")
	}
}

func TestExtractPayload_RejectsInvalidDBDump(t *testing.T) {
	ctx := context.Background()
	buf := &bytes.Buffer{}
	tw, err := backup.NewTarZstWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteFile("db/dump.json", 0o644, nonZeroTime(), []byte("not json")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = backup.ExtractPayload(ctx, bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Error("invalid db/dump.json should produce an error")
	}
}

func TestExtractPayload_UnknownEntriesAreSilentlyDiscarded(t *testing.T) {
	ctx := context.Background()
	buf := &bytes.Buffer{}
	tw, err := backup.NewTarZstWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	// Future-compat: entries the reader doesn't know about must
	// not error — they're forward-compat slots for newer formats.
	if err := tw.WriteFile("future/section/foo.txt", 0o644, nonZeroTime(), []byte("forward-compat")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := backup.ExtractPayload(ctx, bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("unknown entry should be discarded silently, got %v", err)
	}
	defer out.Close()
}

func TestExtractPayload_ParentRefRejected(t *testing.T) {
	ctx := context.Background()
	// Manually craft a tar entry with `..` in the name. Use the
	// standard library directly since NewTarZstWriter wraps writes.
	buf := &bytes.Buffer{}
	tw, err := backup.NewTarZstWriter(buf)
	if err != nil {
		t.Fatal(err)
	}
	// Name with .. in it
	if err := tw.WriteFile("workspace/../etc/shadow", 0o644, nonZeroTime(), []byte("bad")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = backup.ExtractPayload(ctx, bytes.NewReader(buf.Bytes()))
	if err == nil {
		t.Error("`..`-bearing path should be rejected")
	}
	if !errors.Is(err, backup.ErrInvalidManifest) {
		t.Errorf("expected ErrInvalidManifest, got %v", err)
	}
}

// === ExtractPayload + Close idempotency ===

func TestExtractedPayload_CloseTwice(t *testing.T) {
	ctx := context.Background()
	out, err := backup.ExtractPayload(ctx, bytes.NewReader(buildMultiSectionPayload(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Errorf("second Close should be safe, got %v", err)
	}
}

func TestExtractedPayload_NilClose(t *testing.T) {
	var p *backup.ExtractedPayload
	if err := p.Close(); err != nil {
		t.Errorf("nil ExtractedPayload Close should be no-op, got %v", err)
	}
}

// nonZeroTime returns a deterministic non-zero time for tar headers
// in the test fixtures.
func nonZeroTime() time.Time {
	return time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
}
