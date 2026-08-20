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
// os.OpenFile call added anywhere in internal/consolidate,
// internal/memory, or internal/backup that isn't on the allowlist below
// fails the build.
//
// internal/backup is included because it is the other place memory
// content gets written back to disk: a restore replays a backup bundle's
// memory/ entries onto the live PERSONA.md / learned-*.md / etc. paths,
// which is exactly the class of write this guard exists for. As of this
// commit internal/backup has no such call site yet — the restore-side
// memory write lands in a concurrently-developed PR (#1537,
// internal/backup/memoryblobs.go's restoreMemoryBlobFile), which is not
// present on this branch. Scanning internal/backup now, before that file
// exists, is deliberate: the guard is green today because there is
// nothing to catch, and it will go red the moment that PR merges with a
// tmp-write+rename-no-fsync memory write, forcing the same fix this PR
// already applied elsewhere instead of shipping a ninth silent instance.
//
// Deliberately NOT in scope: cmd/crewship/cmd_seed_data_memory.go's
// writeFileIfAbsent uses a raw os.WriteFile, but it is a dev-only seed
// helper gated by "only write if the file doesn't already exist" (never
// overwrites live content), and cmd/crewship is not a package this guard
// walks. Noted here so it isn't re-flagged or re-investigated later.
//
// Two shapes are recognised:
//
//  1. Whole-file overwrite (os.WriteFile, or os.OpenFile with
//     O_WRONLY/O_RDWR but not O_APPEND) — must go through
//     writeFileDurable / memory.WriteFileDurable instead. There is no
//     legitimate exception to this in either package today; every
//     match must be allowlisted with a reason or fixed.
//
//  2. Append (os.OpenFile ... O_APPEND ... O_WRONLY) — O_APPEND plus
//     an explicit f.Sync() before Close(). This shape buys durability
//     but NOT atomicity: O_CREATE and the first write are two syscalls,
//     so between them the file exists at zero bytes and any reader not
//     holding the writer's flock can observe it empty (#1807 — that is
//     how pins.md flaked TestPostRunTrigger_WritesIntoTheCrewBindSource,
//     and the audit watcher reads the same files). It is therefore only
//     acceptable where the read-modify-write cost is genuinely
//     prohibitive, and every allowlisted O_APPEND call is additionally
//     required — by this test, not just by the allowlist comment — to
//     have an f.Sync() call somewhere between the OpenFile line and the
//     end of its enclosing function.
//
//     consolidator.go's appendRules and snapshotPins used to be
//     allowlisted here on the "learned-*.md grows to
//     proposalDiffMaxBytes = 8MiB, re-reading it every tick is real
//     I/O" argument. #1807 retired that: appendRules already read the
//     whole file back after every append (to hand the caller the exact
//     post-write bytes for the audit blob), so the read was being paid
//     regardless and moving it ahead of the write cost nothing. Both
//     now go through memory.WriteFileDurableRoot.
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
	dirs := []string{".", "../memory", "../backup"}

	// allowedLines maps "dir|trimmed source line" to a written reason a
	// human reviewer accepted for NOT routing through the durable
	// helper. Every entry here is a whole-file overwrite that is
	// either the helper's own primitive, content that is not
	// persistent memory (lock sentinels, health probes), or a generic
	// streaming primitive that is not itself a memory-content write.
	allowedWholeFile := map[string]string{
		"../memory|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":     "durable_write.go: this line IS the writeFileDurable primitive every other call site delegates to; covered directly by TestWriteFileDurable_* in durable_write_test.go",
		"../memory|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)":    "writer.go: WriteFile's own inline temp+fsync+rename+dir-fsync sequence, written before writeFileDurable was extracted from it — already durable, not a shortcut around the helper",
		"../memory|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)": "writer_lock_unix.go: flock sentinel file, not memory content — only its existence as an flock anchor matters, not durability of its (empty) bytes",
		"../memory|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":                 "writer_lock_windows.go: same flock-sentinel reasoning as the unix build",
		`../memory|if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {`:            "provider.go: ephemeral health-probe file, created and removed within the same function call — never persisted content",
		"../backup|out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)":      "memoryblobs.go: restoreMemoryBlobFile IS a durable sequence — fsync of the tempfile, atomic rename, then fsync of the parent dir — just written inline rather than delegating. It streams each blob straight out of the bundle tar, and WriteFileDurable takes []byte, so routing through the helper would mean io.ReadAll-ing every blob into memory. Collapse the two once a streaming variant of the helper exists; the doc comment on that function says so.",
		"../backup|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":                 "keyring_flock_unix.go: flock sentinel file for the backup keyring, same reasoning as memory's writer_lock_unix.go — not memory content",
		"../backup|f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)":      "storage.go: LocalStorageOps.Create, a generic io.WriteCloser primitive backup bundle/keyring/dump code streams arbitrary bytes into (tar entries, keyring JSON, DB dumps) — not itself a memory-content write, and bundle/keyring durability is the backup subsystem's own separate, already-tracked concern",

		// *os.Root-anchored twins of the three entries above. These
		// were invisible to this guard until #1807 widened the regex
		// from `os.OpenFile(` to `.OpenFile(` — see the comment there.
		"../memory|f, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":       "durable_write.go: this line IS the WriteFileDurableRoot primitive, the root-anchored form of writeFileDurable — same standing as its os.OpenFile twin above",
		"../memory|f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)": "writer_lock_unix.go: root-anchored form of the flock sentinel open — same reasoning as its os.OpenFile twin above, only its existence as an flock anchor matters",
		"../memory|f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_RDWR, 0o600)":                 "writer_lock_windows.go: same flock-sentinel reasoning as the unix build's root-anchored open",
	}

	// appendLines lists O_APPEND call sites accepted under the
	// "append + explicit f.Sync()" shape. Presence here does not skip
	// the fsync check below — it only says "this call site is allowed
	// to use O_APPEND instead of the durable helper", the test still
	// verifies the fsync is actually present in source.
	//
	// Empty as of #1999: approve.go's appendToCanonical was the last
	// entry — "the last append-shaped canonical write [...] left as-is
	// here only to keep that fix reviewable on its own" — and it now
	// goes through memory.WriteFileDurable like the two consolidator.go
	// sites #1807 converted. Neither package has a write of persistent
	// memory content left that the append+fsync shape is right for, so
	// the next O_APPEND to appear here should be argued for rather than
	// inherited.
	appendLines := map[string]string{}

	// `.OpenFile(` rather than `os.OpenFile(`: these packages open
	// files through *os.Root handles too (root.OpenFile, l.root.OpenFile),
	// and matching only the `os.` form left every root-anchored write
	// unscanned. That blind spot is why consolidator.go's two O_APPEND
	// sites sat here allowlisted-but-unmatched after they moved to
	// os.Root — the guard would not have caught a regression in them.
	wholeFileRe := regexp.MustCompile(`os\.WriteFile\(|\.OpenFile\(`)

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
