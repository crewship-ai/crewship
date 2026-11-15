package server

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/craftapps"
	_ "modernc.org/sqlite"
)

// Agents in Word and Sheet are never on by default (#5315): without a
// document profile nothing is wired, and with one the agent engine is wired
// but the administrator's agents setting starts disabled. Only an explicit
// operator flag fixes it, and then Admin shows it as fixed.
func TestNativeCraftAgentsStayOffUntilExplicitlyAllowed(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE craft_application_settings(subject TEXT PRIMARY KEY, desired TEXT NOT NULL, drain_until TEXT, updated_by TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	cfg := &config.Config{}
	out, err := nativeCraftHTTPConfig(cfg, nil, nil)
	if err != nil || out.AgentsEnabled || out.AgentApply != nil || out.AgentInspect != nil {
		t.Fatal("agents wired without any document profile")
	}
	if m := out.Policy.Mode(ctx, db, craftapps.Agents); m != craftapps.ModeClosed {
		t.Fatalf("agents default mode = %s", m)
	}

	cfg.CraftNative.AgentsEnabled, cfg.CraftNative.AgentsEnabledFromEnv = true, true
	out, err = nativeCraftHTTPConfig(cfg, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := out.Policy.Get(ctx, db, craftapps.Agents)
	if err != nil || s.Source != craftapps.SourceServerConfig || s.Desired != craftapps.Enabled {
		t.Fatalf("explicit operator flag not reflected: %+v %v", s, err)
	}
	if _, _, err = out.Policy.Set(ctx, db, craftapps.Agents, craftapps.Disabled, "admin", 0); err == nil {
		t.Fatal("administrator overrode an operator-fixed setting")
	}
}
