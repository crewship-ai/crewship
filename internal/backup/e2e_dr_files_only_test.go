package backup_test

// The disaster-recovery flow the CLI has always printed, end to end
// (#1716).
//
// Before this, step 3 was unreachable. `restore --as-crew X` forks the
// bundle under a new workspace and a new crew slug, skips the docker
// phase (the forked crews have no containers yet), and prints "provision
// the new crews and re-run restore without the rewrite flag to land
// container state". That re-run is refused unconditionally by
// allowRestore — the forked workspace's id and slug can match neither of
// the bundle's — so the only step that could land the crews' files was
// the one step the guard forbade. What the operator was left with is a
// workspace whose rows are complete and whose crews are empty.
//
// Two things had to be true for the resume to be both possible and safe,
// and this file asserts both:
//
//  1. It has to be authorised by evidence rather than by a flag: the
//     workspace must be one this instance created by restoring THIS
//     bundle. That evidence is the backup_restore_origins row.
//  2. It has to write into the crew the fork created, NOT the crew the
//     bundle names. The manifest still carries the SOURCE crew's slug,
//     so a resume that resolved containers from the manifest would, on a
//     same-instance restore, overwrite the source crew's live data with
//     its own backup. That is a worse outcome than the bug being fixed,
//     so it gets its own assertion.

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
)

// recordingOps is a DockerOps that records which containers were written
// to and which sections landed, so the test can assert the resume
// addressed the right crew. Every container "exists"; nothing else is
// simulated, because the container-side behaviour is covered live in
// live_crew_roundtrip_test.go and what is under test here is the
// addressing.
type recordingOps struct {
	written []string // "<containerID>:<dest>"
	// memoryPermResidual, when set, is what the post-restore permission
	// sweep reports as still wrong — the shape of a .memory directory
	// the agent could not chgrp.
	memoryPermResidual string
}

func (o *recordingOps) Pause(context.Context, string) error   { return nil }
func (o *recordingOps) Unpause(context.Context, string) error { return nil }

