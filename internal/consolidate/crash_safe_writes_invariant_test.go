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
// #1999 widened dirs well past the memory packages — to
// internal/orchestrator, internal/logcollector, internal/conversation,
// internal/devcontainer, internal/provider/apple and cmd/crewship — so
// that the sites that issue triaged could not be ruled benign while
// staying invisible to the guard. That is #1999's own stated acceptance
// criterion: "a site triaged as benign and INVISIBLE is how this
// backlog rebuilds itself." Two of its sites were converted
// (orchestrator/progress.go, cmd/crewship/cmd_token.go); the rest carry
// reasoned entries below.
//
// Note that widening this far changes what the guard is. Beyond
// internal/memory and internal/backup it is no longer only a
// "persistent memory content" guard — it is a raw-write guard over the
// packages listed in dirs, and the reasons below argue benign-ness on
// those packages' own terms (build artefacts, caches, one-shot CLI
// output) rather than on memory-durability terms.
//
// Keys are FILE-scoped ("../memory/writer.go|<line>"), not dir-scoped.
// That tightened in #1999: cmd/crewship alone has forty-odd files with
// near-identical one-shot output lines, and a dir-level key meant
// allowlisting one silently allowlisted every future twin in the
// package.
//
// (cmd/crewship/cmd_seed_data_memory.go's writeFileIfAbsent was called
// out here as deliberately out of scope back when cmd/crewship was not
// walked at all. It is now walked, and that site has its own entry
// below on the same "never overwrites live content" reasoning.)
//
// Three shapes are recognised:
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
//  3. Cached-handle append stream log (added by #1999) — O_APPEND on a
//     handle the type opens once and reuses for many small records,
//     where the fsync lives in a Flush/Close rather than beside each
//     write. See appendStreamLines for why this is a distinct shape and
//     what the test still demands of it (a .Sync() in the same file).
//
// What this test proves: every write in the scanned packages either
// goes through the durable helper or carries a written, reviewed reason
// for not doing so, by construction of the source text.
// What it does NOT prove: that fsync actually reaches stable storage
// on a given kernel/filesystem, or that a real process crash can't
// still lose data between fsync and the next line of code. That is
// the durable_write_test.go / persona_writefailure_test.go job (fault
// injection at the primitive/caller level) and, beyond that, a
// physical guarantee this test suite cannot exercise.
func TestNoRawFileWritesOutsideDurableHelper(t *testing.T) {
	dirs := []string{
		".", "../memory", "../backup",
		"../orchestrator", "../logcollector", "../conversation",
		"../devcontainer", "../provider/apple", "../../cmd/crewship",
	}

	// allowedWholeFile maps "file|trimmed source line" to a written reason a
	// human reviewer accepted for NOT routing through the durable
	// helper. Every entry here is a whole-file overwrite that is
	// either the helper's own primitive, content that is not
	// persistent memory (lock sentinels, health probes), or a generic
	// streaming primitive that is not itself a memory-content write.
	allowedWholeFile := map[string]string{
		"../memory/durable_write.go|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":        "durable_write.go: this line IS the writeFileDurable primitive every other call site delegates to; covered directly by TestWriteFileDurable_* in durable_write_test.go",
		"../memory/writer.go|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)":              "writer.go: WriteFile's own inline temp+fsync+rename+dir-fsync sequence, written before writeFileDurable was extracted from it — already durable, not a shortcut around the helper",
		"../memory/writer_lock_unix.go|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)": "writer_lock_unix.go: flock sentinel file, not memory content — only its existence as an flock anchor matters, not durability of its (empty) bytes",
		"../memory/writer_lock_windows.go|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":              "writer_lock_windows.go: same flock-sentinel reasoning as the unix build",
		`../memory/provider.go|if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {`:                    "provider.go: ephemeral health-probe file, created and removed within the same function call — never persisted content",
		"../backup/memoryblobs.go|out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)":           "memoryblobs.go: restoreMemoryBlobFile IS a durable sequence — fsync of the tempfile, atomic rename, then fsync of the parent dir — just written inline rather than delegating. It streams each blob straight out of the bundle tar, and WriteFileDurable takes []byte, so routing through the helper would mean io.ReadAll-ing every blob into memory. Collapse the two once a streaming variant of the helper exists; the doc comment on that function says so.",
		"../backup/keyring_flock_unix.go|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":               "keyring_flock_unix.go: flock sentinel file for the backup keyring, same reasoning as memory's writer_lock_unix.go — not memory content",
		"../backup/storage.go|f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)":               "storage.go: LocalStorageOps.Create, a generic io.WriteCloser primitive backup bundle/keyring/dump code streams arbitrary bytes into (tar entries, keyring JSON, DB dumps) — not itself a memory-content write, and bundle/keyring durability is the backup subsystem's own separate, already-tracked concern",

		// *os.Root-anchored twins of the three entries above. These
		// were invisible to this guard until #1807 widened the regex
		// from `os.OpenFile(` to `.OpenFile(` — see the comment there.
		"../memory/durable_write.go|f, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":          "durable_write.go: this line IS the WriteFileDurableRoot primitive, the root-anchored form of writeFileDurable — same standing as its os.OpenFile twin above",
		"../memory/writer_lock_unix.go|f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)": "writer_lock_unix.go: root-anchored form of the flock sentinel open — same reasoning as its os.OpenFile twin above, only its existence as an flock anchor matters",
		"../memory/writer_lock_windows.go|f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_RDWR, 0o600)":              "writer_lock_windows.go: same flock-sentinel reasoning as the unix build's root-anchored open",

		// ---- #1999: devcontainer + apple provider ----
		//
		// Build artefacts and cache entries, not persistent memory
		// content. Common to all of them: the destination is a
		// directory this code just created or owns outright, there is
		// no concurrent reader that treats an empty file as
		// authoritative, and losing one means the next provision
		// rebuilds it. Several are also STREAMING copies (io.Copy out
		// of a tar or a source file), and WriteFileDurable takes
		// []byte — routing them through it would mean buffering up to
		// the 50 MiB per-entry cap in memory to buy durability for a
		// file that is regenerated on demand.
		"../devcontainer/features.go|f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)":                                      "features.go: tar-extraction loop for a devcontainer feature, streaming each entry via io.Copy into a destination dir extractTo just created. O_TRUNC only bites if one archive names the same entry twice; there is no pre-existing content to lose.",
		`../devcontainer/imagebuilder.go|if err = os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {`:  "imagebuilder.go: the generated Dockerfile for an image build, written into a freshly-made context dir that is handed straight to the builder and discarded after. Regenerated from the devcontainer spec every build.",
		"../devcontainer/imagebuilder.go|out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)":                                   "imagebuilder.go: copyFile, the generic streaming file-copy primitive copyTree uses to populate a build context (io.Copy from an already-open source). Same standing as backup's storage.go Create above — a stream primitive, not a memory-content write.",
		"../devcontainer/provenance.go|return os.WriteFile(filepath.Join(dir, featureDigestFile), []byte(digest), 0o600)":                          "provenance.go: writeFeatureDigest records a resolved feature digest beside the extracted feature cache. Best-effort by contract — readFeatureDigest degrades a missing or unreadable digest to \"unknown\" and never fails a build, so a lost write costs a provenance note, not correctness.",
		`../devcontainer/provisioner_build.go|if err := os.WriteFile(joinPath(contextDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {`: "provisioner_build.go: the provisioner's own generated Dockerfile, same fresh-context-dir reasoning as imagebuilder.go's.",
		"../devcontainer/runtimes_fetcher.go|if err := os.WriteFile(tmp, data, 0o644); err != nil {":                                               "runtimes_fetcher.go: the runtime catalogue disk cache, and already a temp-write + os.Rename — atomic for readers, merely missing the two fsyncs. It is a re-fetchable cache of a remote catalogue, so a crash losing it costs one HTTP round trip on the next start.",
		"../provider/apple/apple_runtime.go|if err := os.WriteFile(dst, data, 0o600); err != nil {":                                                "apple_runtime.go: copies already-staged files into a container's host-mapped mount during provisioning. The source is still on disk in the staging dir, so a failed copy is retried rather than lost, and the destination is a container that is not running yet.",
		"../provider/apple/apple_runtime.go|f, err := rootFS.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)":                              "apple_runtime.go: tar-extraction into the container staging area, ANCHORED TO AN *os.Root. Deliberately not converted: memory.WriteFileDurable is not root-anchored, so swapping it in would drop the traversal fence that safepath.JoinRel + rootFS together provide on attacker-controlled tar entry names. WriteFileDurableRoot would keep the fence but takes []byte, forcing the bounded io.Copy to buffer a whole entry in memory. Staging dir, freshly created, no live readers.",

		// ---- #1999: cmd/crewship ----
		//
		// SCOPE NOTE, so these are not mistaken for individually
		// audited decisions. Widening dirs to cover cmd/crewship was
		// required by #1999 for two named sites (cmd_token.go, now
		// converted, and cmd_issue_attachments.go). Doing so also
		// surfaced ~15 further writes that #1999 never listed. They
		// were triaged AS A CLASS, not one by one, and the class is:
		// a one-shot CLI process writing a destination the operator
		// named on the command line (-o/--out/--output) or a scaffold
		// path the subcommand exists to create.
		//
		// Why that class is acceptable here: there is no concurrent
		// reader, no daemon state, and nothing downstream treats these
		// files as authoritative system state — the failure mode is
		// "the command failed, run it again", which the non-zero exit
		// already tells the operator. Overwriting a file the user
		// explicitly pointed at is also the documented contract of
		// every comparable tool (cp, curl -o).
		//
		// What this entry does NOT claim: that per-site durability was
		// considered for each. If one of these later turns out to feed
		// something that reads it back, it deserves its own line and
		// its own verdict rather than shelter under this paragraph.
		// The point of listing them individually anyway is that a NEW
		// raw write in cmd/crewship still fails this test.
		`../../cmd/crewship/cmd_admin_gdpr.go|if err := os.WriteFile(out, append(pretty, '\n'), 0o600); err != nil {`:                       "cmd_admin_gdpr.go: --out destination for a GDPR subject export. Operator-named path, 0600 already, regenerated by re-running the command.",
		"../../cmd/crewship/cmd_agent_avatar.go|if err := os.WriteFile(out, svg, 0o644); err != nil {":                                      "cmd_agent_avatar.go: -o destination for a generated avatar SVG. Operator-named path; the avatar is deterministic from the agent, so re-running reproduces it.",
		"../../cmd/crewship/cmd_eval_baseline.go|if err := os.WriteFile(path, data, 0o644); err != nil {":                                   "cmd_eval_baseline.go: saves an eval baseline to a path the subcommand was told to write. One-shot, operator-directed.",
		"../../cmd/crewship/cmd_export.go|if err := os.WriteFile(path, data, 0o600); err != nil {":                                          "cmd_export.go: writeArtifactFile, one artifact of an export bundle written into the --out dir the operator named. Chmods to 0600 immediately after for the same sensitivity reason documented there.",
		"../../cmd/crewship/cmd_export_manifest.go|if err := os.WriteFile(output, []byte(yaml), 0o644); err != nil {":                       "cmd_export_manifest.go (two call sites, identical line): --output destination for a rendered manifest. Operator-named, regenerated on re-run.",
		"../../cmd/crewship/cmd_export_page.go|if err := os.WriteFile(output, []byte(rendered), 0o644); err != nil {":                       "cmd_export_page.go: --output destination for a rendered page. Same operator-named one-shot class.",
		"../../cmd/crewship/cmd_issue_attachments.go|out, err := os.OpenFile(attachmentOutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)": "cmd_issue_attachments.go: -o destination for an attachment download, streamed via io.Copy under a 25 MiB LimitReader (so the durable helper's []byte signature would mean buffering the whole attachment). Named by #1999; kept as-is deliberately. The surrounding code ALREADY handles the partial-write case the O_TRUNC class is about — every failure path closes the handle and os.Remove's the partial file rather than leaving a truncated download that looks complete.",
		"../../cmd/crewship/cmd_prompt.go|if err := os.WriteFile(path, data, 0o600); err != nil {":                                          "cmd_prompt.go: writes a prompt payload to an operator-named path. One-shot CLI output.",
		"../../cmd/crewship/cmd_routine_init.go|if err := os.WriteFile(outPath, payload, 0o644); err != nil {":                              "cmd_routine_init.go: scaffolds a new routine file at the path the operator asked for. Creating that file IS the command's purpose.",
		"../../cmd/crewship/cmd_routine_report.go|if err := os.WriteFile(reportOutFile, []byte(out), 0o644); err != nil {":                  "cmd_routine_report.go: --out destination for a routine report. Operator-named, regenerated on re-run.",
		"../../cmd/crewship/cmd_routine_schema.go|if err := os.WriteFile(out, schemas.RoutineV1, 0o644); err != nil {":                      "cmd_routine_schema.go: dumps the embedded routine JSON schema to an operator-named path. Content is a compile-time constant, so a lost write is recovered by re-running.",
		"../../cmd/crewship/cmd_seed_data_memory.go|return os.WriteFile(path, []byte(content), 0o644)":                                      "cmd_seed_data_memory.go: writeFileIfAbsent, the dev-only seed helper. Guarded by an os.Stat that returns early when the file exists, so it never overwrites live content — the exact site this test's header comment already called out as deliberately out of scope before cmd/crewship was walked at all.",
		"../../cmd/crewship/cmd_skill_authoring.go|if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {":                      "cmd_skill_authoring.go: scaffolds a new skill file. Creating the file is the command's purpose; operator-named destination.",
		"../../cmd/crewship/cmd_skill_authoring.go|if err := os.WriteFile(dest, []byte(full), 0o644); err != nil {":                         "cmd_skill_authoring.go: writes the assembled skill body to the same scaffold destination, same reasoning.",
		"../../cmd/crewship/cmd_slash_admin.go|if err := os.WriteFile(sample, []byte(content), 0o644); err != nil {":                        "cmd_slash_admin.go: writes a sample slash-command file for the operator to edit. Scaffold output, regenerated on re-run.",
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

	// appendStreamLines is the third recognised shape, added by #1999
	// when this guard was widened past the memory packages.
	//
	// It covers append-only STREAM LOGS: the writer opens the file once,
	// caches the handle, and writes many small records through it over
	// the process lifetime. These differ from both shapes above in the
	// two ways that matter:
	//
	//   - Nothing can be lost. The file is opened O_APPEND and never
	//     truncated, so the create-then-write window exists only for the
	//     very FIRST record, and what a reader sees in it is an empty log
	//     — which is also what it would have seen a microsecond earlier,
	//     when the file did not exist. That is not the #1807 failure:
	//     there, an empty pins.md was read as authoritative "no pins".
	//
	//   - The durable helper is the wrong tool. WriteFileDurable is a
	//     whole-file replace, so routing a per-record append through it
	//     makes writing an n-record log O(n^2) — on a hot path that
	//     handles every agent log line and every chat turn.
	//
	// Entries here are NOT exempt from proving durability: the check
	// below requires an explicit .Sync() somewhere in the same file, so
	// the type must own a real flush path (a Flush/Close that fsyncs)
	// rather than merely hoping the page cache is written back. What
	// this shape gives up, deliberately, is a per-record fsync.
	appendStreamLines := map[string]string{
		"../logcollector/writer.go|f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)": "writer.go: Writer.Append's per-(crew,agent) agent log. Handle cached in w.files and reused for every log line; Writer.Flush fsyncs them all and Close releases them. Append-only, no prior content to lose, and a whole-file rewrite per log line would be quadratic on the hottest write path in the process.",
		"../conversation/store.go|f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)":  "store.go: Store.Append's per-session chat JSONL. Same cached-handle stream-log shape as logcollector; Store.Flush/Close fsync (added in #1999 — before that this file had no Sync at all, so its own \"the JSONL is the durable source of truth\" comment was false). Append-only; the DB mirror alongside it is explicitly best-effort, the JSONL is the record.",
	}

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
				// Keyed by FILE, not by directory. #1999 widened dirs
				// to include cmd/crewship, where forty-odd files share
				// near-identical one-shot output lines
				// (`os.WriteFile(path, data, 0o600)` appears in
				// cmd_export.go and cmd_prompt.go verbatim). Under the
				// old dir-level key, allowlisting one of those silently
				// allowlisted every future twin anywhere in the package
				// — the "backlog rebuilds itself invisibly" failure this
				// guard exists to prevent. A site that moves to another
				// file now needs its entry re-stated, which is the point.
				// ToSlash because f comes from filepath.Glob, which
				// yields "..\memory\writer.go" on Windows while every
				// key below is written with forward slashes — without
				// it every lookup misses there and the guard fails the
				// build on a clean tree. (The previous dir-scoped key
				// used the literal dirs entry, which was already
				// forward-slashed, so this only became reachable when
				// the keys went file-scoped.)
				key := filepath.ToSlash(f) + "|" + line
				isAppend := strings.Contains(line, "O_APPEND")
				if isAppend {
					if reason, ok := appendLines[key]; ok {
						_ = reason
						if !enclosingFuncSyncsFile(lines, i) {
							t.Errorf("%s:%d: O_APPEND write %q is allowlisted as append+fsync but no f.Sync() found before the end of its enclosing function", f, i+1, line)
						}
						continue
					}
					if reason, ok := appendStreamLines[key]; ok {
						_ = reason
						// The stream-log shape trades the per-record
						// fsync for an explicit flush path on the type.
						// Require that path to exist in source: without
						// a .Sync() anywhere in the file, the entry is
						// claiming a durability boundary it does not have
						// (which is exactly what conversation/store.go
						// was doing before #1999).
						if !fileSyncsSomewhere(lines) {
							t.Errorf("%s:%d: O_APPEND write %q is allowlisted as a cached-handle stream log, but this file contains no .Sync() at all — the type must own a Flush/Close that fsyncs, or the entry is claiming durability it does not provide", f, i+1, line)
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

// fileSyncsSomewhere reports whether the source file contains any
// .Sync() call. Used for the cached-handle stream-log shape, where the
// fsync deliberately lives in a Flush/Close method rather than in the
// function holding the O_APPEND open, so enclosingFuncSyncsFile's
// single-function window is the wrong scope.
func fileSyncsSomewhere(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, ".Sync()") {
			return true
		}
	}
	return false
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
