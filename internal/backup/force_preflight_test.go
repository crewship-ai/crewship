package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestForcePreflightRefusesDrillAndUnsafeDatabasePath(t *testing.T) {
	for _, tc := range []struct {
		target string
		drill  bool
		want   string
	}{
		{"data", true, "drill"},
		{"data?options", false, "SQLite URI delimiters"},
		{"data#fragment", false, "SQLite URI delimiters"},
		{"data%2Fother", false, "SQLite URI delimiters"},
		{"parent%2Fother/data", false, "SQLite URI delimiters"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			parent := t.TempDir()
			_, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{
				BundlePath: filepath.Join(parent, "absent.tar.zst"), Passphrase: "test",
				DataDir: filepath.Join(parent, tc.target), Force: true, Drill: tc.drill,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q preflight refusal, got %v", tc.want, err)
			}
			if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
				t.Fatalf("after refusal: entries %v, error %v", entries, err)
			}
		})
	}
}
