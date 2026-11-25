//go:build linux

package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestForceRecoveryChecksDatabaseAfterReportInsert(t *testing.T) {
	const changeReference = `CREATE TRIGGER change_fixture_after_report AFTER INSERT ON restore_reports
		BEGIN UPDATE memory_versions SET payload_ref='obsolete-fixture-reference' WHERE id='mv1'; END`
	const changeEnvelope = `CREATE TRIGGER change_fixture_after_report AFTER INSERT ON restore_reports
		BEGIN UPDATE agents SET webhook_secret='v1:invalid-fixture' WHERE id='a_lab'; END`
	for _, tc := range []struct {
		name, trigger, want string
		kit                 bool
	}{
		{"memory reference", changeReference, "unverified memory version reference", true},
		{"kit envelope", changeEnvelope, "cannot open agents.webhook_secret", true},
		{"supplied migration key", changeEnvelope, "cannot open agents.webhook_secret", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			if !tc.kit {
				// v140 seals the fixture using the supplied original key. The
				// final check must retain that key outside the migration scope.
				if _, err := f.db.Exec(`DELETE FROM _migrations WHERE version=140`); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(`UPDATE agents SET webhook_secret='sample webhook' WHERE id='a_lab'`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.db.Exec(tc.trigger); err != nil {
				t.Fatal(err)
			}
			bundle := f.create(t, tc.kit)
			target := t.TempDir()
			if err := os.WriteFile(filepath.Join(target, "operator.txt"), []byte("original generation"), 0600); err != nil {
				t.Fatal(err)
			}
			before := forceGenerationSnapshot(t, target)
			rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{
				BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: target, Force: true,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("force recovery error = %v, want %q", err, tc.want)
			}
			if rep == nil || rep.Result != backup.RestoreResultFailed || rep.PreviousDataDir != "" || rep.ReportID != "" {
				t.Fatalf("incorrect prepublication failure: %+v", rep)
			}
			assertOriginalGeneration(t, target, before)
		})
	}
}
