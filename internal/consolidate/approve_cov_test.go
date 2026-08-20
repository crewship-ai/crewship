package consolidate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// --- pure helpers ---------------------------------------------------------------

func TestCanonicalPathForProposal(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		proposal string
		want     string
	}{
		{
			name:     "inside .proposed resolves one level up",
			proposal: "/mem/topics/.proposed/proposal-abc.md",
			want:     "/mem/topics/learned-2026-06-12.md",
		},
		{
			name:     "outside .proposed stays in same dir",
			proposal: "/mem/topics/proposal-abc.md",
			want:     "/mem/topics/learned-2026-06-12.md",
		},
		{
			name:     "nested .proposed only strips the immediate parent",
			proposal: "/a/.proposed/sub/p.md",
			want:     "/a/.proposed/sub/learned-2026-06-12.md",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanonicalPathForProposal(tc.proposal, now); got != tc.want {
				t.Errorf("CanonicalPathForProposal(%q) = %q, want %q", tc.proposal, got, tc.want)
			}
		})
	}
}

func TestExtractProposalRulesBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "splits on first --- divider",
			in:   "# Proposal header\nmeta: stuff\n\n---\n\n- **Pattern:** p\n",
			want: "- **Pattern:** p",
		},
		{
			name: "no divider returns trimmed full body",
			in:   "  just rules, no scaffolding  \n",
			want: "just rules, no scaffolding",
		},
		{
			name: "keeps content after FIRST divider only",
			in:   "head\n---\nrules\n---\nmore",
			want: "rules\n---\nmore",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractProposalRulesBody(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// --- hasScoreJSONColumn ----------------------------------------------------------

func TestHasScoreJSONColumn_ProbeFailureAssumesModern(t *testing.T) {
	db := openDB(t)
	db.Close() // probe query fails → assume the modern schema
	if !hasScoreJSONColumn(context.Background(), db) {
		t.Error("probe failure must default to true (modern schema)")
	}
	// And the answer is memoised: second call hits the cache.
	if !hasScoreJSONColumn(context.Background(), db) {
		t.Error("cached answer flipped")
	}
}

// --- markProposalDecided -----------------------------------------------------------

func TestMarkProposalDecided_RaceLostReturnsNotPending(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	applyV89Schema(t, db)
	if _, err := db.Exec(
		`INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status)
		 VALUES ('mp_done', 'ws_test', 'crew_test', '/tmp/p.md', 'approved')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := markProposalDecided(context.Background(), db, "mp_done", "rejected", "u1", time.Now())
	if !errors.Is(err, ErrProposalNotPending) {
		t.Errorf("non-pending row: err = %v, want ErrProposalNotPending", err)
	}
	// Row must be untouched.
	var status string
	if qerr := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = 'mp_done'`).Scan(&status); qerr != nil {
		t.Fatalf("re-read: %v", qerr)
	}
	if status != "approved" {
		t.Errorf("status mutated to %q", status)
	}
}

func TestMarkProposalDecided_ExecError(t *testing.T) {
	db := openDB(t) // no memory_proposals table
	defer db.Close()
	err := markProposalDecided(context.Background(), db, "mp_x", "approved", "u1", time.Now())
	if err == nil || errors.Is(err, ErrProposalNotPending) {
		t.Errorf("expected raw exec error, got %v", err)
	}
}

// --- loadProposalForDecision ---------------------------------------------------------

func TestLoadProposalForDecision_QueryError(t *testing.T) {
	db := openDB(t) // table missing → non-ErrNoRows query error
	defer db.Close()
	_, err := loadProposalForDecision(context.Background(), db, "mp_x")
	if err == nil || errors.Is(err, ErrProposalNotFound) {
		t.Errorf("expected wrapped query error, got %v", err)
	}
	if !strings.Contains(err.Error(), "query proposal") {
		t.Errorf("error not wrapped with context: %v", err)
	}
}

// --- ExplainProposal ------------------------------------------------------------------

func TestExplainProposal_DecidedFieldsPopulatedAfterApprove(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)

	// Pre-decision: pointers nil.
	pre, err := ExplainProposal(context.Background(), db, proposalID)
	if err != nil {
		t.Fatalf("explain pre: %v", err)
	}
	if pre.Status != "pending" || pre.DecidedAt != nil || pre.DecidedBy != nil {
		t.Errorf("pre-decision explanation wrong: status=%q decidedAt=%v decidedBy=%v",
			pre.Status, pre.DecidedAt, pre.DecidedBy)
	}
	if pre.RulesCount != 1 {
		t.Errorf("RulesCount = %d, want 1", pre.RulesCount)
	}
	if string(pre.Scores) == "" {
		t.Errorf("Scores must never be empty raw JSON")
	}

	if _, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "user_7", ApprovalOptions{}); err != nil {
		t.Fatalf("approve: %v", err)
	}

	post, err := ExplainProposal(context.Background(), db, proposalID)
	if err != nil {
		t.Fatalf("explain post: %v", err)
	}
	if post.Status != "approved" {
		t.Errorf("status = %q, want approved", post.Status)
	}
	if post.DecidedAt == nil || *post.DecidedAt == "" {
		t.Errorf("DecidedAt not populated: %v", post.DecidedAt)
	}
	if post.DecidedBy == nil || *post.DecidedBy != "user_7" {
		t.Errorf("DecidedBy = %v, want user_7", post.DecidedBy)
	}
}

