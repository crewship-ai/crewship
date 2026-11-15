package artifactrun

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const baseSchema = `
CREATE TABLE workspaces (id TEXT PRIMARY KEY);
CREATE TABLE crews (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
CREATE TABLE agents (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, crew_id TEXT);
CREATE TABLE pages (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL);
CREATE TABLE feature_flags (id TEXT PRIMARY KEY, key TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 0);
CREATE TABLE feature_flag_overrides (id TEXT PRIMARY KEY, flag_id TEXT NOT NULL, workspace_id TEXT NOT NULL, enabled INTEGER NOT NULL);
INSERT INTO workspaces VALUES ('ws'),('ws-off');
INSERT INTO crews VALUES ('crew','ws'),('crew-off','ws-off'),('crew-2','ws');
INSERT INTO agents VALUES ('agent','ws','crew'),('agent-off','ws-off','crew-off');
INSERT INTO feature_flags VALUES ('ff_artifact_agent_access_modes','artifact_agent_access_modes',0);
INSERT INTO feature_flag_overrides VALUES ('on','ff_artifact_agent_access_modes','ws',1);
`

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	migration, err := os.ReadFile("../database/migrations/20261114093302_artifact_unattended_runs.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(baseSchema + string(migration)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestRecord_MintsAFreshCapabilityStoredOnlyAsADigest(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	a, err := Record(ctx, db, Run{WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", RunID: "run-a", Trigger: TriggerWebhook, Source: "hook"}, time.Hour)
	if err != nil || !strings.HasPrefix(a, Prefix) || len(a) != len(Prefix)+64 {
		t.Fatalf("capability %q %v", a, err)
	}
	b, err := Record(ctx, db, Run{WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", RunID: "run-b", Trigger: TriggerRoutine}, time.Hour)
	if err != nil || b == a {
		t.Fatalf("second run's capability %q (first %q) %v", b, a, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT capability_sha256 FROM artifact_unattended_runs WHERE run_id='run-a'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == a || strings.Contains(stored, strings.TrimPrefix(a, Prefix)) || !Matches(a, stored) || Matches(b, stored) {
		t.Fatalf("stored %q for %q", stored, a)
	}
}

func TestRecord_RefusesAReplayOfTheSameRun(t *testing.T) {
	db := openDB(t)
	run := Run{WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", RunID: "run-once", Trigger: TriggerScheduled}
	if _, err := Record(context.Background(), db, run, time.Hour); err != nil {
		t.Fatal(err)
	}
	if c, err := Record(context.Background(), db, run, time.Hour); !errors.Is(err, ErrInvalid) || c != "" {
		t.Fatalf("replay minted %q %v", c, err)
	}
}

func TestRecord_RefusesWhatIsNotARunWithoutAHuman(t *testing.T) {
	db := openDB(t)
	for name, run := range map[string]Run{
		"chat trigger":     {WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", RunID: "r1", Trigger: "USER"},
		"no agent":         {WorkspaceID: "ws", CrewID: "crew", RunID: "r2", Trigger: TriggerWebhook},
		"no crew":          {WorkspaceID: "ws", AgentID: "agent", RunID: "r3", Trigger: TriggerWebhook},
		"agent of no crew": {WorkspaceID: "ws", CrewID: "crew-2", AgentID: "agent", RunID: "r4", Trigger: TriggerWebhook},
		"no run":           {WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", Trigger: TriggerWebhook},
		"oversized source": {WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", RunID: "r5", Trigger: TriggerWebhook, Source: strings.Repeat("s", 501)},
		"agent elsewhere":  {WorkspaceID: "ws", CrewID: "crew", AgentID: "agent-off", RunID: "r6", Trigger: TriggerWebhook},
	} {
		if c, err := Record(context.Background(), db, run, time.Hour); err == nil || c != "" {
			t.Fatalf("%s: recorded %q", name, c)
		}
	}
}

func TestRecord_FlagOffRecordsNothing(t *testing.T) {
	db := openDB(t)
	c, err := Record(context.Background(), db, Run{WorkspaceID: "ws-off", CrewID: "crew-off", AgentID: "agent-off", RunID: "off", Trigger: TriggerWebhook}, time.Hour)
	if err != nil || c != "" {
		t.Fatalf("flag off: %q %v", c, err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_unattended_runs`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("rows %d %v", n, err)
	}
}

func TestEnd_EndsOnceAndTheRowIsOtherwiseImmutable(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, err := Record(ctx, db, Run{WorkspaceID: "ws", CrewID: "crew", AgentID: "agent", RunID: "run-end", Trigger: TriggerWebhook}, 0); err != nil {
		t.Fatal(err)
	}
	var created, expires string
	if err := db.QueryRow(`SELECT created_at,expires_at FROM artifact_unattended_runs WHERE run_id='run-end'`).Scan(&created, &expires); err != nil {
		t.Fatal(err)
	}
	c, _ := time.Parse(time.RFC3339Nano, created)
	e, _ := time.Parse(time.RFC3339Nano, expires)
	if got := e.Sub(c); got != MaxTTL {
		t.Fatalf("ttl %s, want the bound %s", got, MaxTTL)
	}
	if _, err := db.Exec(`UPDATE artifact_unattended_runs SET agent_id='other' WHERE run_id='run-end'`); err == nil {
		t.Fatal("the run's agent could be changed")
	}
	if _, err := db.Exec(`UPDATE artifact_unattended_runs SET expires_at='2099-01-01T00:00:00Z' WHERE run_id='run-end'`); err == nil {
		t.Fatal("the run's expiry could be extended")
	}
	for i := 0; i < 2; i++ {
		if err := End(ctx, db, "run-end"); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := db.Exec(`UPDATE artifact_unattended_runs SET ended_at=NULL WHERE run_id='run-end'`); err == nil {
		t.Fatal("an ended run could be reopened")
	}
	if err := End(ctx, db, "never-recorded"); err != nil {
		t.Fatal(err)
	}
}

func TestMatches_RefusesAnythingElse(t *testing.T) {
	c := Prefix + strings.Repeat("0f", 32)
	d := Digest(c)
	for _, v := range []string{"", "agtv2.x.y.z.w", strings.Repeat("0f", 32), Prefix + strings.Repeat("0f", 31), c + "0"} {
		if Matches(v, d) {
			t.Fatalf("%q matched", v)
		}
	}
	if !Matches(c, d) || Matches(c, "") {
		t.Fatal("digest compare")
	}
}
