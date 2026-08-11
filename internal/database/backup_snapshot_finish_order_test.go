package database

import (
	"errors"
	"strings"
	"testing"
)

// fakeBackup stands in for *sqlite.Backup so the copy's call ORDER can be
// asserted. The real object cannot be used for this: the incomplete-copy
// branch is only reachable when SQLite returns SQLITE_OK from a Step(-1)
// that was asked for every remaining page, which no in-process fixture can
// force. What the fake models is the one property of the real object that
// makes the order matter — Finish destroys it.
//
// modernc.org/sqlite's Backup.Finish calls sqlite3_backup_finish, which ends
// in sqlite3_free(p) (lib/sqlite_g_*.go, "the sqlite3_backup object ... is
// destroyed by a call to sqlite3_backup_finish"), while Remaining and
// PageCount dereference that same pointer with no validity check. So every
// use after Finish reads freed memory. The fake records those uses instead of
// returning junk, because a test cannot assert on undefined behaviour.
type fakeBackup struct {
	stepMore  bool
	stepErr   error
	finishErr error

	remaining int
	pageCount int

	stepCalls   int
	finishCalls int
	// usedAfterFinish names the methods called once the object was destroyed.
	usedAfterFinish []string
}

func (f *fakeBackup) Step(n int32) (bool, error) {
	f.stepCalls++
	if n != -1 {
		panic("copyAllPages must ask for every remaining page in one Step(-1)")
	}
	return f.stepMore, f.stepErr
}

func (f *fakeBackup) Finish() error {
	f.finishCalls++
	return f.finishErr
}

func (f *fakeBackup) Remaining() int {
	f.noteUse("Remaining")
	return f.remaining
}

func (f *fakeBackup) PageCount() int {
	f.noteUse("PageCount")
	return f.pageCount
}

func (f *fakeBackup) noteUse(method string) {
	if f.finishCalls > 0 {
		f.usedAfterFinish = append(f.usedAfterFinish, method)
	}
}

// TestCopyAllPages_NeverUsesBackupAfterFinish pins the handle's lifetime
// against every outcome of the copy. The incomplete-snapshot branch is the
// one that got this wrong: it built its message from Remaining/PageCount
// after Finish had already freed the object, so the operator would have been
// handed garbage page counts at best, and a segfault in the middle of the
// pre-migration snapshot at worst — a boot killed by a crash from the code
// that exists to make boot recoverable.
//
// The table also holds the surrounding contract in place: Finish runs exactly
// once on every path (it closes the destination handle), and the step error
// wins over the finish error when both fire.
func TestCopyAllPages_NeverUsesBackupAfterFinish(t *testing.T) {
	stepBoom := errors.New("disk I/O error")
	finishBoom := errors.New("finish failed")

	tests := []struct {
		name    string
		backup  fakeBackup
		wantErr string // substring; empty means the copy must succeed
	}{
		{
			name:   "complete copy",
			backup: fakeBackup{stepMore: false},
		},
		{
			// SQLITE_OK from a Step(-1): pages remain although all of them
			// were requested, so the destination is partial.
			name:    "incomplete copy reports both page counts",
			backup:  fakeBackup{stepMore: true, remaining: 7, pageCount: 42},
			wantErr: "snapshot incomplete: 7 of 42 pages left",
		},
		{
			name:    "step error",
			backup:  fakeBackup{stepErr: stepBoom},
			wantErr: "copy pages into snapshot",
		},
		{
			name:    "finish error",
			backup:  fakeBackup{finishErr: finishBoom},
			wantErr: "finalize snapshot",
		},
		{
			// Both failed: the step error is the closer cause.
			name:    "step error wins over finish error",
			backup:  fakeBackup{stepErr: stepBoom, finishErr: finishBoom},
			wantErr: "copy pages into snapshot",
		},
		{
			// Pages remain AND finish failed: still no read of a freed
			// object, whichever branch the switch takes.
			name:    "incomplete copy with finish error",
			backup:  fakeBackup{stepMore: true, remaining: 1, pageCount: 2, finishErr: finishBoom},
			wantErr: "finalize snapshot",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := tc.backup
			err := copyAllPages(&fake)

			if len(fake.usedAfterFinish) > 0 {
				t.Errorf("copyAllPages used the backup object after Finish freed it: %v "+
					"— capture Remaining/PageCount before calling Finish",
					strings.Join(fake.usedAfterFinish, ", "))
			}
			if fake.finishCalls != 1 {
				t.Errorf("Finish called %d times, want exactly 1 (it closes the destination handle)", fake.finishCalls)
			}
			if fake.stepCalls != 1 {
				t.Errorf("Step called %d times, want exactly 1", fake.stepCalls)
			}

			switch {
			case tc.wantErr == "":
				if err != nil {
					t.Fatalf("copyAllPages = %v, want nil", err)
				}
			case err == nil:
				t.Fatalf("copyAllPages = nil, want an error containing %q", tc.wantErr)
			case !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("copyAllPages = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
