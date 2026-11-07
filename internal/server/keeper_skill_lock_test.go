package server

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/skills"
)

// The lifecycle persister refuses to move a locked skill to stale or archived
// even if the evaluator were bypassed (#5045).
func TestSqlSkillPersister_SetLifecycleSkipsLockedSkill(t *testing.T) {
	t.Parallel()
	db := krDB(t)
	mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name, locked) VALUES ('sk_lock','lk','lk','L',1)`)
	mustExec(t, db, `INSERT INTO skills (id, name, slug, display_name) VALUES ('sk_free','fr','fr','F')`)
	pers := &sqlSkillPersister{db: db, logger: krLogger()}

	for _, next := range []skills.LifecycleState{skills.LifecycleStale, skills.LifecycleArchived} {
		for id, want := range map[string]string{"sk_lock": "active", "sk_free": string(next)} {
			mustExec(t, db, `UPDATE skills SET lifecycle_state='active' WHERE id = ?`, id)
			if err := pers.SetLifecycle(context.Background(), id, next, "unused"); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := db.QueryRow(`SELECT lifecycle_state FROM skills WHERE id = ?`, id).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s -> %s: state = %q, want %q", id, next, got, want)
			}
		}
	}
}
