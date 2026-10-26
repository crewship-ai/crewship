//go:build linux

package restrictedworkflow

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestCrewGrantPublicationKeepsExactSourceWithOverlappingRun(t *testing.T) {
	s, _ := publishedFixture(t)
	rights := []access.Right{{Kind: "routine", ID: "routine", Operation: "view"}, {Kind: "routine", ID: "routine", Operation: "edit"}, {Kind: "routine", ID: "routine", Operation: "publish"}, {Kind: "routine", ID: "routine", Operation: "run"}, {Kind: "crew", ID: "crew", Operation: "use"}}
	replaceGuest(t, s, "h1", rights...)
	// More than one derived agent row shares the crew root grant ID.
	mustExec(t, s, `INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('other-agent','w','crew','Other','other-agent','AGENT')`)
	tx, err := s.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, canonical, fingerprint, err := CompilePublicationIn(t.Context(), s.db, tx, "h1", "w", "routine", routine)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	publishVersion(t, tx, 2, routine, string(canonical), fingerprint)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Adding another source cannot invalidate or substitute the frozen source.
	direct := access.Right{Kind: "agent", ID: "agent", Operation: "run"}
	rights = append(rights, direct)
	replaceGuest(t, s, "h1", rights...)
	if _, err = s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "overlap"}, "", 0); err != nil {
		t.Fatal("valid overlap rejected", err)
	}
	// Keep the direct agent right, but remove exactly the crew source captured
	// by publication. Current access survives; this publication must not.
	rights = append(rights[:4], direct)
	replaceGuest(t, s, "h1", rights...)
	if err = (access.Store{DB: s.db}).Check(t.Context(), "h1", "w", direct); err != nil {
		t.Fatal("control: direct run missing", err)
	}
	if _, err = s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "revoked source"}, "", 0); !errors.Is(err, ErrDenied) {
		t.Fatal("frozen crew source substituted", err)
	}
}
