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
func (o *recordingOps) ExecAs(context.Context, string, string, []string) (int, []byte, error) {
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
		if !strings.Contains(err.Error(), "crew provision") {
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
