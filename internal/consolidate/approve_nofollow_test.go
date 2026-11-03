package consolidate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// The .proposed directory lives inside the crew's memory tree, which the
// crew's container can write. Approve must therefore read the staged
// proposal the same way every other door into that tree does: a regular
// file, never a symlink. These tests pin that for ApproveProposal; the
// HTTP-level twins live in internal/api/proposal_nofollow_test.go.

const nofollowMarker = "outside-content-must-not-be-merged"

// writeOutsideProposal writes a well-formed proposal body to a file that
// is NOT inside the proposal's output directory and returns its path.
func writeOutsideProposal(t *testing.T) string {
	t.Helper()
	outside := filepath.Join(t.TempDir(), "outside.md")
	body := "# Proposed learned rules\n\n---\n\n- **Pattern:** " + nofollowMarker +
		"\n  **Action:** none\n  **Confidence:** 0.9\n  **Evidence:** e1\n"
	if err := os.WriteFile(outside, []byte(body), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	return outside
}

func TestApproveProposal_RefusesSymlinkedProposalFile(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, outputDir := seedPendingProposal(t, db, w)
	var proposalPath string
	if err := db.QueryRow(`SELECT proposal_path FROM memory_proposals WHERE id = ?`, proposalID).Scan(&proposalPath); err != nil {
		t.Fatalf("read proposal path: %v", err)
	}
	outside := writeOutsideProposal(t)
	if err := os.Remove(proposalPath); err != nil {
		t.Fatalf("remove staged proposal: %v", err)
	}
	if err := os.Symlink(outside, proposalPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	_, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", ApprovalOptions{})
	if err == nil {
		t.Fatal("ApproveProposal accepted a symlinked proposal file; want refusal")
	}
	if strings.Contains(err.Error(), nofollowMarker) {
		t.Fatalf("error leaks the symlink target's content: %v", err)
	}

	canonical := filepath.Join(outputDir, "learned-"+time.Now().UTC().Format("2006-01-02")+".md")
	if got, rerr := os.ReadFile(canonical); rerr == nil && strings.Contains(string(got), nofollowMarker) {
		t.Fatalf("symlink target content was merged into %s:\n%s", canonical, got)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q after refused approve, want pending", status)
	}
}

func TestApproveProposal_RegularProposalFileStillMerges(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)
	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1", ApprovalOptions{})
	if err != nil {
		t.Fatalf("ApproveProposal on a regular file: %v", err)
	}
	got, err := os.ReadFile(appr.CanonicalPath)
	if err != nil {
		t.Fatalf("read canonical: %v", err)
	}
	if !strings.Contains(string(got), "approve me") {
		t.Errorf("canonical missing merged rule: %q", got)
	}
}

// With StorageRoot set, a .proposed directory replaced by a symlink is
// refused as well; a regular proposal under the same root still merges.
func TestApproveProposal_StorageRoot(t *testing.T) {
	cases := []struct {
		name        string
		symlinkDir  bool
		wantRefusal bool
	}{
		{name: "regular_file_merges", symlinkDir: false, wantRefusal: false},
		{name: "symlinked_proposed_dir_refused", symlinkDir: true, wantRefusal: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openDB(t)
			defer db.Close()
			w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
			defer w.Close()
			applyV89Schema(t, db)

			proposalID, outputDir := seedPendingProposal(t, db, w)
			var proposalPath string
			if err := db.QueryRow(`SELECT proposal_path FROM memory_proposals WHERE id = ?`, proposalID).Scan(&proposalPath); err != nil {
				t.Fatalf("read proposal path: %v", err)
			}
			if tc.symlinkDir {
				outsideDir := t.TempDir()
				data, err := os.ReadFile(writeOutsideProposal(t))
				if err != nil {
					t.Fatalf("read outside: %v", err)
				}
				if err := os.WriteFile(filepath.Join(outsideDir, filepath.Base(proposalPath)), data, 0o644); err != nil {
					t.Fatalf("write outside proposal: %v", err)
				}
				proposedDir := filepath.Dir(proposalPath)
				if err := os.RemoveAll(proposedDir); err != nil {
					t.Fatalf("remove proposed dir: %v", err)
				}
				if err := os.Symlink(outsideDir, proposedDir); err != nil {
					t.Fatalf("plant dir symlink: %v", err)
				}
			}

			appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1",
				ApprovalOptions{StorageRoot: filepath.Dir(outputDir)})
			if tc.wantRefusal {
				if err == nil {
					t.Fatal("ApproveProposal followed a symlinked .proposed directory; want refusal")
				}
				canonical := filepath.Join(outputDir, "learned-"+time.Now().UTC().Format("2006-01-02")+".md")
				if got, rerr := os.ReadFile(canonical); rerr == nil && strings.Contains(string(got), nofollowMarker) {
					t.Fatalf("symlink target content was merged:\n%s", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApproveProposal: %v", err)
			}
			got, err := os.ReadFile(appr.CanonicalPath)
			if err != nil || !strings.Contains(string(got), "approve me") {
				t.Fatalf("canonical missing merged rule (err=%v): %q", err, got)
			}
		})
	}
}
