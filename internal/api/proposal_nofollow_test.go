package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Staged proposals (skills and memory consolidation) live in the crew's
// .proposed directory, inside the memory tree the crew's container can
// write. The List handler already reads those entries without following
// symlinks; approve and diff must hold the same line. Each case plants a
// link to a file outside the tree and checks that the request is refused
// and that nothing of the target reaches the response or the catalog.

const proposalNoFollowMarker = "outside-content-must-not-be-read"

func outsideSkillFile(t *testing.T, dir string) string {
	t.Helper()
	body := "---\n" +
		"name: " + proposalNoFollowMarker + "\n" +
		"description: Use when the user asks about staged skill testing (auto-promoted memory rule)\n" +
		"category: CUSTOM\n" +
		"runtime: INSTRUCTIONS\n" +
		"maturity: EXPERIMENTAL\n" +
		"---\n\n" +
		"# " + proposalNoFollowMarker + "\n"
	p := filepath.Join(dir, "skill-planted.md")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write outside skill: %v", err)
	}
	return p
}

func TestSkillProposed_Approve_RefusesSymlinkedStagedEntry(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, proposedDir, outsideDir string)
	}{
		{
			name: "final_component_symlink",
			plant: func(t *testing.T, proposedDir, outsideDir string) {
				if err := os.MkdirAll(proposedDir, 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				target := outsideSkillFile(t, outsideDir)
				if err := os.Symlink(target, filepath.Join(proposedDir, "skill-planted.md")); err != nil {
					t.Fatalf("plant symlink: %v", err)
				}
			},
		},
		{
			name: "proposed_directory_symlink",
			plant: func(t *testing.T, proposedDir, outsideDir string) {
				if err := os.MkdirAll(filepath.Dir(proposedDir), 0o755); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				outsideSkillFile(t, outsideDir)
				if err := os.Symlink(outsideDir, proposedDir); err != nil {
					t.Fatalf("plant dir symlink: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID, crewID, slug := newSkillProposedHandlerTest(t, "nofollow-"+strings.ReplaceAll(tc.name, "_", "-"))
			root := t.TempDir()
			outsideDir := t.TempDir()
			tc.plant(t, hostProposedDir(t, root, crewID, slug), outsideDir)
			h.SetStorageBasePath(root)

			bodyBytes, _ := json.Marshal(approveBody{CrewID: crewID, FileName: "skill-planted.md"})
			req := httptest.NewRequest("POST", "/api/v1/skills/proposed/approve", bytes.NewReader(bodyBytes))
			req = withWorkspaceUser(req, userID, wsID, "MANAGER")
			rr := httptest.NewRecorder()
			h.Approve(rr, req)

			if rr.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422; body=%s", rr.Code, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), proposalNoFollowMarker) {
				t.Fatalf("response leaks the link target: %s", rr.Body.String())
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM skills WHERE slug = ?`, proposalNoFollowMarker).Scan(&n); err != nil {
				t.Fatalf("count skills: %v", err)
			}
			if n != 0 {
				t.Fatalf("a symlinked staged entry was imported (%d skill rows)", n)
			}
			if _, err := os.Stat(filepath.Join(outsideDir, "skill-planted.md")); err != nil {
				t.Fatalf("link target must be left untouched: %v", err)
			}
		})
	}
}

// writeOutsideProposalBody writes a well-formed proposal markdown outside
// any crew tree and returns its path.
func writeOutsideProposalBody(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "outside.md")
	body := "# Proposed learned rules\n\n---\n\n- **Pattern:** " + proposalNoFollowMarker +
		"\n  **Action:** none\n  **Confidence:** 0.9\n  **Evidence:** e1\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write outside proposal: %v", err)
	}
	return p
}

func TestProposed_Diff_RefusesSymlinkedFiles(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, proposalPath string)
	}{
		{
			name: "proposal_markdown_symlink",
			plant: func(t *testing.T, proposalPath string) {
				outside := writeOutsideProposalBody(t)
				if err := os.Remove(proposalPath); err != nil {
					t.Fatalf("remove proposal: %v", err)
				}
				if err := os.Symlink(outside, proposalPath); err != nil {
					t.Fatalf("plant symlink: %v", err)
				}
			},
		},
		{
			name: "canonical_learned_symlink",
			plant: func(t *testing.T, proposalPath string) {
				outside := filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(outside, []byte(proposalNoFollowMarker+"\n"), 0o644); err != nil {
					t.Fatalf("write outside: %v", err)
				}
				canonical := filepath.Join(filepath.Dir(filepath.Dir(proposalPath)),
					"learned-"+time.Now().UTC().Format("2006-01-02")+".md")
				if err := os.Symlink(outside, canonical); err != nil {
					t.Fatalf("plant symlink: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID, crewID := newProposedHandlerTest(t)
			proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")
			tc.plant(t, proposalPath)

			req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+proposalID+"/diff", nil)
			req.SetPathValue("id", proposalID)
			req = withWorkspaceUser(req, userID, wsID, "MEMBER")
			rr := httptest.NewRecorder()
			h.Diff(rr, req)

			if rr.Code == http.StatusOK {
				t.Fatalf("diff followed a symlink; want refusal. body=%s", rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), proposalNoFollowMarker) {
				t.Fatalf("response leaks the link target: %s", rr.Body.String())
			}
		})
	}
}

func TestProposed_Approve_RefusesSymlinkedProposal(t *testing.T) {
	h, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")
	outside := writeOutsideProposalBody(t)
	if err := os.Remove(proposalPath); err != nil {
		t.Fatalf("remove proposal: %v", err)
	}
	if err := os.Symlink(outside, proposalPath); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	req := httptest.NewRequest("POST", "/api/v1/consolidate/proposed/"+proposalID+"/approve", nil)
	req.SetPathValue("id", proposalID)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Approve(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("approve followed a symlinked proposal; want refusal. body=%s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), proposalNoFollowMarker) {
		t.Fatalf("response leaks the link target: %s", rr.Body.String())
	}
	canonical := filepath.Join(filepath.Dir(filepath.Dir(proposalPath)),
		"learned-"+time.Now().UTC().Format("2006-01-02")+".md")
	if got, err := os.ReadFile(canonical); err == nil && strings.Contains(string(got), proposalNoFollowMarker) {
		t.Fatalf("link target merged into canonical learned file:\n%s", got)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "pending" {
		t.Errorf("status = %q after refused approve, want pending", status)
	}
}

// With the storage root wired (as the router does), a symlinked parent
// directory is refused too, not only a symlinked final component. A
// regular proposal under the same root still diffs and approves.
func TestProposed_RootedReads_RefuseSymlinkedProposedDir(t *testing.T) {
	cases := []struct {
		name string
		call func(h *ProposedHandler, w http.ResponseWriter, r *http.Request)
		verb string
		op   string
		role string
	}{
		{name: "diff", call: (*ProposedHandler).Diff, verb: "GET", op: "diff", role: "MEMBER"},
		{name: "approve", call: (*ProposedHandler).Approve, verb: "POST", op: "approve", role: "OWNER"},
	}
	for _, tc := range cases {
		t.Run(tc.name+"_symlinked_dir_refused", func(t *testing.T) {
			h, db, userID, wsID, crewID := newProposedHandlerTest(t)
			proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")
			root := filepath.Dir(filepath.Dir(proposalPath))
			h.SetStorageBasePath(root)

			outsideDir := t.TempDir()
			outside := writeOutsideProposalBody(t)
			data, err := os.ReadFile(outside)
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

			req := httptest.NewRequest(tc.verb, "/api/v1/consolidate/proposed/"+proposalID+"/"+tc.op, nil)
			req.SetPathValue("id", proposalID)
			req = withWorkspaceUser(req, userID, wsID, tc.role)
			rr := httptest.NewRecorder()
			tc.call(h, rr, req)

			if rr.Code == http.StatusOK {
				t.Fatalf("%s followed a symlinked .proposed directory; body=%s", tc.op, rr.Body.String())
			}
			if strings.Contains(rr.Body.String(), proposalNoFollowMarker) {
				t.Fatalf("response leaks the link target: %s", rr.Body.String())
			}
		})
		t.Run(tc.name+"_regular_file_ok", func(t *testing.T) {
			h, db, userID, wsID, crewID := newProposedHandlerTest(t)
			proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")
			h.SetStorageBasePath(filepath.Dir(filepath.Dir(proposalPath)))

			req := httptest.NewRequest(tc.verb, "/api/v1/consolidate/proposed/"+proposalID+"/"+tc.op, nil)
			req.SetPathValue("id", proposalID)
			req = withWorkspaceUser(req, userID, wsID, tc.role)
			rr := httptest.NewRecorder()
			tc.call(h, rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("%s on a regular proposal: status = %d, body=%s", tc.op, rr.Code, rr.Body.String())
			}
		})
	}
}
