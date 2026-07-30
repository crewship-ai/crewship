package consolidate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// crash_safe_writes_invariant_test.go — the source-guard half of the
// durable-write fix (2026-07-30 crash-safety audit). internal/memory's
// writeFileDurable (write-temp + fsync + atomic rename + fsync parent
// dir) is the one place PERSONA.md, learned-*.md, pins.md, lesson
// files, proposal staging files, version blobs and quarantine copies
// are supposed to reach disk through. Two call sites (WritePersona and
// appendToCanonical) had quietly bypassed it with a plain os.WriteFile
// or an un-synced O_APPEND. This test closes the CLASS of bug, not
// just those two instances: any future os.WriteFile / mutating
// os.OpenFile call added anywhere in internal/consolidate or
// internal/memory that isn't on the allowlist below fails the build.
//
// Two shapes are recognised:
//
//  1. Whole-file overwrite (os.WriteFile, or os.OpenFile with
//     O_WRONLY/O_RDWR but not O_APPEND) — must go through
//     writeFileDurable / memory.WriteFileDurable instead. There is no
//     legitimate exception to this in either package today; every
//     match must be allowlisted with a reason or fixed.
//  2. Append (os.OpenFile ... O_APPEND ... O_WRONLY) — read-modify-
//     write via the durable helper is not the right shape for a file
//     that only ever grows (learned-*.md is read by the diff endpoint
//     up to proposalDiffMaxBytes = 8MiB; re-reading and rewriting that
//     on every consolidation tick is real, avoidable I/O). The
//     accepted alternative is O_APPEND + an explicit f.Sync() before
//     Close(), matching the convention consolidator.go's appendRules
//     already used before this fix. Every allowlisted O_APPEND call is
//     additionally required — by this test, not just by the allowlist
//     comment — to have an f.Sync() call somewhere between the
//     OpenFile line and the end of its enclosing function.
//
// What this test proves: every write of persistent memory content in
// these two packages either goes through the durable helper or is an
// append that explicitly fsyncs, by construction of the source text.
// What it does NOT prove: that fsync actually reaches stable storage
// on a given kernel/filesystem, or that a real process crash can't
// still lose data between fsync and the next line of code. That is
// the durable_write_test.go / persona_writefailure_test.go job (fault
// injection at the primitive/caller level) and, beyond that, a
// physical guarantee this test suite cannot exercise.
func TestNoRawFileWritesOutsideDurableHelper(t *testing.T) {
	dirs := []string{".", "../memory"}

	// allowedLines maps "dir|trimmed source line" to a written reason a
	// human reviewer accepted for NOT routing through the durable
	// helper. Every entry here is a whole-file overwrite that is
	// either the helper's own primitive, or content that is not
	// persistent memory (lock sentinels, health probes).
	allowedWholeFile := map[string]string{
		"../memory|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":     "durable_write.go: this line IS the writeFileDurable primitive every other call site delegates to; covered directly by TestWriteFileDurable_* in durable_write_test.go",
		"../memory|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)":    "writer.go: WriteFile's own inline temp+fsync+rename+dir-fsync sequence, written before writeFileDurable was extracted from it — already durable, not a shortcut around the helper",
		"../memory|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)": "writer_lock_unix.go: flock sentinel file, not memory content — only its existence as an flock anchor matters, not durability of its (empty) bytes",
		"../memory|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":                 "writer_lock_windows.go: same flock-sentinel reasoning as the unix build",
		`../memory|if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {`:            "provider.go: ephemeral health-probe file, created and removed within the same function call — never persisted content",
	}

	// appendLines lists O_APPEND call sites accepted under the
	// "append + explicit f.Sync()" shape. Presence here does not skip
	// the fsync check below — it only says "this call site is allowed
	// to use O_APPEND instead of the durable helper", the test still
	// verifies the fsync is actually present in source.
	appendLines := map[string]string{
		".|f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)":          "consolidator.go (appendRules, snapshotPins): append-only canonical/pins files; read-modify-write via the durable helper would mean re-reading up to proposalDiffMaxBytes (8MiB) on every tick",
		".|f, err := os.OpenFile(canonicalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)": "approve.go (appendToCanonical): same append-only reasoning as consolidator.go's appendRules",
	}

	wholeFileRe := regexp.MustCompile(`os\.WriteFile\(|os\.OpenFile\(`)

	checkedFiles := 0
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatalf("glob %s: %v", dir, err)
		}
		if len(files) == 0 {
			t.Fatalf("no .go files found under %s — test is looking in the wrong place", dir)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			checkedFiles++
			src, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read %s: %v", f, err)
			}
			lines := strings.Split(string(src), "\n")
			for i, raw := range lines {
				line := strings.TrimSpace(raw)
				if !wholeFileRe.MatchString(line) {
					continue
				}
				if !strings.Contains(line, "O_WRONLY") && !strings.Contains(line, "O_RDWR") &&
					!strings.Contains(line, "os.WriteFile(") {
					// Read-only open (e.g. O_RDONLY) — not a write.
					continue
				}
				key := dir + "|" + line
				isAppend := strings.Contains(line, "O_APPEND")
				if isAppend {
					if reason, ok := appendLines[key]; ok {
						_ = reason
						if !enclosingFuncSyncsFile(lines, i) {
							t.Errorf("%s:%d: O_APPEND write %q is allowlisted as append+fsync but no f.Sync() found before the end of its enclosing function", f, i+1, line)
						}
						continue
					}
					t.Errorf("%s:%d: new O_APPEND write %q is not on the append allowlist — either add an f.Sync() before Close() and a reasoned allowlist entry, or route it through memory.WriteFileDurable", f, i+1, line)
					continue
				}
				if reason, ok := allowedWholeFile[key]; ok {
					_ = reason
					continue
				}
				t.Errorf("%s:%d: raw file write %q bypasses the durable-write helper (writeFileDurable / memory.WriteFileDurable) — either use it or add a reasoned allowlist entry to this test", f, i+1, line)
			}
		}
	}
	if checkedFiles == 0 {
		t.Fatal("no non-test .go files were scanned")
	}
}

// enclosingFuncSyncsFile scans forward from lines[openIdx] (the
// os.OpenFile call) to the end of its enclosing top-level function —
// approximated as the next line starting with "func " or EOF — and
// reports whether an f.Sync() call appears anywhere in that span.
func enclosingFuncSyncsFile(lines []string, openIdx int) bool {
	for i := openIdx + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "func ") {
			return false
		}
		if strings.Contains(lines[i], "f.Sync()") {
			return true
		}
	}
	return false
}