// CopyFrom serves a one-file tar shaped the way Docker's own
// CopyFromContainer shapes one: a top-level wrapper directory named
// after the source path, then the contents. The collector strips that
// wrapper, so getting it right is what makes the capture non-empty —
// and a non-empty capture is what makes the manifest report the section,
// which is what makes the resume have anything to land.
func (o *recordingOps) CopyFrom(_ context.Context, _, srcPath string) (io.ReadCloser, error) {
	wrapper := strings.Trim(srcPath, "/")
	if i := strings.LastIndex(wrapper, "/"); i >= 0 {
		wrapper = wrapper[i+1:]
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	body := []byte("content of " + srcPath + "\n")
	_ = tw.WriteHeader(&tar.Header{
		Name: wrapper + "/", Typeflag: tar.TypeDir, Mode: 0o755, Uid: 1001, Gid: 1001,
	})
	_ = tw.WriteHeader(&tar.Header{
		Name: wrapper + "/probe.txt", Typeflag: tar.TypeReg, Mode: 0o644,
		Size: int64(len(body)), Uid: 1001, Gid: 1001,
	})
	_, _ = tw.Write(body)
	_ = tw.Close()
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
func (o *recordingOps) CopyTo(context.Context, string, string, io.Reader) error { return nil }
func (o *recordingOps) CopyToPath(_ context.Context, containerID string, spec backup.ExtractSpec, content io.Reader) error {
	_, _ = io.Copy(io.Discard, content)
	o.written = append(o.written, containerID+":"+spec.Dest)
	return nil
}
func (o *recordingOps) ContainerExists(context.Context, string) (bool, error) { return true, nil }
func (o *recordingOps) Exec(context.Context, string, []string) (int, []byte, error) {
	return 0, nil, nil
}

// ExecAs answers the preflight probes cleanly, and optionally reports a
// residual for the post-restore memory-permission sweep — which is how a
// crew whose data landed but whose .memory group could not be re-applied
// looks from the runner's side.
func (o *recordingOps) ExecAs(_ context.Context, _, _ string, cmd []string) (int, []byte, error) {
	if o.memoryPermResidual != "" && len(cmd) > 0 && strings.Contains(strings.Join(cmd, " "), "chgrp 1002") {
		return 0, []byte(o.memoryPermResidual), nil
	}
	return 0, nil, nil
}

func (o *recordingOps) containersTouched() map[string]bool {
	out := map[string]bool{}
	for _, w := range o.written {
		out[strings.SplitN(w, ":", 2)[0]] = true
	}
	return out
}

func TestE2E_DisasterRecovery_FilesOnlyResume(t *testing.T) {
	ctx := context.Background()

	// === 1. Source workspace, backed up.
	db := openMigratedDB(t)
	sourceWorkspaceID := seedWorkspace(t, db)

	// Mirrors the docker provider's crewResourceName: the container name
	// carries the crew ID as well as the slug, precisely so two crews
	// that share a slug across workspaces cannot collide. That is what
	// makes the "did the resume address the fork or the source?" question
	// answerable at all — under --as-workspace the forked crew KEEPS the
	// bundle's slug and only its id changes.
	containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }

	const passphrase = "dr-files-only-resume"
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:             backup.ScopeWorkspace,
		WorkspaceID:       sourceWorkspaceID,
		OutputDir:         t.TempDir(),
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:        passphrase,
		CrewContainerName: containerFor,
		DockerOps:         &recordingOps{},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Which crews the bundle carries, and what the SOURCE instance calls
	// their containers. If the resume writes to any of these, it has
	// overwritten the live source crew.
	sourceContainers := map[string]bool{}
	for _, c := range created.Manifest.Contents.Crews {
		sourceContainers[containerFor(c.ID, c.Slug)] = true
	}

	// === 2. Fork it into a new workspace, same instance. This is the
	// step that skips the docker phase.
	forkOps := &recordingOps{}
	fork, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path:         created.Path,
		Passphrase:   passphrase,
		AsWorkspace:  "acme-dr",
		Actor:        backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:    forkOps,
		ContainerFor: containerFor,
	})
	if err != nil {
		t.Fatalf("rewrite restore: %v", err)
	}
	if !fork.DockerPhaseSkipped {
		t.Fatalf("a --as-workspace restore must skip the docker phase; it did not")
	}
	// The fork landed no container state, and has to say so. Reporting
	// the bundle's crew count here is precisely the claim that sends an
	// operator away thinking the migration is done.
	if fork.CrewsRestored != 0 {
		t.Errorf("fork reports %d crews restored; it skipped the docker phase and restored none", fork.CrewsRestored)
	}
	if len(forkOps.written) != 0 {
		t.Fatalf("rewrite restore wrote container state it should have skipped: %v", forkOps.written)
	}
	if fork.RestoredWorkspaceID == "" || fork.RestoredWorkspaceID == sourceWorkspaceID {
		t.Fatalf("fork should have a NEW workspace id; got %q (source %q)", fork.RestoredWorkspaceID, sourceWorkspaceID)
	}

	// === 3. The provenance the resume will be authorised by.
	origin, err := backup.LookupRestoreOrigin(ctx, db, fork.RestoredWorkspaceID)
	if err != nil {
		t.Fatalf("LookupRestoreOrigin: %v", err)
	}
	if origin == nil {
		t.Fatalf("no restore origin recorded for the forked workspace — the resume can never be authorised")
	}
	if origin.BundleSHA256 != created.Manifest.Checksums.PayloadSHA256 {
		t.Fatalf("origin records bundle %q, bundle is %q", origin.BundleSHA256, created.Manifest.Checksums.PayloadSHA256)
	}
	if len(origin.CrewsByBundleSlug) != len(created.Manifest.Contents.Crews) {
		t.Fatalf("origin maps %d crews, bundle has %d — the resume cannot address them all",
			len(origin.CrewsByBundleSlug), len(created.Manifest.Contents.Crews))
	}
	// Every mapped crew must be a real row in the FORKED workspace, not
	// the source's.
	for bundleSlug, target := range origin.CrewsByBundleSlug {
		var wsID string
		if err := db.QueryRowContext(ctx, `SELECT workspace_id FROM crews WHERE id = ?`, target.ID).Scan(&wsID); err != nil {
			t.Fatalf("crew %s mapped to id %s which is not a row: %v", bundleSlug, target.ID, err)
		}
		if wsID != fork.RestoredWorkspaceID {
			t.Errorf("bundle crew %s maps to crew %s in workspace %s, want the forked workspace %s",
				bundleSlug, target.ID, wsID, fork.RestoredWorkspaceID)
		}
	}

	// === 4. The resume itself. Crews are provisioned by now (containerFor
	// resolves), so this is the step that lands the files.
	resumeOps := &recordingOps{}
	var logs []string
	resume, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path:              created.Path,
		Passphrase:        passphrase,
		FilesOnly:         true,
		ResumeWorkspaceID: fork.RestoredWorkspaceID,
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:         resumeOps,
		ContainerFor:      containerFor,
		Logger:            func(m string) { logs = append(logs, m) },
	})
	if err != nil {
		t.Fatalf("--files-only resume: %v — this is the step the documented DR flow has never been able to complete", err)
	}
	if len(resumeOps.written) == 0 {
		t.Fatalf("--files-only resume landed no container state at all; logs: %v", logs)
	}

	// The assertion this whole fix is about: the files went to the crews
	// the fork created, and NOT to the source crews the manifest names.
	touched := resumeOps.containersTouched()
	for _, target := range origin.CrewsByBundleSlug {
		want := containerFor(target.ID, target.Slug)
		if !touched[want] {
			t.Errorf("resume never wrote to %s, the container of the crew the fork created", want)
		}
	}
	for c := range touched {
		if sourceContainers[c] {
			t.Errorf("resume wrote into %s — that is the SOURCE crew's container; its live data has just been overwritten by a sibling's backup", c)
		}
	}

	// RestoredWs is what the CLI prints as `workspace=`, so it has to be
	// the slug the operator named, not the id echoed back at them.
	if resume.RestoredWs != "acme-dr" {
		t.Errorf("resume reported workspace %q, want the slug acme-dr", resume.RestoredWs)
	}
	// What landed, not what the bundle describes. The two happen to be
	// equal here; asserting the landed count is what makes a future
	// change that writes to nothing visible.
	if resume.CrewsRestored != len(created.Manifest.Contents.Crews) {
		t.Errorf("resume reports %d crews restored, want %d — it must count what landed, not what the bundle describes",
			resume.CrewsRestored, len(created.Manifest.Contents.Crews))
	}

	// The resume must not have touched the database. Re-inserting the
	// bundle's rows under their original ids, into an instance that
	// already holds remapped copies of all of them, is exactly what the
	// old "re-run restore without the rewrite flag" advice would have
	// done had the guard allowed it.
	if resume.RowsInserted != 0 {
		t.Errorf("--files-only inserted %d rows; it must change no database state", resume.RowsInserted)
	}
}

