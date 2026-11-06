//go:build !clionly && !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/journal"
)

type lockJournalSink struct {
	mu    sync.Mutex
	types []journal.EntryType
}

func (s *lockJournalSink) Emit(_ context.Context, e journal.Entry) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.types = append(s.types, e.Type)
	return "id", nil
}
func (s *lockJournalSink) Flush(context.Context) error { return nil }

// Real CLI process -> real router: a curator locks a skill, an autonomous
// rewrite is refused with an error naming the lock, the lock shows in list and
// get, and unlocking lets the write through again (#5045).
func TestAcceptance_SkillLockRefusesAWorkspaceOwner(t *testing.T) {
	rig, db := startFidelityServer(t, "fid-ws", "fid-owner", append(append([]string{}, fidelityBaseSeed...),
		`INSERT INTO skills(id,name,slug,display_name,source,lifecycle_state,content) VALUES('sk-lk','lk-skill','lk-skill','LK','CUSTOM','active','# original')`,
		`INSERT INTO agent_skills(id,agent_id,skill_id,enabled) VALUES('as-lk','fid-agent-id','sk-lk',1)`,
		`INSERT INTO users(id,email,full_name,instance_role) VALUES('fid-boss','boss@fid.invalid','Boss','ADMIN')`,
	))
	out, err := rig.run(nil, "skill", "lock", "lk-skill")
	if err == nil {
		t.Fatalf("a workspace owner who only assigned the skill must not lock it, got: %s", out)
	}
	if !strings.Contains(out, "instance administrator") {
		t.Errorf("error must name who can lock: %s", out)
	}
	var locked int
	if err := db.QueryRow(`SELECT locked FROM skills WHERE id='sk-lk'`).Scan(&locked); err != nil || locked != 0 {
		t.Fatalf("locked = %d err=%v, want 0", locked, err)
	}
}

func TestAcceptance_SkillLockBlocksRewrites(t *testing.T) {
	sink := &lockJournalSink{}
	rig, db := startFidelityServer(t, "fid-ws", "fid-owner", append(append([]string{}, fidelityBaseSeed...),
		`INSERT INTO skills(id,name,slug,display_name,source,lifecycle_state,content) VALUES('sk-lk','lk-skill','lk-skill','LK','CUSTOM','active','# original')`,
		`UPDATE users SET instance_role='ADMIN' WHERE id='fid-owner'`,
	), api.WithJournal(sink))

	if out := rig.must("skill", "lock", "lk-skill"); !strings.Contains(out, "locked") {
		t.Errorf("skill lock output = %q", out)
	}
	doc := decodeDoc(t, rig.stdout("skill", "get", "lk-skill", "-f", "json"))
	if doc["locked"] != true {
		t.Errorf("skill get -f json locked = %v", doc["locked"])
	}
	wantContains(t, "skill get", rig.must("skill", "get", "lk-skill"), "locked")
	wantContains(t, "skill list", rig.must("skill", "list"), "locked")

	// Archiving by lifecycle automation is covered in unit tests; here the
	// autonomous rewrite path reachable from the CLI is a re-import.
	md := "---\nname: lk-skill\ndescription: Use when testing locks.\ncategory: CODING\n---\n# Rewritten\n\n## Procedure\n1. Do it.\n"
	mdFile := filepath.Join(t.TempDir(), "SKILL.md")
	if err := os.WriteFile(mdFile, []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := rig.run(nil, "skill", "import", "--file", mdFile)
	if err == nil {
		t.Fatalf("import over a locked skill should fail, got: %s", out)
	}
	if !strings.Contains(out, "locked") {
		t.Errorf("error must name the lock: %s", out)
	}
	var content string
	if err := db.QueryRow(`SELECT content FROM skills WHERE id='sk-lk'`).Scan(&content); err != nil || content != "# original" {
		t.Fatalf("content = %q err=%v, want untouched", content, err)
	}
	blocked := 0
	sink.mu.Lock()
	for _, typ := range sink.types {
		if typ == journal.EntrySkillWriteBlocked {
			blocked++
		}
	}
	sink.mu.Unlock()
	if blocked != 1 {
		t.Errorf("skill.write_blocked journal entries = %d, want 1", blocked)
	}

	wantContains(t, "skill unlock", rig.must("skill", "unlock", "lk-skill"), "unlocked")
	doc = decodeDoc(t, rig.stdout("skill", "get", "lk-skill", "-f", "json"))
	if doc["locked"] != false {
		t.Errorf("after unlock locked = %v", doc["locked"])
	}
	rig.must("skill", "import", "--file", mdFile)
	if err := db.QueryRow(`SELECT content FROM skills WHERE id='sk-lk'`).Scan(&content); err != nil || !strings.Contains(content, "Rewritten") {
		t.Fatalf("unlocked import should rewrite: %q %v", content, err)
	}
}
