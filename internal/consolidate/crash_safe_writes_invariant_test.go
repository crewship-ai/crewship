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
// #2124 then read the sites #1999 had only triaged as a class (the
// cmd/crewship group plus four in devcontainer/apple) one at a time,
// converted the four whose reader is another process or a later
// command (apple_runtime.go's bind-mount CopyToContainer branch,
// cmd_prompt.go, cmd_eval_baseline.go, cmd_seed_data_memory.go's
// writeFileIfAbsent — the one this comment used to call "deliberately
// out of scope", whose os.Stat guard meant a torn seed was never
// repaired), and widened the regex to the .Create( / .CreateTemp(
// shapes, which had let os.Create + io.Copy sites through unseen.
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
		`../../cmd/crewship/cmd_seed_team_chat.go|lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)`: "cmd_seed_team_chat.go: empty exclusive-create invocation lock, not account state. O_EXCL must fail while another seed owns the sentinel; atomic replacement through WriteFileDurable would destroy that exclusion. The lock is closed immediately, never receives bytes and is removed on return; a stale lock fails closed after a crash. TestSeedTeamChatStateLockStopsParallelMutations verifies the exclusion.",
		"../memory/durable_write.go|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":                       "durable_write.go: this line IS the writeFileDurable primitive every other call site delegates to; covered directly by TestWriteFileDurable_* in durable_write_test.go",
		"../memory/writer.go|f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)":                             "writer.go: WriteFile's own inline temp+fsync+rename+dir-fsync sequence, written before writeFileDurable was extracted from it — already durable, not a shortcut around the helper",
		"../memory/writer_lock_unix.go|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)":                "writer_lock_unix.go: flock sentinel file, not memory content — only its existence as an flock anchor matters, not durability of its (empty) bytes",
		"../memory/writer_lock_windows.go|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":                             "writer_lock_windows.go: same flock-sentinel reasoning as the unix build",
		`../memory/provider.go|if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {`:                                   "provider.go: ephemeral health-probe file, created and removed within the same function call — never persisted content",
		"../backup/memoryblobs.go|out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)":                          "memoryblobs.go: restoreMemoryBlobFile IS a durable sequence — fsync of the tempfile, atomic rename, then fsync of the parent dir — just written inline rather than delegating. It streams each blob straight out of the bundle tar, and WriteFileDurable takes []byte, so routing through the helper would mean io.ReadAll-ing every blob into memory. Collapse the two once a streaming variant of the helper exists; the doc comment on that function says so.",
		"../backup/keyring_flock_unix.go|f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR, 0o600)":                              "keyring_flock_unix.go: flock sentinel file for the backup keyring, same reasoning as memory's writer_lock_unix.go — not memory content",
		"../backup/storage.go|f, err := os.OpenFile(clean, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)":                              "storage.go: LocalStorageOps.Create, a generic io.WriteCloser primitive backup bundle/keyring/dump code streams arbitrary bytes into (tar entries, keyring JSON, DB dumps) — not itself a memory-content write, and bundle/keyring durability is the backup subsystem's own separate, already-tracked concern",

		// *os.Root-anchored twins of the three entries above. These
		// were invisible to this guard until #1807 widened the regex
		// from `os.OpenFile(` to `.OpenFile(` — see the comment there.
		"../memory/durable_write.go|f, err := root.OpenFile(tmpName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)":          "durable_write.go: this line IS the WriteFileDurableRoot primitive, the root-anchored form of writeFileDurable — same standing as its os.OpenFile twin above",
		"../memory/writer_lock_unix.go|f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0o600)": "writer_lock_unix.go: root-anchored form of the flock sentinel open — same reasoning as its os.OpenFile twin above, only its existence as an flock anchor matters",
		"../memory/writer_lock_windows.go|f, err := l.root.OpenFile(l.name, os.O_CREATE|os.O_RDWR, 0o600)":              "writer_lock_windows.go: same flock-sentinel reasoning as the unix build's root-anchored open",

		// ---- #1999 / #2124: devcontainer + apple provider ----
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
		//
		// #2124 read the four sites #1999 had not individually
		// triaged. One (apple_runtime.go's bind-mount branch of
		// CopyToContainer) was converted: its reader is the agent
		// process inside the container, and the old reason's "not
		// running yet" claim was false. The other three stay, with
		// reasons that name their reader.
		"../devcontainer/features.go|f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)":                                      "features.go: extractTarGz's tar-extraction loop for a devcontainer feature, streaming each entry via io.Copy into the private temp dir its caller just made with createExtractTempDir (and RemoveAll's on any failure). O_TRUNC only bites if one archive names the same entry twice; there is no pre-existing content to lose.",
		`../devcontainer/imagebuilder.go|if err = os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {`:  "imagebuilder.go: the generated Dockerfile for an image build, written into a freshly-made context dir that is handed straight to the builder and discarded after. Regenerated from the devcontainer spec every build.",
		"../devcontainer/imagebuilder.go|out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)":                                   "imagebuilder.go: copyFile, the generic streaming file-copy primitive copyTree uses to populate a build context (io.Copy from an already-open source). Same standing as backup's storage.go Create above — a stream primitive, not a memory-content write.",
		"../devcontainer/provenance.go|return os.WriteFile(filepath.Join(dir, featureDigestFile), []byte(digest), 0o600)":                          "provenance.go: writeFeatureDigest, called only from FeatureDownloader.pull, which writes the digest into the extraction temp dir BEFORE the os.Rename that publishes the whole feature dir — so no reader can see the file before it is complete, and it moves atomically with install.sh. Its one reader, readFeatureDigest (via resolveFromCache), degrades a missing or malformed digest to \"\" and never fails a build; a power loss between the dir rename and writeback would tear install.sh in the same dir just as readily, and IsCached would then reject the whole cache entry.",
		`../devcontainer/provisioner_build.go|if err := os.WriteFile(joinPath(contextDir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {`: "provisioner_build.go: stageBuildContextWithSteps writing the generated Dockerfile into the os.MkdirTemp context dir it just made, which the same call hands to the builder and RemoveAll's. Regenerated by GenerateDockerfile on every build; no reader outside this process, no previous content.",
		"../devcontainer/provisioner_build.go|out, err := os.Create(dst) // #nosec G304 — dst is an internally built temp path":                    "provisioner_build.go: tarTree, streaming a feature dir into <contextDir>/features/<id>.tar for the same fresh build context as the Dockerfile entry above (os.Create + tar.Writer, so the durable helper's []byte signature would mean buffering the archive). Made visible to this guard by #2124's .Create( widening.",
		"../devcontainer/runtimes_fetcher.go|if err := os.WriteFile(tmp, data, 0o644); err != nil {":                                               "runtimes_fetcher.go: RuntimeFetcher.writeDiskCache — already a temp-write + os.Rename, so its reader, readDiskCache (via GetRuntimes), sees whole files only; what is missing is the two fsyncs. A power loss that leaves the renamed file empty is rejected by readDiskCache's json.Unmarshal, GetRuntimes then serves FallbackRuntimeCatalog, and server_lifecycle.go's startCatalogRefresh rewrites the cache within 60s of the next start. A re-fetchable cache of a remote catalogue; converting would buy fsyncs for a file whose loss costs one HTTP round trip.",
		`../devcontainer/catalog_fetcher.go|tmpFile, err := os.CreateTemp(f.cacheDir, featureCatalogFile+".*.tmp")`:                                "catalog_fetcher.go: CatalogFetcher.writeDiskCache, the feature-catalogue twin of runtimes_fetcher.go's entry — os.CreateTemp + os.Rename, atomic for its reader readDiskCache (via GetCatalog), no fsync. Same degradation path: an empty file after power loss fails json.Unmarshal, GetCatalog serves the embedded fallback, startCatalogRefresh re-fetches on the next start. Made visible by #2124's .CreateTemp( widening.",
		"../provider/apple/apple_runtime.go|f, err := rootFS.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)":                              "apple_runtime.go: unpackTarInto, tar-extraction into the CopyToContainer staging area, ANCHORED TO AN *os.Root. Deliberately not converted: memory.WriteFileDurable is not root-anchored, so swapping it in would drop the traversal fence that safepath.JoinRel + rootFS together provide on attacker-controlled tar entry names. WriteFileDurableRoot would keep the fence but takes []byte, forcing the bounded io.Copy to buffer a whole entry in memory. Staging dir, freshly created by os.MkdirTemp, no live readers, RemoveAll'd on return.",

		// ---- #1999: backup's StorageOps callers ----
		//
		// Made visible by #2124's .Create( / .CreateTemp( widening.
		// Every one of these goes through the StorageOps interface to
		// LocalStorageOps.Create / LocalStorageOps.CreateTemp (the
		// storage.go primitives above), and every one is backup's own
		// ".partial then Rename" or "temp then stream" idiom. Bundle
		// and keyring durability is the backup subsystem's separately
		// tracked concern, as the storage.go entry already says — note
		// for whoever picks that up that none of these fsync before
		// the rename.
		"../backup/keyring.go|w, err := k.storage.Create(ctx, partial, 0o600)":                                       "keyring.go: Keyring.saveLocked writing the keyring JSON to <path>.partial, then storage.Rename over the live file — atomic for readers, no fsync. Through LocalStorageOps.Create.",
		"../backup/restorer.go|f, err := st.CreateTemp(ctx, tempDir, safe+\"-*.tar\")":                               "restorer.go: ExtractPayload's per-section tar sinks in the restore temp dir, streamed out of the payload. Fresh unique files nothing reads until extraction finishes. Through LocalStorageOps.CreateTemp.",
		`../backup/runner_create.go|payloadFile, err := st.CreateTemp(ctx, "", "crewship-backup-payload-*.tar.zst")`: "runner_create.go: CreateBackup's streamed payload temp, removed on every exit path. Through LocalStorageOps.CreateTemp.",
		`../backup/runner_create.go|sealedFile, err := st.CreateTemp(ctx, "", "crewship-backup-sealed-*")`:           "runner_create.go: CreateBackup's sealed-payload temp, streamed into the bundle in step 8 and removed after. Through LocalStorageOps.CreateTemp.",
		"../backup/runner_create.go|outFile, err := st.Create(ctx, partialPath, 0o600)":                              "runner_create.go: CreateBackup's final bundle, written as <bundle>.partial and st.Rename'd into place — the partial is removed on any failure, so a torn bundle never carries the real name. No fsync before the rename. Through LocalStorageOps.Create.",
		"../backup/storage.go|f, err := os.CreateTemp(cleanDir, pattern)":                                            "storage.go: LocalStorageOps.CreateTemp, the primitive the four entries above reach — a fresh O_EXCL file with a random name, so it can never truncate anything a reader holds. Same standing as LocalStorageOps.Create above.",

		// ---- #2124: cmd/crewship, read one site at a time ----
		//
		// #1999 widened dirs to cover cmd/crewship for two named sites
		// and surfaced the rest, which it triaged AS A CLASS: a one-shot
		// CLI process writing a destination the operator named on the
		// command line (-o/--out/--output) or a scaffold path the
		// subcommand exists to create. #2124 read each one.
		//
		// Three of them were NOT that class and are now converted:
		// cmd_prompt.go (promptSaveCmd writes ~/.crewship/prompts/<name>
		// that promptUseCmd pipes into ask/run), cmd_eval_baseline.go
		// (evalBaselineSaveCmd writes ~/.crewship/eval-baselines/<name>
		// that `eval baseline diff` reads back in CI) and
		// cmd_seed_data_memory.go (writeFileIfAbsent lays down
		// PERSONA.md/pins.md that the server reads, and its os.Stat
		// guard meant a torn first write was never repaired). Each has
		// a *_durable_test.go that fails on the old code.
		//
		// What makes the remaining entries acceptable, per site rather
		// than per class: the operator typed the destination, the
		// command prints "wrote <path>" only after the write returned,
		// and a failed write is a non-zero exit naming the path — so a
		// torn file is visible to the person who asked for it, and
		// re-running the command reproduces it. Nothing in this
		// process or the server reads any of these paths back.
		// Overwriting a file the user explicitly pointed at is also the
		// documented contract of every comparable tool (cp, curl -o).
		//
		// Two of the os.Create entries (cmd_activity.go,
		// cmd_system_openapi.go) `defer f.Close()` on the writable
		// handle, so a Close error would be swallowed after the success
		// line; cmd_issue_attachments.go and cmd_backup_admin.go show
		// the explicit-close shape. Noted, not fixed here — a Close
		// error on a local file after successful writes is not the
		// torn-write class this guard is about.
		`../../cmd/crewship/cmd_admin_gdpr.go|if err := os.WriteFile(out, append(pretty, '\n'), 0o600); err != nil {`:                       "cmd_admin_gdpr.go: adminGDPRExportCmd's --out destination for a GDPR subject export. Operator-named path, 0600 already, PrintSuccess only after the write; regenerated by re-running the command.",
		"../../cmd/crewship/cmd_activity.go|f, err := os.Create(outPath)":                                                                   "cmd_activity.go: activityCmd's --out file for --export ndjson/csv, encoded straight into the handle. Operator-named path, PrintSuccess after the encoder finishes; re-running reproduces it from the API. Made visible by #2124's .Create( widening.",
		"../../cmd/crewship/cmd_agent_avatar.go|if err := os.WriteFile(out, svg, 0o644); err != nil {":                                      "cmd_agent_avatar.go: agentAvatarShowCmd's --out destination for the stored avatar SVG fetched from the server. Operator-named path; the server still holds the avatar, so re-running reproduces it.",
		"../../cmd/crewship/cmd_backup_admin.go|f, err := os.Create(dest)":                                                                  "cmd_backup_admin.go: backupDownloadCmd's --out (default: the bundle's basename), streamed via io.Copy from the download body. Refuses an existing file without --force, closes explicitly and os.Remove's the partial on any write or close error — the same partial-file handling as cmd_issue_attachments.go. Made visible by #2124's .Create( widening.",
		"../../cmd/crewship/cmd_doctor.go|f, err := os.CreateTemp(dataDir.Root, \".doctor-write-*.tmp\")":                                   "cmd_doctor.go: checkDataDirWritable's touch-test probe — created, closed and removed within the function, never written to. Same standing as memory's provider.go health probe above.",
		"../../cmd/crewship/cmd_export.go|if err := os.WriteFile(path, data, 0o600); err != nil {":                                          "cmd_export.go: writeArtifactFile, one artifact (prompt.md, response.md, timeline.txt, and writeJSONFile's JSON) of an export bundle written into exportCmd's --out dir (default ./run-<run-id>). Chmods to 0600 immediately after for the sensitivity reason documented at exportMkdir. Nothing reads the bundle back; it exists to be read by a person or diffed in git.",
		"../../cmd/crewship/cmd_export_manifest.go|if err := os.WriteFile(output, []byte(yaml), 0o644); err != nil {":                       "cmd_export_manifest.go (two call sites, identical line): runExportWorkspace and runExportCrew's --output destination for a rendered manifest, \"wrote <path>\" on stderr after. Operator-named, regenerated from the API on re-run.",
		"../../cmd/crewship/cmd_export_page.go|if err := os.WriteFile(output, []byte(rendered), 0o644); err != nil {":                       "cmd_export_page.go: runExportPage's --output destination for the rendered page manifests, \"wrote <path>\" after. Refuses to write at all when there are no pages, precisely so an existing export is not truncated to nothing. Operator-named, regenerated on re-run.",
		"../../cmd/crewship/cmd_issue_attachments.go|out, err := os.OpenFile(attachmentOutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)": "cmd_issue_attachments.go: issueAttachmentCmd's -o destination for an attachment download, streamed via io.Copy under a 25 MiB LimitReader (so the durable helper's []byte signature would mean buffering the whole attachment). Named by #1999; kept as-is deliberately. The surrounding code ALREADY handles the partial-write case the O_TRUNC class is about — every failure path closes the handle and os.Remove's the partial file rather than leaving a truncated download that looks complete.",
		`../../cmd/crewship/cmd_persona.go|tmp, err := os.CreateTemp("", "persona-*"+ext)`:                                                  "cmd_persona.go: openInEditor's scratch file for $EDITOR — a fresh unique temp, removed on return, whose only reader is the editor this process is about to launch and then this process reading the result back. Not persisted anywhere.",
		"../../cmd/crewship/cmd_routine_init.go|if err := os.WriteFile(outPath, payload, 0o644); err != nil {":                              "cmd_routine_init.go: routineInitCmd's --output destination for a routine skeleton (or a fetched definition). Creating that file IS the command's purpose; it then tells the operator to edit it and run `routine validate` on it, which would reject a torn one.",
		"../../cmd/crewship/cmd_routine_report.go|if err := os.WriteFile(reportOutFile, []byte(out), 0o644); err != nil {":                  "cmd_routine_report.go: routineReportCmd's --out destination for a rendered md/html report, \"Wrote ... report to <path>\" after. Operator-named, regenerated from the API on re-run.",
		"../../cmd/crewship/cmd_routine_schema.go|if err := os.WriteFile(out, schemas.RoutineV1, 0o644); err != nil {":                      "cmd_routine_schema.go: routineSchemaCmd's --output destination for the embedded routine JSON schema. Content is a compile-time constant, so a lost write is recovered by re-running.",
		"../../cmd/crewship/cmd_seed_team_chat.go|f, err := os.CreateTemp(filepath.Dir(path), \".accounts-*\")":                             "cmd_seed_team_chat.go: teamSeedSave, the dev seed's private account state — already a full durable sequence inline (CreateTemp beside the target, Chmod 0600, Write, f.Sync, Close, os.Rename), short only of the parent-dir fsync. Its reader is the next seed invocation under the same exclusive lock. Made visible by #2124's .CreateTemp( widening.",
		"../../cmd/crewship/cmd_self_update.go|f, err := os.CreateTemp(dir, \".crewship-write-probe-*\")":                                   "cmd_self_update.go: dirWritable's probe — created, closed and removed within the function, never written to. Same standing as cmd_doctor.go's.",
		"../../cmd/crewship/cmd_skill_authoring.go|if err := os.WriteFile(dest, []byte(content), 0o644); err != nil {":                      "cmd_skill_authoring.go: skillInitCmd scaffolding <--output or ./<slug>>/SKILL.md. Refuses an existing file without --force; creating the file is the command's purpose and it tells the operator to edit it next.",
		"../../cmd/crewship/cmd_skill_authoring.go|if err := os.WriteFile(dest, []byte(full), 0o644); err != nil {":                         "cmd_skill_authoring.go: skillExportCmd's --output destination (a directory means <slug>.md inside it) for a SKILL.md reassembled from the server's copy, PrintSuccess after. Operator-named; the server still holds the skill, so re-running reproduces it.",
		"../../cmd/crewship/cmd_slash_admin.go|if err := os.WriteFile(sample, []byte(content), 0o644); err != nil {":                        "cmd_slash_admin.go: slashInitCmd writing the sample review.md into cli.DefaultSlashDir, guarded by an os.Stat so it never overwrites one that exists. The command prints the path and tells the operator to try it, so a torn sample is seen by the person who asked for it; it is a template to edit, not state anything reads back unprompted.",
		"../../cmd/crewship/cmd_system_openapi.go|f, err := os.Create(path)":                                                                "cmd_system_openapi.go: systemOpenAPICmd's --out destination for the spec, io.Copy'd from the response body after the content-type check. Operator-named path, PrintSuccess after the copy; re-running reproduces it from the server. Made visible by #2124's .Create( widening.",
		"../../cmd/crewship/cmd_telemetry.go|f, err := os.CreateTemp(filepath.Dir(dbPath), \".crewship-ro-probe-*.tmp\")":                   "cmd_telemetry.go: walIndexUnbuildable's writability probe beside the SQLite file — created, closed and removed within the function, never written to. Same standing as cmd_doctor.go's.",
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
	// below requires the file's own Close method to fsync, so the type
	// must own a boundary the process actually reaches rather than
	// merely hoping the page cache is written back. What this shape
	// gives up, deliberately, is a per-record fsync.
	//
	// Close, not "a Sync somewhere in the file": the looser form was
	// tried first and both entries below defeated it — store.go had no
	// Sync at all, and writer.go had one in a Flush with no caller
	// outside tests, which fsyncs nothing in production while reading
	// as a durability boundary.
	appendStreamLines := map[string]string{
		"../logcollector/writer.go|f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)": "writer.go: Writer.Append's per-(crew,agent) agent log. Handle cached in w.files and reused for every log line; Writer.Close fsyncs them all before releasing them, and Server.Shutdown calls it. (Writer.Flush fsyncs too but has no caller outside tests, so it is NOT what makes this entry safe — before #1999 the Sync lived only there and these logs were never flushed in production.) Append-only, no prior content to lose, and a whole-file rewrite per log line would be quadratic on the hottest write path in the process.",
		"../conversation/store.go|f, err = os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0640)":  "store.go: Store.Append's per-session chat JSONL. Same cached-handle stream-log shape as logcollector; Store.Flush/Close fsync (added in #1999 — before that this file had no Sync at all, so its own \"the JSONL is the durable source of truth\" comment was false). Append-only; the DB mirror alongside it is explicitly best-effort, the JSONL is the record.",
	}

	// `.OpenFile(` rather than `os.OpenFile(`: these packages open
	// files through *os.Root handles too (root.OpenFile, l.root.OpenFile),
	// and matching only the `os.` form left every root-anchored write
	// unscanned. That blind spot is why consolidator.go's two O_APPEND
	// sites sat here allowlisted-but-unmatched after they moved to
	// os.Root — the guard would not have caught a regression in them.
	//
	// `.Create(` and `.CreateTemp(` (#2124): os.Create is
	// O_RDWR|O_CREATE|O_TRUNC — the whole-file-overwrite shape spelled
	// differently — and four sites in the walked packages used it while
	// the guard matched only os.WriteFile and .OpenFile, so an
	// `os.Create` + `io.Copy` into an operator's -o path was invisible
	// here. os.CreateTemp never truncates anything, but it is how every
	// hand-rolled "temp + rename" sequence starts, and those are exactly
	// the sites that need reading for a missing fsync. The method form
	// covers *os.Root.Create too, same as .OpenFile above.
	wholeFileRe := regexp.MustCompile(`os\.WriteFile\(|\.OpenFile\(|\.Create\(|\.CreateTemp\(`)

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
					!strings.Contains(line, "os.WriteFile(") &&
					!strings.Contains(line, ".Create(") && !strings.Contains(line, ".CreateTemp(") {
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
						// fsync for a durability boundary on the type.
						// Require that boundary to be Close, not merely
						// "a .Sync() somewhere in the file": a Flush
						// method with no caller satisfies the loose form
						// while fsyncing nothing in production, which is
						// what conversation/store.go (no Sync at all) and
						// logcollector/writer.go (Sync only in an uncalled
						// Flush) were both doing before #1999. Close is
						// the method the process is guaranteed to reach.
						if !closeSyncsFile(lines) {
							t.Errorf("%s:%d: O_APPEND write %q is allowlisted as a cached-handle stream log, but this file's Close does not fsync — the type must own a Close that syncs its handles, or the entry is claiming durability it does not provide (a Flush nobody calls is not a durability boundary)", f, i+1, line)
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

// closeSyncsFile reports whether the file declares a Close method whose
// body fsyncs. Used for the cached-handle stream-log shape, where the
// fsync deliberately lives in a method rather than beside the O_APPEND
// open, so enclosingFuncSyncsFile's single-function window is the wrong
// scope.
//
// Why CLOSE specifically, and not "a .Sync() anywhere in the file":
// that looser form is satisfied by a Flush method nobody calls, which
// is a durability boundary on paper only. logcollector.Writer was
// exactly that — a Flush with an fsync and zero non-test callers, next
// to a Close that released the descriptors without flushing them —
// while its allowlist entry claimed "Flush fsyncs them all and Close
// releases them" as the reason the site was safe. Close is the one
// method the process is actually guaranteed to reach (both writers are
// closed from Server.Shutdown), so it is the one the check pins.
func closeSyncsFile(lines []string) bool {
	for i, l := range lines {
		if !strings.HasPrefix(l, "func ") || !strings.Contains(l, ") Close(") {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(lines[j], "func ") {
				break
			}
			if strings.Contains(lines[j], ".Sync()") {
				return true
			}
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
