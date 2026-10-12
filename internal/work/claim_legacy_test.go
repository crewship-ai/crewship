package work

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
)

func seedLegacyAssignment(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('ws1','test','test')`,
		`INSERT INTO crews(id,workspace_id,name,slug,container_memory_mb) VALUES('crew','ws1','test','test',4096)`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('legacy','ws1','crew','test','test')`,
		`INSERT INTO chats(id,workspace_id,agent_id,title,mode,status) VALUES('chat','ws1','legacy','test','MISSION','ACTIVE')`,
		`INSERT INTO assignments(id,workspace_id,chat_id,assigned_by_id,assigned_to_id,task,status) VALUES('old','ws1','chat','legacy','legacy','test','RUNNING')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClaimLegacyAssignmentLimitsAndFairness(t *testing.T) {
	for _, gate := range []string{"server", "background", "agent", "crew"} {
		t.Run(gate, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			seedLegacyAssignment(t, db)
			lim := DefaultLimits()
			blocked := backgroundReq("new")
			switch gate {
			case "server":
				lim.ServerTotal = 1
			case "background":
				lim.ServerBackground = 1
			case "agent":
				blocked.AgentID = "legacy"
			case "crew":
				blocked.CrewID = "crew"
				if _, err := db.Exec(`UPDATE crews SET max_concurrent_agents=1 WHERE id='crew'`); err != nil {
					t.Fatal(err)
				}
			}
			// More than one scan batch of blocked work must not hide another
			// agent/crew's eligible item behind the LIMIT.
			for i := 0; i < candidateBatch+1; i++ {
				accept(t, s, db, blocked)
			}
			free := accept(t, s, db, backgroundReq("free"))
			claim, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test", Limits: lim})
			if gate == "server" || gate == "background" {
				if !errors.Is(err, ErrNoWork) {
					t.Fatalf("claim=%v err=%v", claim, err)
				}
			} else if err != nil || claim.Item.ID != free.WorkID {
				t.Fatalf("claim=%v err=%v want=%s", claim, err, free.WorkID)
			}
		})
	}
}

func TestClaimLegacyAssignmentDeduplicatesOnlyHoldingOwner(t *testing.T) {
	s, db, _ := newTestStore(t)
	seedLegacyAssignment(t, db)
	r := backgroundReq("legacy")
	r.Source, r.DomainKind, r.DomainID, r.CrewID = SourceAssignment, "assignment", "old", "crew"
	owner := accept(t, s, db, r)
	for _, tc := range []struct {
		state string
		want  int
	}{{"queued", 1}, {"running", 1}, {"needs_reconciliation", 1}, {"succeeded", 1}} {
		t.Run(tc.state, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE work_items SET state=? WHERE id=?`, tc.state, owner.WorkID); err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			counts, err := s.countLiveTx(context.Background(), tx)
			if err != nil || counts.total != tc.want || counts.background != tc.want {
				t.Fatalf("counts=%+v err=%v", counts, err)
			}
		})
	}
	// Same agent, different assignment: it is a second execution.
	if _, err := db.Exec(`UPDATE work_items SET state='running',domain_id='different' WHERE id=?`, owner.WorkID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	counts, err := s.countLiveTx(context.Background(), tx)
	if err != nil || counts.total != 2 {
		t.Fatalf("counts=%+v err=%v", counts, err)
	}
}

func TestClaimCrewBudgetMatchesMemorySetting(t *testing.T) {
	for _, tc := range []struct {
		setting string
		want    int
	}{{"", 2}, {"1024", 4}, {"0", 2}, {"999999", 2}, {"garbage", 2}, {"+1024", 4}} {
		t.Run(fmt.Sprintf("setting_%s", tc.setting), func(t *testing.T) {
			_, db, _ := newTestStore(t)
			seedLegacyAssignment(t, db)
			if tc.setting != "" {
				if _, err := db.Exec(`INSERT INTO app_settings(key,value) VALUES('runtime.agent_min_memory_mb',?)`, tc.setting); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			expr, err := crewBudgetExpression(context.Background(), tx, "?")
			if err != nil {
				t.Fatal(err)
			}
			var budget int
			if err := tx.QueryRow(`SELECT `+expr, "crew").Scan(&budget); err != nil {
				t.Fatal(err)
			}
			if budget != tc.want {
				t.Fatalf("budget=%d want=%d", budget, tc.want)
			}
		})
	}
}

func TestClaimLegacySessionBlocksDifferentAgent(t *testing.T) {
	s, db, _ := newTestStore(t)
	seedLegacyAssignment(t, db)
	for _, q := range []string{
		`INSERT INTO missions(id,workspace_id,crew_id,lead_agent_id,trace_id,title) VALUES('mission','ws1','crew','legacy','trace','test')`,
		`INSERT INTO issue_agent_sessions(id,workspace_id,mission_id,agent_id) VALUES('session','ws1','mission','legacy')`,
		`UPDATE assignments SET session_id='session' WHERE id='old'`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	blocked := chatReq("different", "session")
	accept(t, s, db, blocked)
	if c, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test"}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("claim=%v err=%v", c, err)
	}
	if _, err := db.Exec(`UPDATE assignments SET status='COMPLETED' WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test"}); err != nil {
		t.Fatal(err)
	}
}

func TestClaimMixedLegacyCrewLastSlotAtomic(t *testing.T) {
	s, db, _ := newTestStore(t)
	seedLegacyAssignment(t, db)
	for _, agent := range []string{"first", "second"} {
		r := backgroundReq(agent)
		r.CrewID = "crew"
		accept(t, s, db, r)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	var claimed, blocked int
	for err := range errs {
		if err == nil {
			claimed++
		} else if errors.Is(err, ErrNoWork) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if claimed != 1 || blocked != 1 {
		t.Fatalf("claimed=%d blocked=%d", claimed, blocked)
	}
}

func TestClaimCorruptCrewOverrideCannotBypassCapacity(t *testing.T) {
	s, db, _ := newTestStore(t)
	seedLegacyAssignment(t, db)
	if _, err := db.Exec(`UPDATE crews SET max_concurrent_agents='bogus' WHERE id='crew'`); err != nil {
		t.Fatal(err)
	}
	r := backgroundReq("new-agent")
	r.CrewID = "crew"
	blocked := accept(t, s, db, r)
	it, err := s.Get(context.Background(), blocked.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.countLiveTx(context.Background(), tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	allowed, err := s.admitsTx(context.Background(), tx, it, DefaultLimits(), live)
	_ = tx.Rollback()
	if err != nil || allowed {
		t.Fatalf("authoritative pass admitted corrupt override: %v %v", allowed, err)
	}
	if c, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test", WorkID: blocked.WorkID}); !errors.Is(err, ErrNoWork) {
		t.Fatalf("invalid override bypassed cap: claim=%v err=%v", c, err)
	}
	// The SQL prefilter must not hide another crew behind invalid configuration.
	free := accept(t, s, db, backgroundReq("free"))
	if c, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test"}); err != nil || c.Item.ID != free.WorkID {
		t.Fatalf("unrelated work blocked: claim=%v err=%v", c, err)
	}
	if _, err := db.Exec(`UPDATE crews SET max_concurrent_agents=2 WHERE id='crew'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(context.Background(), ClaimOptions{LeaseOwner: "test", WorkID: blocked.WorkID}); err != nil {
		t.Fatalf("valid override refused: %v", err)
	}
}
