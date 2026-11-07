package storageheadroom

import (
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/diskusage"
)

func withUsage(t *testing.T, fn usageFunc) {
	t.Helper()
	previous := usage.Load()
	usage.Store(&fn)
	t.Cleanup(func() { usage.Store(previous) })
}

// The probe keeps reserve + one DB copy + twice (reserved + write). One byte
// short of that is refused; exactly enough is admitted; a failed statfs is a
// refusal, never an admission.
func TestCheckRefusesBelowReserveThroughInjectedProbe(t *testing.T) {
	db, err := database.Open("file:" + filepath.Join(t.TempDir(), "headroom.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE artifact_storage_global SET min_free_bytes=1000 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	var pages, pageSize, reserved int64
	if err = db.QueryRow(`SELECT (SELECT page_count FROM pragma_page_count),(SELECT page_size FROM pragma_page_size),reserved_bytes FROM artifact_storage_global`).Scan(&pages, &pageSize, &reserved); err != nil {
		t.Fatal(err)
	}
	const write = 4096
	required := uint64(1000 + pages*pageSize + 2*(reserved+write))
	for _, tc := range []struct {
		name string
		free uint64
		err  error
		want bool
	}{
		{"exactly enough", required, nil, false},
		{"one byte short", required - 1, nil, true},
		{"probe failed", 1 << 62, errors.New("statfs failed"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withUsage(t, func(string) (diskusage.Stats, error) { return diskusage.Stats{FreeBytes: tc.free}, tc.err })
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			got := Check(t.Context(), tx, write)
			if errors.Is(got, ErrInsufficient) != tc.want || (!tc.want && got != nil) {
				t.Fatalf("Check = %v, want refusal %v", got, tc.want)
			}
		})
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = Check(t.Context(), tx, -1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("negative estimate: %v", err)
	}
}
