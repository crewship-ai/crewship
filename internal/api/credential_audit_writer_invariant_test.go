package api

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// credential_audit.workspace_id is what makes the admin audit view indexable
// (migration 20260810153104). It is nullable, because SQLite cannot ADD COLUMN
// a NOT NULL with a REFERENCES clause — so the schema itself cannot refuse a
// row that omits it.
//
// That makes the failure mode silent in the worst way: an INSERT that leaves
// workspace_id out succeeds, the row is stored, nothing logs, and the event
// simply never appears in any workspace-scoped audit read. Compliance data
// that is present but unreachable is worse than data that is missing loudly.
//
// This is not hypothetical. Two fixtures in audit_sources_test.go wrote
// exactly that INSERT, and they only surfaced because the scoped query started
// using the column. A runtime test cannot catch the general case — it can only
// cover writers that exist today — so the guard is a source scan, in the same
// style as internal/api/admin_authz_floor_test.go's build-failing invariants.
//
// Scope: NON-TEST Go sources only. A test may legitimately construct a
// pre-migration row to exercise the backfill (see
// internal/database/migrate_credential_audit_workspace_test.go, which does
// precisely that); production code never may.
func TestEveryCredentialAuditInsertNamesTheWorkspace(t *testing.T) {
	t.Parallel()

	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	// Matches the statement and enough of what follows to see the column
	// list. `(?is)` so it spans the newlines a formatted INSERT contains.
	insertRe := regexp.MustCompile(`(?is)INSERT\s+(?:OR\s+\w+\s+)?INTO\s+credential_audit\s*\(([^)]*)\)`)

	var offenders []string
	scanned := 0

	err = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case "node_modules", ".git", "web", "out", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		scanned++
		for _, m := range insertRe.FindAllStringSubmatch(string(src), -1) {
			columns := m[1]
			if strings.Contains(strings.ToLower(columns), "workspace_id") {
				continue
			}
			rel, _ := filepath.Rel(repoRoot, path)
			offenders = append(offenders, rel+":\n      columns were ("+strings.Join(strings.Fields(columns), " ")+")")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	// A scan that silently matched nothing would pass forever. Prove it ran.
	if scanned < 100 {
		t.Fatalf("only scanned %d Go files — the walk is not reaching the tree", scanned)
	}

	if len(offenders) > 0 {
		t.Errorf("%d INSERT INTO credential_audit without workspace_id:\n    %s\n\n"+
			"The column is nullable (ALTER TABLE ADD COLUMN cannot make it NOT NULL with a\n"+
			"REFERENCES clause), so omitting it does not fail — it writes a row that no\n"+
			"workspace-scoped audit read will ever return. Derive it in the statement:\n"+
			"    (SELECT workspace_id FROM credentials WHERE id = ?)\n"+
			"so it cannot disagree with the credential the row describes.",
			len(offenders), strings.Join(offenders, "\n    "))
	}
}