// TestE2E_DisasterRecovery_FilesOnlyRefusesUnrelatedWorkspace pins the
// other half of the contract: the flag authorises nothing on its own.
// A workspace that was not forked from this bundle cannot use it to pull
// another tenant's container state into its own crews.
func TestE2E_DisasterRecovery_FilesOnlyRefusesUnrelatedWorkspace(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	sourceWorkspaceID := seedWorkspace(t, db)

	const passphrase = "dr-files-only-refusal"
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: sourceWorkspaceID,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// A workspace with no provenance at all.
	const strangerID = "ws_stranger_no_provenance"
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		strangerID, "Stranger", "stranger-ws"); err != nil {
		t.Fatalf("seed stranger workspace: %v", err)
	}

	ops := &recordingOps{}
	_, err = backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path:              created.Path,
		Passphrase:        passphrase,
		FilesOnly:         true,
		ResumeWorkspaceID: strangerID,
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:         ops,
		ContainerFor:      func(id, slug string) string { return "crewship-team-" + slug + "-" + id },
	})
	if err == nil {
		t.Fatalf("--files-only into a workspace with no provenance was allowed")
	}
	if !strings.Contains(err.Error(), "not created by restoring this bundle") {
		t.Errorf("refusal should say why; got: %v", err)
	}
	if len(ops.written) != 0 {
		t.Errorf("refused resume still wrote container state: %v", ops.written)
	}
}

