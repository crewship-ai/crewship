package pipeline

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/llm"
)

func TestRunVerdictUnavailableInputsNeverReachProvider(t *testing.T) {
	ctx := context.Background()
	// No configured resolver must be safe even without a database.
	newRunVerdictHook(nil, nil, nil, nil)(ctx, "ws", "crew", "agent", "pipeline", "slug", "run")
	for _, stage := range []string{"flag storage", "disabled", "missing journal"} {
		t.Run(stage, func(t *testing.T) {
			db := openStoreTestDB(t)
			defer db.Close()
			if stage != "flag storage" {
				_, err := db.ExecContext(ctx, `CREATE TABLE feature_flags (id TEXT, key TEXT, enabled INTEGER); CREATE TABLE feature_flag_overrides (flag_id TEXT, workspace_id TEXT, enabled INTEGER); INSERT INTO feature_flags VALUES ('flag', 'run_verdict_summaries', 0);`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if stage == "missing journal" {
				if _, err := db.ExecContext(ctx, `UPDATE feature_flags SET enabled = 1`); err != nil {
					t.Fatal(err)
				}
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			calls := 0
			resolve := func() (llm.Provider, string, time.Duration) { calls++; return nil, "", 0 }
			newRunVerdictHook(db, nil, resolve, logger)(ctx, "ws", "crew", "agent", "pipeline", "slug", "run")
			if calls != 0 {
				t.Fatal("optional verdict attempted provider resolution without usable enabled journal")
			}
			if stage == "disabled" && logs.Len() != 0 {
				t.Fatalf("disabled feature logged a failure: %s", logs.String())
			}
			if stage == "flag storage" && !strings.Contains(logs.String(), "feature flag check") {
				t.Fatalf("missing flag diagnostic: %s", logs.String())
			}
			if stage == "missing journal" && !strings.Contains(logs.String(), "fetch entries") {
				t.Fatalf("missing journal diagnostic: %s", logs.String())
			}
		})
	}
}
