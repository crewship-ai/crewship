package logging

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

// These tests share the package-global controller, so they run sequentially
// (no t.Parallel) and each starts by rebuilding the logger at info, which
// resets the baseline. ResetLevel in a defer cancels any pending TTL timer so
// it can't fire during a later test.

func TestLevelControl_RuntimeToggle(t *testing.T) {
	defer ResetLevel()
	var buf bytes.Buffer
	logger := New("info", "json", &buf)

	// At info, debug is suppressed on the LIVE logger.
	logger.Debug("d1")
	if buf.Len() != 0 {
		t.Fatalf("debug leaked at info level: %q", buf.String())
	}

	// Flip to debug at runtime — same logger instance must now emit debug.
	prev, err := SetLevel("debug", 0)
	if err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	if prev != "info" {
		t.Errorf("previous = %q, want info", prev)
	}
	logger.Debug("d2")
	if !strings.Contains(buf.String(), "d2") {
		t.Fatalf("debug not emitted after SetLevel(debug): %q", buf.String())
	}

	// Reset returns to the configured baseline.
	ResetLevel()
	buf.Reset()
	logger.Debug("d3")
	if buf.Len() != 0 {
		t.Fatalf("debug leaked after ResetLevel: %q", buf.String())
	}
}

func TestLevelControl_TTLAutoReverts(t *testing.T) {
	defer ResetLevel()
	var buf bytes.Buffer
	logger := New("info", "json", &buf)

	if _, err := SetLevel("debug", 40*time.Millisecond); err != nil {
		t.Fatalf("SetLevel: %v", err)
	}
	cur, base, exp := LevelState()
	if cur != "debug" || base != "info" {
		t.Fatalf("state = (%s, %s), want (debug, info)", cur, base)
	}
	if exp.IsZero() {
		t.Fatal("expiry not set for a timed override")
	}
	logger.Debug("during")
	if !strings.Contains(buf.String(), "during") {
		t.Fatal("debug suppressed during active override")
	}

	// Poll for the auto-revert rather than a fixed sleep (CI scheduler slack).
	deadline := time.Now().Add(2 * time.Second)
	for {
		cur, _, exp = LevelState()
		if cur == "info" && exp.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not auto-revert within deadline (level=%s)", cur)
		}
		time.Sleep(5 * time.Millisecond)
	}
	buf.Reset()
	logger.Debug("after")
	if buf.Len() != 0 {
		t.Fatalf("debug leaked after auto-revert: %q", buf.String())
	}
}

func TestSetLevel_RejectsUnknown(t *testing.T) {
	defer ResetLevel()
	_ = New("info", "json", io.Discard)
	if _, err := SetLevel("verbose", 0); err == nil {
		t.Fatal("SetLevel(verbose) should error, not silently apply info")
	}
	// A rejected set must not have changed the live level.
	if cur, _, _ := LevelState(); cur != "info" {
		t.Errorf("level changed to %q after a rejected set", cur)
	}
}

func TestParseLevelStrict(t *testing.T) {
	ok := map[string]string{
		"debug": "debug", "INFO": "info", "warn": "warn",
		"warning": "warn", "error": "error", "fatal": "error", " Debug ": "debug",
	}
	for in, want := range ok {
		l, valid := parseLevelStrict(in)
		if !valid || levelString(l) != want {
			t.Errorf("parseLevelStrict(%q) = (%s, %v), want (%s, true)", in, levelString(l), valid, want)
		}
	}
	for _, bad := range []string{"", "verbose", "trace", "nonsense"} {
		if _, valid := parseLevelStrict(bad); valid {
			t.Errorf("parseLevelStrict(%q) accepted, want rejected", bad)
		}
	}
}