func TestExplainProposal_EmptyScoreJSONDefaultsToObject(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	applyV89Schema(t, db)
	if _, err := db.Exec(
		`INSERT INTO memory_proposals (id, workspace_id, crew_id, proposal_path, status, score_json)
		 VALUES ('mp_blank_score', 'ws_test', 'crew_test', '/tmp/p.md', 'pending', '')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	out, err := ExplainProposal(context.Background(), db, "mp_blank_score")
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	if string(out.Scores) != "{}" {
		t.Errorf("empty score_json must normalise to '{}', got %q", out.Scores)
	}
}

func TestExplainProposal_QueryError(t *testing.T) {
	db := openDB(t)
	db.Close()
	_, err := ExplainProposal(context.Background(), db, "mp_x")
	if err == nil || errors.Is(err, ErrProposalNotFound) {
		t.Errorf("closed DB should yield a wrapped query error, got %v", err)
	}
}

// --- ApproveProposal best-effort branches ----------------------------------------------

// TestApproveProposal_VersionRecordFailureDoesNotAbort: BlobRoot set but
// the memory_versions table is missing — RecordVersion fails, the warn
// branch fires, and the approve must still fully succeed with an empty
// VersionSha.
func TestApproveProposal_VersionRecordFailureDoesNotAbort(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db) // deliberately NO memory_versions table

	proposalID, outputDir := seedPendingProposal(t, db, w)
	appr, err := ApproveProposal(context.Background(), db, w, quietLogger(), proposalID, "u1",
		ApprovalOptions{BlobRoot: filepath.Join(outputDir, "versions")})
	if err != nil {
		t.Fatalf("approve must survive version-record failure: %v", err)
	}
	if appr.VersionSha != "" {
		t.Errorf("VersionSha = %q, want empty when recording failed", appr.VersionSha)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if status != "approved" {
		t.Errorf("status = %q, want approved", status)
	}
}

// TestApproveProposal_EmitFailureDoesNotAbort: the final journal emit is
// best-effort. A failing emitter must not undo the approve.
func TestApproveProposal_EmitFailureDoesNotAbort(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	w := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer w.Close()
	applyV89Schema(t, db)

	proposalID, _ := seedPendingProposal(t, db, w)
	// nil logger also exercises the slog.Default fallback branch.
	appr, err := ApproveProposal(context.Background(), db, &failEmitter{okFor: 0}, nil, proposalID, "u1", ApprovalOptions{})
	if err != nil {
		t.Fatalf("approve must survive emit failure: %v", err)
	}
	if appr.RulesMerged != 1 {
		t.Errorf("RulesMerged = %d, want 1", appr.RulesMerged)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM memory_proposals WHERE id = ?`, proposalID).Scan(&status); err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if status != "approved" {
		t.Errorf("status = %q, want approved despite emit failure", status)
	}
}

func TestRejectProposal_NilLoggerAndMissingRow(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	applyV89Schema(t, db)
	err := RejectProposal(context.Background(), db, &noopEmitter{}, nil, "mp_missing", "u1", "why not")
	if !errors.Is(err, ErrProposalNotFound) {
		t.Errorf("err = %v, want ErrProposalNotFound", err)
	}
}

// --- appendToCanonical error branches ---------------------------------------------------

func TestAppendToCanonical_MkdirFails(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	// Parent of the canonical path is a regular file → MkdirAll fails.
	err := appendToCanonical(filepath.Join(blocker, "sub", "learned-x.md"), time.Now(), "body")
	if err == nil || !strings.Contains(err.Error(), "mkdir canonical") {
		t.Errorf("expected mkdir error, got %v", err)
	}
}

