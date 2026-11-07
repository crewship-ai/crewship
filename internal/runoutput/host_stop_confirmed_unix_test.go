//go:build unix

package runoutput

import (
	"os"
	"path/filepath"
	"testing"
)

// stopConfirmedDir reuses the guarded host fixture (same UID rules and
// required lane) and returns its protected run directory.
func stopConfirmedDir(t *testing.T) string {
	t.Helper()
	dir := hostFixtureInput(t).Directory
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHostStopConfirmedIsBoundToOneExecution(t *testing.T) {
	dir := stopConfirmedDir(t)
	if ok, err := HostStopConfirmed(dir, "exec-a"); err != nil || ok {
		t.Fatalf("missing record confirmed=%v %v", ok, err)
	}
	if err := RecordHostStopConfirmed(dir, "exec-a"); err != nil {
		t.Fatal(err)
	}
	// Recording the same execution again is idempotent.
	if err := RecordHostStopConfirmed(dir, "exec-a"); err != nil {
		t.Fatal(err)
	}
	if ok, err := HostStopConfirmed(dir, "exec-a"); err != nil || !ok {
		t.Fatalf("own record confirmed=%v %v", ok, err)
	}
	if ok, err := HostStopConfirmed(dir, "exec-b"); err != nil || ok {
		t.Fatalf("foreign execution confirmed=%v %v", ok, err)
	}
	if ok, err := HostStopConfirmed(dir, ""); err != nil || ok {
		t.Fatalf("empty execution confirmed=%v %v", ok, err)
	}
	if err := RecordHostStopConfirmed(dir, "exec-b"); err == nil {
		t.Fatal("record for another execution replaced the existing one")
	}
	info, err := os.Stat(filepath.Join(dir, hostStopConfirmedFile))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record mode=%v %v", info, err)
	}
}

func TestHostStopConfirmedRejectsMalformedRecordsAndIdentities(t *testing.T) {
	dir := stopConfirmedDir(t)
	for _, id := range []string{"", "a b", "a\nb"} {
		if err := RecordHostStopConfirmed(dir, id); err == nil {
			t.Fatalf("invalid execution identity %q recorded", id)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, hostStopConfirmedFile), []byte("stopped exec-a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if ok, err := HostStopConfirmed(dir, "exec-a"); err == nil || ok {
		t.Fatalf("malformed record confirmed=%v %v", ok, err)
	}
	if ok, err := HostStopConfirmed(filepath.Join(dir, "missing"), "exec-a"); err == nil || ok {
		t.Fatalf("unvalidated directory confirmed=%v %v", ok, err)
	}
}
