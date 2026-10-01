package backup_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// A write admitted before the window that is still running when the drain
// timeout runs out: the backup is skipped as busy, nothing is copied, and
// writes are admitted again. Copying around it would give a bundle whose
// database and files disagree.
func TestInstanceBackupSkipsWhenWritesDoNotDrain(t *testing.T) {
	f := newInstanceFixture(t)
	t.Setenv(quiesce.DrainTimeoutEnv, "30ms")
	ctrl := quiesce.New()
	stuck, ok := ctrl.Enter(context.Background())
	if !ok {
		t.Fatal("admission closed with no window")
	}
	defer stuck.Leave()

	var phases []string
	_, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin"}, Recipients: []age.Recipient{f.identity.Recipient()},
		Paths: f.paths, Quiesce: ctrl, BusyWait: time.Second, Poll: 5 * time.Millisecond,
		Busy:     func(context.Context) (int, string, error) { return 0, "", nil },
		Progress: func(p string) { phases = append(phases, p) },
	})
	if !errors.Is(err, backup.ErrInstanceBusy) {
		t.Fatalf("err = %v, want ErrInstanceBusy", err)
	}
	// Begin's error is wrapped with %v, so the reason is in the message.
	if !strings.Contains(err.Error(), "writes did not drain") {
		t.Fatalf("err = %v, want it to say the writes did not drain", err)
	}
	if len(phases) != 0 {
		t.Fatalf("phases = %v, want no copy at all", phases)
	}
	if ctrl.Holding() {
		t.Fatal("an abandoned window still holds writes")
	}
	if entries, _ := os.ReadDir(f.outputDir); len(entries) != 0 {
		t.Fatalf("output dir holds %d entries after a skipped run", len(entries))
	}
	w, ok := ctrl.Enter(context.Background())
	if !ok {
		t.Fatal("writes were not admitted again after the skip")
	}
	w.Leave()
}