// TestE2E_DisasterRecovery_FilesOnlyRefusesWhenNothingLands is the
// no-op case, and it is the same failure mode as #1713 restated one
// layer up: an operation that reports success without having verified it
// did anything. A resume whose crews resolve to no container writes
// nothing, and would otherwise exit 0 having printed the BUNDLE's crew
// count — which an operator reads as "my crews' files are back".
//
// Authorisation is genuinely satisfied here; only the landing fails. That
// is the point: the refusal has to come from what happened, not from the
// permission check.
func TestE2E_DisasterRecovery_FilesOnlyRefusesWhenNothingLands(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	sourceWorkspaceID := seedWorkspace(t, db)
	containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }

	const passphrase = "dr-files-only-noop"
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:             backup.ScopeWorkspace,
		WorkspaceID:       sourceWorkspaceID,
		OutputDir:         t.TempDir(),
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:        passphrase,
		CrewContainerName: containerFor,
		DockerOps:         &recordingOps{},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	fork, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path:         created.Path,
		Passphrase:   passphrase,
		AsWorkspace:  "acme-dr",
		Actor:        backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:    &recordingOps{},
		ContainerFor: containerFor,
	})
	if err != nil {
		t.Fatalf("rewrite restore: %v", err)
	}

	t.Run("no container resolves for any crew", func(t *testing.T) {
		ops := &recordingOps{}
		_, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
			Path:              created.Path,
			Passphrase:        passphrase,
			FilesOnly:         true,
			ResumeWorkspaceID: fork.RestoredWorkspaceID,
			Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
			DockerOps:         ops,
			// The crews were never provisioned, so nothing resolves.
			ContainerFor: func(string, string) string { return "" },
		})
		if err == nil {
			t.Fatalf("a resume that landed nothing reported success")
		}
		if !strings.Contains(err.Error(), "landed no crew filesystem state") {
			t.Errorf("refusal should say nothing landed; got %v", err)
		}
		// `crew start` and not `crew provision`: the container has to be
		// RUNNING for exec-tar to write into it, and provision only ever
		// built an image (see restore_remediation.go).
		if !strings.Contains(err.Error(), "crewship crew start") {
			t.Errorf("refusal should name the step that fixes it; got %v", err)
		}
		if len(ops.written) != 0 {
			t.Errorf("nothing should have been written: %v", ops.written)
		}
	})

	t.Run("no container runtime configured", func(t *testing.T) {
		_, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
			Path:              created.Path,
			Passphrase:        passphrase,
			FilesOnly:         true,
			ResumeWorkspaceID: fork.RestoredWorkspaceID,
			Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
			// Landing container state is the only thing this mode does,
			// so no runtime means the call is a no-op by construction.
			DockerOps:    nil,
			ContainerFor: nil,
		})
		if err == nil {
			t.Fatalf("--files-only with no container runtime reported success")
		}
		if !strings.Contains(err.Error(), "needs a container runtime") {
			t.Errorf("refusal should say why; got %v", err)
		}
	})
}

// TestE2E_OrdinaryRestore_ReportsCrewsActuallyRestored covers the plain
// restore path's CrewsRestored, which nothing else does.
//
// The gap was real and it was mine: CrewsRestored went in to stop a
// resume claiming the bundle's crew count when it had landed nothing,
// and was then set on the resume path only — so an ordinary restore
// reported crews_count 4, crews_restored 0. The follow-up commit set it
// on the ordinary path too, and the only assertion that shipped
// alongside read `fork.CrewsRestored == 0`. Zero is the ZERO VALUE:
// that assertion passes whether the field is set or the line is deleted,
// so it could not tell the fix from its absence.
//
// A number that is only ever asserted equal to zero is not covered. This
// asserts a non-zero one, against the containers the restore actually
// wrote to, so the count has to be earned rather than defaulted.
func TestE2E_OrdinaryRestore_ReportsCrewsActuallyRestored(t *testing.T) {
	ctx := context.Background()
	containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }

	source := openMigratedDB(t)
	sourceWorkspaceID := seedWorkspace(t, source)

	const passphrase = "ordinary-restore-crews-restored"
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:             backup.ScopeWorkspace,
		WorkspaceID:       sourceWorkspaceID,
		OutputDir:         t.TempDir(),
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:        passphrase,
		CrewContainerName: containerFor,
		DockerOps:         &recordingOps{},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// How many crews the bundle can actually land, read off the manifest
	// rather than hard-coded — the point is that the reported count
	// matches reality, not that it matches a number in this file.
	wantLanded := 0
	for _, c := range created.Manifest.Contents.Crews {
		if c.HasFilesystemSections(created.Manifest.FormatVersion) {
			wantLanded++
		}
	}
	if wantLanded == 0 {
		t.Fatal("fixture bundle carries no crew filesystem sections; there would be nothing to count")
	}

	// A fresh instance: no --as-* rewrite, no --replace, so this is the
	// ordinary committed path and the docker phase runs.
	target := openMigratedDB(t)
	ops := &recordingOps{}
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:         created.Path,
		Passphrase:   passphrase,
		Actor:        backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:    ops,
		ContainerFor: containerFor,
	})
	if err != nil {
		t.Fatalf("ordinary restore: %v", err)
	}
	if res.DockerPhaseSkipped {
		t.Fatalf("an ordinary restore must run the docker phase; it was skipped")
	}
	if res.CrewsRestored != wantLanded {
		t.Errorf("CrewsRestored = %d, want %d — an ordinary restore must report what it landed, not zero",
			res.CrewsRestored, wantLanded)
	}
	// Tie the number to the containers that were genuinely written to, so
	// the field cannot drift into reporting a plausible constant.
	if got := len(ops.containersTouched()); got != res.CrewsRestored {
		t.Errorf("CrewsRestored = %d but %d container(s) were written to; the count must come from the writes",
			res.CrewsRestored, got)
	}
	if res.CrewsRestored > res.CrewsCount {
		t.Errorf("CrewsRestored (%d) exceeds CrewsCount (%d); it counts a subset of what the bundle describes",
			res.CrewsRestored, res.CrewsCount)
	}
}

