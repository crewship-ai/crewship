package backup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

type restoreReferenceRecorder struct {
	refs []resourcelifecycle.ImageReference
	err  error
}

func (p *restoreReferenceRecorder) Preserve(_ context.Context, refs []resourcelifecycle.ImageReference) (map[string]string, error) {
	p.refs = append(p.refs, refs...)
	return nil, p.err
}

func TestRestorePreservesTransactionSelectionsBeforeCommitAndRollsBackOnFailure(t *testing.T) {
	source := openMigratedDBCov(t)
	ws, crew := seedCovWorkspace(t, source, "pin-before-commit")
	id := "sha256:" + strings.Repeat("a", 64)
	if _, err := source.Exec(`UPDATE crews SET cached_image=? WHERE id=?`, id, crew); err != nil {
		t.Fatal(err)
	}
	dump, err := DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		target := openMigratedDBCov(t)
		target.SetMaxOpenConns(1) // a callback rereading target DB would deadlock
		recorder := &restoreReferenceRecorder{}
		if fail {
			recorder.err = errors.New("exact environment unavailable")
		}
		gate := resourcelifecycle.Images(target)
		gate.SetReferenceProtector(recorder)
		_, err := RestoreDumpTxHooks(t.Context(), target, dump, nil)
		if fail != (err != nil) {
			t.Fatalf("restore failed=%t: %v", fail, err)
		}
		found := false
		for _, ref := range recorder.refs {
			found = found || (ref.Ref == id && ref.Required)
		}
		if !found {
			t.Fatal("restored current image was not made exact-required before commit")
		}
		var count int
		if err := target.QueryRow(`SELECT count(*) FROM crews WHERE id=?`, crew).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if (count == 0) != fail {
			t.Fatalf("failed admission left published crew: count=%d failure=%t", count, fail)
		}
	}
}

func TestRestoreDoesNotEnrollIgnoredCrewSelections(t *testing.T) {
	source := openMigratedDBCov(t)
	ws, crew := seedCovWorkspace(t, source, "pin-ignored")
	dump, err := DumpWorkspace(t.Context(), source, ws)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDBCov(t)
	if _, err := RestoreDumpTxHooks(t.Context(), target, dump, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Exec(`UPDATE crews SET cached_image='unrelated-target-selection' WHERE id=?`, crew); err != nil {
		t.Fatal(err)
	}
	recorder := &restoreReferenceRecorder{err: errors.New("must not inspect ignored rows")}
	resourcelifecycle.Images(target).SetReferenceProtector(recorder)
	if _, err := RestoreDumpTxHooks(t.Context(), target, dump, nil); err != nil {
		t.Fatal(err)
	}
	if len(recorder.refs) != 0 {
		t.Fatal("restore enrolled existing rows it did not insert")
	}
}
