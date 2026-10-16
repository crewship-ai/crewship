package slidesexport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixtureJournal(t *testing.T) *cleanupJournal {
	t.Helper()
	j, err := openCleanupJournal(t.TempDir(), "unit")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.close() })
	return j
}
func TestCleanupJournalPreservesMalformedAndForeignIntents(t *testing.T) {
	j := fixtureJournal(t)
	v := newIntent("unit")
	if err := j.write(v, true); err != nil {
		t.Fatal(err)
	}
	got, err := j.read(v.Name)
	if err != nil || got != v {
		t.Fatal(got, err)
	}
	got.Token = "foreign_token_1234567890"
	if !errors.Is(j.remove(got), ErrCleanup) {
		t.Fatal("foreign token removed intent")
	}
	raw, _ := json.Marshal(v)
	raw = append(raw[:len(raw)-1], []byte(`,"phase":"created"}`)...)
	if err := os.WriteFile(filepath.Join(j.directory, v.Name+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := j.list(); !errors.Is(err, ErrCleanup) {
		t.Fatal("duplicate field accepted", err)
	}
	if _, err := os.Stat(filepath.Join(j.directory, v.Name+".json")); err != nil {
		t.Fatal("malformed recovery data removed", err)
	}
}
func TestCleanupJournalRefusesReplacedDirectory(t *testing.T) {
	j := fixtureJournal(t)
	moved := j.directory + "-original"
	if err := os.Rename(j.directory, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Mkdir(j.directory, 0700); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(j.write(newIntent("unit"), true), ErrCleanup) {
		t.Fatal("replaced journal accepted")
	}
}
func TestCleanupJournalCrossProcessExclusiveLease(t *testing.T) {
	if path := os.Getenv("CREWSHIP_TEST_EXPORT_JOURNAL_LOCK"); path != "" {
		j, err := openCleanupJournal(path, "unit")
		if err == nil {
			_ = j.close()
			t.Fatal("child acquired live parent lease")
		}
		return
	}
	j := fixtureJournal(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCleanupJournalCrossProcessExclusiveLease$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CREWSHIP_TEST_EXPORT_JOURNAL_LOCK="+j.directory)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lease: %s %v", output, err)
	}
}

// This executable is a test-owned process fixture, never a real Docker daemon.
func fakeAbsentDocker(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf 'No such object\\n' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
func TestUncertainRecoveryRequiresAuditedTrustedAcknowledgement(t *testing.T) {
	fakeAbsentDocker(t)
	r := &Runner{OwnerID: "unit", JournalDir: t.TempDir()}
	defer r.Close()
	// Initialize under the same lifetime OS lease as production maintenance.
	l, err := r.acquire(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	v := newIntent("unit")
	v.Phase = "uncertain"
	if err := l.journal.write(v, true); err != nil {
		t.Fatal(err)
	}
	l.Close()
	t.Cleanup(func() { uncertainJobs.Delete(v.Name) })
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := r.Reconcile(ctx); !errors.Is(err, ErrCleanup) {
		t.Fatal("uncertain absence was accepted", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("reconcile ignored caller deadline")
	}
	if _, err := r.journal.read(v.Name); err != nil {
		t.Fatal("uncertain intent erased", err)
	}
	r.Image = "sha256:" + strings.Repeat("a", 64)
	if _, err := r.Acquire(t.Context()); !errors.Is(err, ErrCleanup) {
		t.Fatal("quarantine bypassed", err)
	}
	if err := r.AcknowledgeCleanup(t.Context(), v.Name, "review_1", false); !errors.Is(err, ErrCleanup) {
		t.Fatal("missing quiescence assertion accepted")
	}
	if err := r.AcknowledgeCleanup(t.Context(), v.Name, "review_1", true); err != nil {
		t.Fatal(err)
	}
	audit, err := os.ReadFile(filepath.Join(r.JournalDir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), "review_1") || strings.Contains(string(audit), v.Token) {
		t.Fatal("audit missing or leaked token")
	}
	l, err = r.Acquire(t.Context())
	if err != nil {
		t.Fatal("acknowledged recovery still quarantined", err)
	}
	l.Close()
}