// TestAppendToCanonical_UnreadableTargetIsAnError pins the rule #1999
// carried over from #1807: only fs.ErrNotExist may be read as "this is
// the first approval of the day". The write is a whole-file replace
// now, so a read failure we shrugged off would mean writing the
// first-run header over content we never read — where the old O_APPEND
// could append blind and survive it. A directory at the canonical path
// is the cheap portable way to make the pre-read fail with something
// that is not ErrNotExist.
func TestAppendToCanonical_UnreadableTargetIsAnError(t *testing.T) {
	dir := t.TempDir()
	asDir := filepath.Join(dir, "learned-x.md")
	if err := os.Mkdir(asDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := appendToCanonical(asDir, time.Now(), "body")
	if err == nil || !strings.Contains(err.Error(), "read canonical") {
		t.Errorf("expected read error, got %v", err)
	}
}

// TestAppendToCanonical_RefusesSymlinkedTarget is the guard the
// whole-file replace needs and the O_APPEND shape did not.
//
// appendRules — the OTHER writer of this same learned-YYYY-MM-DD.md, in
// the same directory, under the same <path>.lock — Lstats the target and
// refuses a symlink ("refusing symlinked target"). appendToCanonical now
// writes with the same shape, so it needs the same refusal, and for a
// sharper reason: the pre-read is what supplies the prefix of the new
// file. os.ReadFile FOLLOWS a planted final-component symlink, so
// without this check the link target's bytes are copied verbatim into
// the crew's canonical learned file — which the HITL diff endpoint, the
// audit watcher, every agent projecting shared memory, and the
// memory_versions audit blob all read. The old O_APPEND wrote THROUGH
// the link instead (#1039's confused-deputy write); neither is
// acceptable, and internal/memory already refuses the shape for the
// host-side card writers.
func TestAppendToCanonical_RefusesSymlinkedTarget(t *testing.T) {
	dir := t.TempDir()
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "secret.txt")
	const secretBody = "content the approver may not copy into crew memory\n"
	if err := os.WriteFile(secret, []byte(secretBody), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	canonical := filepath.Join(dir, "learned-2026-06-01.md")
	if err := os.Symlink(secret, canonical); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	err := appendToCanonical(canonical, time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), "- **rule**\n")
	if err == nil {
		t.Fatal("appendToCanonical accepted a symlinked canonical path")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Errorf("error = %v, want it to name the symlink refusal", err)
	}

	// The refusal must come BEFORE anything is read or written: the link
	// still points at the secret, and the secret is untouched.
	fi, lerr := os.Lstat(canonical)
	if lerr != nil {
		t.Fatalf("lstat canonical: %v", lerr)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file — the refusal came too late")
	}
	got, rerr := os.ReadFile(canonical)
	if rerr != nil {
		t.Fatalf("read through link: %v", rerr)
	}
	if string(got) != secretBody {
		t.Errorf("the link target was modified: %q", got)
	}
}

func TestAppendToCanonical_LockFails(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "learned-x.md")
	// A directory at the sentinel path makes the flock OpenFile fail.
	if err := os.Mkdir(canonical+".lock", 0o755); err != nil {
		t.Fatalf("mkdir lock blocker: %v", err)
	}
	err := appendToCanonical(canonical, time.Now(), "body")
	if err == nil || !strings.Contains(err.Error(), "lock canonical") {
		t.Errorf("expected lock error, got %v", err)
	}
}

func TestAppendToCanonical_AppendsWithDivider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "learned-2026-06-12.md")
	now := time.Date(2026, 6, 12, 9, 30, 0, 0, time.UTC)

	if err := appendToCanonical(path, now, "- rule one"); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := appendToCanonical(path, now, "- rule two\n"); err != nil {
		t.Fatalf("second append: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(body)
	if got := strings.Count(s, "# Learned rules"); got != 1 {
		t.Errorf("header count = %d, want 1", got)
	}
	if got := strings.Count(s, "## Approved at"); got != 2 {
		t.Errorf("approved sections = %d, want 2", got)
	}
	if !strings.Contains(s, "\n---\n") {
		t.Errorf("missing divider between blocks:\n%s", s)
	}
	if !strings.Contains(s, "- rule one") || !strings.Contains(s, "- rule two") {
		t.Errorf("bodies missing:\n%s", s)
	}
	if !strings.HasSuffix(s, "\n") {
		t.Errorf("file must end with newline")
	}
}
