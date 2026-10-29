package api

import (
	"net/http"
	"testing"
)

// The loosening check must compare against the effective policy as it stands
// on the writer transaction. A concurrent OWNER move that seals a folder
// between the request's first reads and its transaction must not let a
// MANAGER's move-to-root undo that seal against a stale, unsealed baseline.
// Not parallel: it sets a package-level interleaving hook.
func TestCredentialFolders_LooseningCheckUsesInTransactionPolicy(t *testing.T) {
	tests := []struct {
		name string
		role string
		want int
	}{
		{"manager cannot undo a seal applied concurrently", "MANAGER", http.StatusForbidden},
		{"admin may", "ADMIN", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newCredFolderRig(t)
			code, sealed := g.create(t, "OWNER", `{"name":"Sealed","reveal_policy":"nobody"}`)
			if code != http.StatusCreated {
				t.Fatalf("sealed parent: %d", code)
			}
			moved := g.mustRoot(t, "Moved")
			folderUpdateBeforeTx = func() {
				if _, err := g.cred.db.Exec(`UPDATE credential_folders SET parent_id = ? WHERE id = ?`, sealed.ID, moved); err != nil {
					t.Error(err)
				}
			}
			t.Cleanup(func() { folderUpdateBeforeTx = nil })
			if got := g.patchFolder(t, tc.role, moved, `{"parent_id":null}`); got != tc.want {
				t.Fatalf("%s move to root after concurrent seal = %d, want %d", tc.role, got, tc.want)
			}
		})
	}
}