// TestE2E_DryRun_ReportsNothingRestored pins the deliberate zero. A dry
// run rolls back and writes no container state, so reporting anything
// else would be the same false claim in the opposite direction.
func TestE2E_DryRun_ReportsNothingRestored(t *testing.T) {
	ctx := context.Background()
	containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }

	db := openMigratedDB(t)
	wsID := seedWorkspace(t, db)
	const passphrase = "dry-run-crews-restored"
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:             backup.ScopeWorkspace,
		WorkspaceID:       wsID,
		OutputDir:         t.TempDir(),
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:        passphrase,
		CrewContainerName: containerFor,
		DockerOps:         &recordingOps{},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	ops := &recordingOps{}
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:         created.Path,
		Passphrase:   passphrase,
		DryRun:       true,
		Actor:        backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:    ops,
		ContainerFor: containerFor,
	})
	if err != nil {
		t.Fatalf("dry-run restore: %v", err)
	}
	if res.CrewsRestored != 0 {
		t.Errorf("dry run reports %d crews restored; it writes no container state", res.CrewsRestored)
	}
	if len(ops.written) != 0 {
		t.Errorf("dry run wrote container state: %v", ops.written)
	}
}

// TestE2E_ForkedWorkspace_UnflaggedRerunDoesNotOverwriteSource is the
// overwrite the whole --files-only design exists to prevent, arriving
// through the one door nothing was watching.
//
// The operator follows the drill: `restore B --as-workspace acme-dr`,
// which writes the provenance row. Then they run `restore B -w ws_new`
// WITHOUT --files-only — the exact command the previous CLI text told
// them to run for years. With no rewrite flag and no FilesOnly,
// skipDocker is false and crewTargetFor falls through to the MANIFEST's
// crew identity, which on a same-instance restore is the SOURCE crew's
// container. RestoreCrew then extracts the bundle's /workspace and /crew
// over the source crew's live code and agent memory, replacing them with
// the older backup. The DB half is INSERT OR IGNORE, so the row counts
// look untroubled while live data is gone.
//
// Why the existing sibling-overwrite test did not catch it: that test
// asserts exactly the right thing — "the resume must not write into the
// SOURCE crew's container" — but only ever calls RestoreBackup with
// FilesOnly: true. Every path it exercises is the mode that was built.
// The dangerous call is the mode the operator was previously TOLD to
// use, and no test pointed at it. Testing the feature you added is not
// the same as testing the command your users already have in their
// runbooks.
func TestE2E_ForkedWorkspace_UnflaggedRerunDoesNotOverwriteSource(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	sourceWorkspaceID := seedWorkspace(t, db)
	containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }

	const passphrase = "unflagged-rerun-guard"
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope:             backup.ScopeWorkspace,
		WorkspaceID:       sourceWorkspaceID,
		OutputDir:         t.TempDir(),
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:        passphrase,
		CrewContainerName: containerFor,
		DockerOps:         &recordingOps{},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	sourceContainers := map[string]bool{}
	for _, c := range created.Manifest.Contents.Crews {
		sourceContainers[containerFor(c.ID, c.Slug)] = true
	}

	fork, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path:         created.Path,
		Passphrase:   passphrase,
		AsWorkspace:  "acme-dr",
		Actor:        backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:    &recordingOps{},
		ContainerFor: containerFor,
	})
	if err != nil {
		t.Fatalf("rewrite restore: %v", err)
	}

	// The dangerous call: same bundle, the forked workspace in context,
	// no --files-only. This is what the old guidance printed.
	ops := &recordingOps{}
	_, err = backup.RestoreBackup(ctx, db, backup.RestoreOptions{
		Path:              created.Path,
		Passphrase:        passphrase,
		ResumeWorkspaceID: fork.RestoredWorkspaceID,
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:         ops,
		ContainerFor:      containerFor,
	})

	// The assertion that matters most is the second one: even if a future
	// change decides to allow this call, it must never reach the source
	// crew's container.
	for _, w := range ops.written {
		container := strings.SplitN(w, ":", 2)[0]
		if sourceContainers[container] {
			t.Errorf("an un-flagged re-run wrote into %s — the SOURCE crew's live container. Its workspace and agent memory have just been replaced by an older backup", container)
		}
	}
	if err == nil {
		t.Fatalf("an un-flagged re-run against a forked workspace was allowed; it must point the operator at --files-only")
	}
	if !strings.Contains(err.Error(), "--files-only") {
		t.Errorf("refusal must name the mode that is safe here; got %v", err)
	}
	if len(ops.written) != 0 {
		t.Errorf("refused re-run still wrote container state: %v", ops.written)
	}
}

