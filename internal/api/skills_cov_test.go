package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/skills"
)

// skills_cov_test.go — remaining branches: the installed_on
// best-effort failure on List, the Delete error/race/journal arms,
// sanitiseSkillImportError's classification table, and
// severityForUnsafe. Helpers prefixed covSk.

type covSkFailEmitter struct{}

func (covSkFailEmitter) Emit(_ context.Context, _ journal.Entry) (string, error) {
	return "", errors.New("journal sink down")
}
func (covSkFailEmitter) Flush(_ context.Context) error { return nil }

func covSkFixture(t *testing.T) (*SkillHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewSkillHandler(db, newTestLogger()), userID, wsID
}

func covSkSeedSkill(t *testing.T, h *SkillHandler, id, source string) {
	t.Helper()
	execOrFatal(t, h.db, `INSERT INTO skills
		(id, name, slug, display_name, description, vendor, version, category, source, verification,
		 downloads, rating_count, pricing_tier, featured, tags, credential_requirements, content)
		VALUES (?, ?, ?, ?, 'd', 'v', '1.0.0', 'CODING', ?, 'UNVERIFIED', 0, 0, 'FREE', 0, '[]', '[]', '# skill')`,
		id, id, id, "Skill "+id, source)
}

// TestCovSk_List_InstalledOnFailure_NonFatal — a broken agent_skills
// table only degrades the cards (no avatars), never fails the list.
func TestCovSk_List_InstalledOnFailure_NonFatal(t *testing.T) {
	h, userID, wsID := covSkFixture(t)
	covSkSeedSkill(t, h, "covsk-s1", "CUSTOM")
	execOrFatal(t, h.db, `ALTER TABLE agent_skills RENAME TO as_broken`)

	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/skills", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "covsk-s1") {
		t.Errorf("body missing seeded skill: %s", rr.Body.String())
	}
}

func covSkDelete(h *SkillHandler, userID, wsID, skillID string) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/skills/"+skillID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("skillId", skillID)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	return rr
}

func TestCovSk_Delete_ExecError_500(t *testing.T) {
	h, userID, wsID := covSkFixture(t)
	covSkSeedSkill(t, h, "covsk-s2", "CUSTOM")
	execOrFatal(t, h.db, `CREATE TRIGGER covsk_block_del BEFORE DELETE ON skills
		BEGIN SELECT RAISE(ABORT, 'covsk forced'); END`)
	rr := covSkDelete(h, userID, wsID, "covsk-s2")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovSk_Delete_ZeroRows_404(t *testing.T) {
	h, userID, wsID := covSkFixture(t)
	covSkSeedSkill(t, h, "covsk-s3", "CUSTOM")
	execOrFatal(t, h.db, `CREATE TRIGGER covsk_ignore_del BEFORE DELETE ON skills
		BEGIN SELECT RAISE(IGNORE); END`)
	rr := covSkDelete(h, userID, wsID, "covsk-s3")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovSk_Delete_JournalEmitFailure_NonFatal — the audit emit
// failing must not undo a successful delete response.
func TestCovSk_Delete_JournalEmitFailure_NonFatal(t *testing.T) {
	h, userID, wsID := covSkFixture(t)
	covSkSeedSkill(t, h, "covsk-s4", "CUSTOM")
	h.SetJournal(covSkFailEmitter{})
	rr := covSkDelete(h, userID, wsID, "covsk-s4")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"deleted":true`) {
		t.Errorf("body = %s, want deleted ack", rr.Body.String())
	}
}

func TestCovSk_SanitiseSkillImportError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"ssrf", skills.ErrSSRFBlocked,
			"import blocked: target resolves to a private, loopback, or otherwise restricted address"},
		{"https-only", errors.New("only HTTPS URLs are allowed"),
			"only HTTPS URLs are allowed"},
		{"localhost", errors.New("validate: localhost URLs are not allowed"),
			"validate: localhost URLs are not allowed"},
		{"parse", errors.New("parse skill: missing front matter"),
			"parse skill: missing front matter"},
	}
	for _, c := range cases {
		if got := sanitiseSkillImportError(c.err); got != c.want {
			t.Errorf("%s: sanitise = %q, want %q", c.name, got, c.want)
		}
	}
	// A raw network error must NOT be echoed verbatim.
	raw := errors.New(`Get "https://10.0.0.5/x": dial tcp 10.0.0.5:443: connect: connection refused`)
	got := sanitiseSkillImportError(raw)
	if strings.Contains(got, "10.0.0.5") {
		t.Errorf("sanitised error leaked internal IP: %q", got)
	}
}

func TestCovSk_SeverityForUnsafe(t *testing.T) {
	if severityForUnsafe(true) != journal.SeverityWarn {
		t.Errorf("unsafe import severity = %v, want warn", severityForUnsafe(true))
	}
	if severityForUnsafe(false) != journal.SeverityInfo {
		t.Errorf("safe import severity = %v, want info", severityForUnsafe(false))
	}
}