// TestE2E_DegradedMemoryPermsDoNotFailARestoreThatLanded is where the
// decision about ErrMemoryPermsDegraded actually lives.
//
// reapplyMemoryPerms runs after every section is written, so a
// permission shortfall there arrives when the data is already on disk.
// Failing the restore at that point does not undo anything on the
// container — it only rolls back the DB transaction, leaving rows that
// describe less than the filesystem holds, and re-running cannot help
// because the entry that blocked the chgrp blocks it again.
//
// RestoreCrew wraps the sentinel and hands the decision up; this is the
// test of the decision. It is deliberately at the RUNNER level, because
// a test that called RestoreCrew directly would assert the wrapping and
// not the choice — which is exactly how the first version of this let a
// mutation of the branch survive.
func TestE2E_DegradedMemoryPermsDoNotFailARestoreThatLanded(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	sourceWorkspaceID := seedWorkspace(t, source)
	containerFor := func(id, slug string) string { return "crewship-team-" + slug + "-" + id }

	const passphrase = "degraded-perms-not-fatal"
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:             backup.ScopeWorkspace,
		WorkspaceID:       sourceWorkspaceID,
		OutputDir:         t.TempDir(),
		Actor:             backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase:        passphrase,
		CrewContainerName: containerFor,
		DockerOps:         &recordingOps{},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	ops := &recordingOps{
		// A .memory directory the sweep still reports as wrong after
		// every tolerant pass has run.
		memoryPermResidual: "/crew/agents/robin/.memory",
	}
	var logs []string
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:         created.Path,
		Passphrase:   passphrase,
		Actor:        backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		DockerOps:    ops,
		ContainerFor: containerFor,
		Logger:       func(m string) { logs = append(logs, m) },
	})
	if err != nil {
		t.Fatalf("a restore whose data landed was failed over permissions: %v", err)
	}
	if res.RowsInserted == 0 {
		t.Error("the DB transaction was rolled back, so the rows now describe less than the container holds")
	}
	if res.CrewsRestored == 0 {
		t.Error("crews whose data landed must be counted, degraded permissions or not")
	}
	if len(ops.written) == 0 {
		t.Fatal("nothing was written; the fixture is not exercising the case")
	}

	// Not fatal is not the same as not reported. The operator has to be
	// told, and told which directory.
	var told bool
	for _, l := range logs {
		if strings.Contains(l, "memory tree permissions") && strings.Contains(l, "robin") {
			told = true
		}
	}
	if !told {
		t.Errorf("degraded permissions were swallowed; the operator is never told which directory the sidecar cannot write. logs: %v", logs)
	}
}
