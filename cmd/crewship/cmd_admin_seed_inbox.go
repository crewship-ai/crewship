//go:build !clionly

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/inbox"
)

// `crewship admin seed-inbox` fills the inbox with one row of every kind so
// the surface can actually be exercised.
//
// It exists because the inbox has no create endpoint — every row is written
// by a producer (a pipeline reaching a waitpoint, an agent escalating, the
// keeper sweeping), so a fresh workspace shows an empty inbox and there is no
// way to see how the views, facets and the reading pane behave against real
// variety. Reviewing an inbox redesign against two rows is guesswork.
//
// Rows go in through inbox.Insert and harbormaster.Enqueue — the same writers
// the real producers use — so what lands is shaped exactly like production
// data, not hand-built SQL that agrees with whatever the UI happens to expect.
// Everything it writes carries a `seed_` source id, which is what --clear
// removes; it never touches a row it did not create.
//
// Host-side, like the rest of `admin`: it opens the local database through
// openGatedLocalDB, so it honours DATABASE_URL and refuses when the operator
// has named a server (a clone whose crewshipd runs against ./crewship.db while
// the default data dir holds an unrelated file is the normal case here, and
// seeding the wrong one silently is the failure this gate exists to stop).
var adminSeedInboxCmd = &cobra.Command{
	Use:   "seed-inbox",
	Short: "Fill the inbox with one row of every kind (development data)",
	Long: `Write a spread of inbox items and approval gates into the local database.

One row per inbox kind, in a mix of states, plus approval-queue rows both
pending and decided — enough to exercise Needs action, Updates and History and
every facet. Development data: give it a scratch workspace, not a real one.

FIXTURE, NOT A RUNTIME TEST. The run behind a seeded approval gate is a row,
not an execution: no executor waits on it, so approving one resumes nothing.
Use it to remove "is it seeded?" preconditions from a test, never as evidence
that a decision took effect — that is approval-gate-demo's job.

  crewship admin seed-inbox --workspace <id>
  crewship admin seed-inbox --workspace <id> --clear   # remove what it wrote`,
	RunE: runAdminSeedInbox,
}

type seedRow struct {
	kind     string
	title    string
	body     string
	sender   string
	priority string
	blocking bool
	payload  map[string]any
	category string
	// resolve, when set, marks the row resolved with this action once written
	// — that is how History gets both real decisions and archived noise.
	resolve string
}

func seedInboxRows(now time.Time) []seedRow {
	in := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	return []seedRow{
		{
			kind: inbox.KindWaitpoint, title: "Approve production deploy of checkout-api",
			body:   "Pipeline `release` is holding at the approval gate before the production rollout.",
			sender: "Release pipeline", priority: "high", blocking: true,
			payload: map[string]any{"pipeline_run_id": "run_seed_1", "step_id": "approve", "timeout_at": in(35 * time.Minute), "risk_level": "normal"},
		},
		{
			kind: inbox.KindWaitpoint, title: "Drop the production analytics table",
			body:   "Reclaims 40 GB before tonight's migration. 18 months of event history, not covered by the nightly backup.",
			sender: "Riley", priority: "urgent", blocking: true,
			payload: map[string]any{"pipeline_run_id": "run_seed_2", "step_id": "drop", "timeout_at": in(25 * time.Minute), "risk_level": "destructive"},
		},
		{
			kind: inbox.KindEscalation, title: "Credential needed for the Stripe connector",
			body:   "The agent cannot continue without a live API key.",
			sender: "Ops crew", priority: "high", blocking: true,
			payload: map[string]any{"escalation_type": "credential", "provider": "stripe"},
		},
		{
			kind: inbox.KindEscalation, title: "New skill proposed: invoice-reconciler",
			body:   "Authored from three repeated runs. Review before it becomes available to the crew.",
			sender: "Skill author", priority: "medium", blocking: true,
			payload: map[string]any{"kind": "skill_proposal", "skill_id": "sk_seed_proposal"},
		},
		{
			kind: inbox.KindFailedRun, title: "Nightly backup run failed",
			body:   "Exit 1 — the container pool was full when the run started.",
			sender: "Scheduler", priority: "high",
			payload: map[string]any{"run_id": "run_seed_failed"},
		},
		{
			kind: inbox.KindMessage, title: "Riley replied in #ops",
			body:   "\"The migration is staged — I need the gate approved before 23:00.\"",
			sender: "Riley", priority: "medium", category: "chat.replies",
			payload: map[string]any{"session_id": "chat_seed_1"},
		},
		{
			kind: inbox.KindMessage, title: "Workspace digest — last 24 h",
			body:   "6 routine runs — 5 completed, 0 failed, 1 waiting. $0.28 total cost.",
			sender: "workspace-digest", priority: "low",
			payload: map[string]any{"pipeline_run_id": "run_seed_digest", "subkind": "routine_update"},
		},
		{
			kind: inbox.KindMemoryConsolidation, title: "Memory proposal: 4 facts to keep",
			body:   "Consolidation found four durable facts from this week's runs.",
			sender: "Memory keeper", priority: "medium",
			payload: map[string]any{"proposal_id": "mem_seed_1"},
		},
		{
			kind: inbox.KindScheduleMissed, title: "Missed occurrence: weekly-report",
			body:   "The 08:00 occurrence did not start — the workspace was over its container quota.",
			sender: "Scheduler", priority: "high",
			payload: map[string]any{"schedule_id": "sch_seed_1"},
		},
		{
			kind: inbox.KindScheduleCircuitBreakerTripped, title: "Circuit breaker tripped: sync-crm",
			body:   "Five consecutive failures. The schedule is paused until it is re-enabled.",
			sender: "Scheduler", priority: "urgent",
			payload: map[string]any{"schedule_id": "sch_seed_2"},
		},
		// The two trigger kinds that fire and never become a run (A4): a webhook
		// whose fire failed three times in a row, and an automation whose match
		// could not be enqueued three times in a row. Both route to routines.missed.
		{
			kind: inbox.KindWebhookFireFailed, title: "Webhook fire failed: github-push",
			body:   "Three consecutive fires did not become a run. Check the target routine and the fire log.",
			sender: "Webhooks", priority: "high",
			payload: map[string]any{"webhook_id": "whk_seed_1"},
		},
		{
			kind: inbox.KindAutomationEnqueueFailed, title: "Automation enqueue failed: on-issue-created",
			body:   "The rule matched three times and the run could not be enqueued each time.",
			sender: "Automations", priority: "high",
			payload: map[string]any{"automation_id": "aut_seed_1"},
		},
		// Three source-less curator advisories: no decision endpoint, nobody
		// blocked. They exist so the grouping rule has something to group.
		{kind: inbox.KindEscalation, title: "Skill check: could not evaluate invoice-parser", sender: "Skill Curator", priority: "medium"},
		{kind: inbox.KindEscalation, title: "Skill check: could not evaluate lead-scorer", sender: "Skill Curator", priority: "medium"},
		{kind: inbox.KindEscalation, title: "Skill check: could not evaluate report-writer", sender: "Skill Curator", priority: "medium"},
		// History: real decisions, and archived noise beside them.
		{
			kind: inbox.KindWaitpoint, title: "Approve schema migration 0141",
			body: "Adds a nullable column to `orders`.", sender: "Release pipeline", priority: "high",
			payload: map[string]any{"pipeline_run_id": "run_seed_3", "step_id": "approve"},
			resolve: "approved",
		},
		{
			kind: inbox.KindWaitpoint, title: "Approve outbound email blast",
			body: "12,400 recipients.", sender: "Marketing crew", priority: "high",
			payload: map[string]any{"pipeline_run_id": "run_seed_4", "step_id": "approve"},
			resolve: "denied",
		},
		{
			kind: inbox.KindMessage, title: "Weekly cost report",
			body: "Spend is flat week over week.", sender: "workspace-digest", priority: "low",
			resolve: "archived",
		},
	}
}

// seedSuffix mints the per-invocation tag that both identifier families carry:
// the run id (`run_seed_<suffix>`) and every source id (`seed_<suffix>_<n>`).
//
// It was `time.Now().Unix()`, separately in each place, and second precision is
// not enough for a command people run twice in a row. The second invocation
// re-minted the same run id and failed on the pipeline_runs primary key; with
// no pipeline to hang the run on it got further and re-minted the same source
// ids, where inbox.Insert dedupes silently while the success line still
// reported them as written.
//
// Base 36 keeps it short. The `run_seed_` and `seed_` prefixes are load-bearing
// and must not move: --clear finds what this wrote by LIKE on exactly those.
func seedSuffix(now time.Time) string {
	return strconv.FormatInt(now.UnixNano(), 36)
}

func runAdminSeedInbox(cmd *cobra.Command, _ []string) error {
	workspaceID, _ := cmd.Flags().GetString("workspace")
	clear, _ := cmd.Flags().GetBool("clear")
	if workspaceID == "" {
		return fmt.Errorf("--workspace is required")
	}

	db, err := openGatedLocalDB(cmd, "crewship admin seed-inbox", "")
	if err != nil {
		return err
	}
	defer db.Close()

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// One suffix for both identifier families, minted once: they have to agree
	// within an invocation and differ between two of them.
	suffix := seedSuffix(time.Now())

	if clear {
		res, err := db.ExecContext(ctx,
			`DELETE FROM inbox_items WHERE workspace_id = ? AND source_id LIKE 'seed_%'`, workspaceID)
		if err != nil {
			return fmt.Errorf("clear seeded inbox rows: %w", err)
		}
		n, _ := res.RowsAffected()
		res2, err := db.ExecContext(ctx,
			`DELETE FROM approvals_queue WHERE workspace_id = ? AND reason LIKE '[seed]%'`, workspaceID)
		if err != nil {
			return fmt.Errorf("clear seeded approvals: %w", err)
		}
		n2, _ := res2.RowsAffected()
		if _, err := db.ExecContext(ctx,
			`DELETE FROM pipeline_waitpoints WHERE workspace_id = ? AND token LIKE 'seed_%'`, workspaceID); err != nil {
			return fmt.Errorf("clear seeded waitpoint tokens: %w", err)
		}
		if _, err := db.ExecContext(ctx,
			`DELETE FROM pipeline_runs WHERE workspace_id = ? AND id LIKE 'run_seed_%'`, workspaceID); err != nil {
			return fmt.Errorf("clear the seeded run: %w", err)
		}
		cli.PrintSuccess(fmt.Sprintf("Removed %d seeded inbox item(s) and %d seeded approval(s).", n, n2))
		return nil
	}

	var ownerID string
	if err := db.QueryRowContext(ctx,
		`SELECT user_id FROM workspace_members WHERE workspace_id = ? ORDER BY created_at LIMIT 1`,
		workspaceID).Scan(&ownerID); err != nil {
		return fmt.Errorf("resolve a workspace member to attribute decisions to: %w", err)
	}

	// READ THIS BEFORE TREATING A SEEDED GATE AS A RUNTIME TEST.
	//
	// The run row below is SYNTHETIC. It is an INSERT into pipeline_runs with
	// status 'running'; no executor goroutine is parked on the waitpoint, so
	// approving a seeded gate flips the token and cascades the inbox row and
	// then NOTHING RESUMES. That is fine for what this command is for — a
	// deterministic fixture for the UI, the filters, History and response
	// shapes, and for proving a button reaches its endpoint — and it is not a
	// substitute for approval-gate-demo, which is the only thing that proves
	// approve → the run actually continued.
	//
	// The row exists at all because CancelOrphanedWaitpoints settles any gate
	// whose pipeline_runs row is missing or terminal on every boot (#2163).
	// Note the irony worth not repeating: that sweep exists because "an
	// unreachable gate that still accepts an approval tells the operator they
	// approved something that never ran" — and a seeded gate is exactly such a
	// gate. It is acceptable here only because this command is development
	// tooling and says so; it must never become the evidence for a release
	// claim about runtime behaviour. When the workspace has no pipeline to hang them on, the
	// waitpoint rows are written without a token: they then have no source at
	// all, which the inbox PATCH guard now recognises, so they can still be
	// dismissed into History instead of sitting in Needs action forever.
	var pipelineID, pipelineSlug string
	if err := db.QueryRowContext(ctx,
		`SELECT id, slug FROM pipelines WHERE workspace_id = ? ORDER BY created_at LIMIT 1`,
		workspaceID).Scan(&pipelineID, &pipelineSlug); err != nil {
		pipelineID = ""
	}
	seedRunID := ""
	if pipelineID != "" {
		seedRunID = "run_seed_" + suffix
		if _, err := db.ExecContext(ctx, `INSERT INTO pipeline_runs
			(id, workspace_id, pipeline_id, pipeline_slug, status, mode, started_at, current_step_id, triggered_via)
			VALUES (?, ?, ?, ?, 'running', 'run', ?, 'approve', 'manual')`,
			seedRunID, workspaceID, pipelineID, pipelineSlug,
			time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("seed the run behind the approval gates: %w", err)
		}
	}

	now := time.Now()
	written := 0
	for i, row := range seedInboxRows(now) {
		sourceID := fmt.Sprintf("seed_%s_%d", suffix, i)
		payload := row.payload
		if payload == nil {
			payload = map[string]any{}
		}
		if payload["pipeline_run_id"] != nil && seedRunID != "" {
			payload["pipeline_run_id"] = seedRunID
		}
		if err := inbox.Insert(ctx, db.DB, logger, inbox.Item{
			WorkspaceID: workspaceID,
			Kind:        row.kind,
			SourceID:    sourceID,
			Title:       row.title,
			BodyMD:      row.body,
			SenderType:  "agent",
			SenderName:  row.sender,
			Priority:    row.priority,
			Blocking:    row.blocking,
			Payload:     payload,
			Category:    row.category,
		}); err != nil {
			return fmt.Errorf("insert %s: %w", row.kind, err)
		}
		written++

		// A waitpoint is source-managed: the inbox row alone cannot be
		// decided, and its Approve button drives
		// /pipelines/waitpoints/{token}/approve. Give the pending ones a real
		// token row so that endpoint answers and the decision actually
		// completes — a seeded waitpoint with no source would render buttons
		// that 404, which is a worse demo than no row at all.
		if row.kind == inbox.KindWaitpoint && row.resolve == "" && seedRunID != "" {
			timeoutAt, _ := row.payload["timeout_at"].(string)
			if timeoutAt == "" {
				timeoutAt = now.Add(45 * time.Minute).UTC().Format(time.RFC3339)
			}
			runID := seedRunID
			stepID, _ := row.payload["step_id"].(string)
			if _, err := db.ExecContext(ctx, `INSERT INTO pipeline_waitpoints
				(token, workspace_id, pipeline_run_id, step_id, kind, prompt, status, timeout_at)
				VALUES (?, ?, ?, ?, 'approval', ?, 'pending', ?)`,
				sourceID, workspaceID, runID, stepID, row.title, timeoutAt); err != nil {
				return fmt.Errorf("seed the waitpoint token behind %q: %w", row.title, err)
			}
		}

		if row.resolve != "" {
			inbox.ResolveBySource(ctx, db.DB, logger, row.kind, sourceID, row.resolve, ownerID)
		}
	}

	// Approval gates, through the same writer Harbor Master uses. One is left
	// pending, one is decided, so History carries an approval receipt too.
	timeout := now.Add(45 * time.Minute)
	pendingID, err := harbormaster.Enqueue(ctx, db.DB, nil, harbormaster.Request{
		WorkspaceID: workspaceID, RequestedBy: ownerID,
		Kind:      harbormaster.KindDestructiveOp,
		Reason:    "[seed] delete the staging deployment and its volumes",
		Payload:   map[string]any{"tool": "delete_deployment", "args": map[string]any{"target": "staging"}},
		TimeoutAt: &timeout,
	})
	if err != nil {
		return fmt.Errorf("enqueue pending approval: %w", err)
	}
	decidedID, err := harbormaster.Enqueue(ctx, db.DB, nil, harbormaster.Request{
		WorkspaceID: workspaceID, RequestedBy: ownerID,
		Kind:    harbormaster.KindToolCall,
		Reason:  "[seed] run the quarterly export against the production replica",
		Payload: map[string]any{"tool": "shell.exec", "args": map[string]any{"command": "./export.sh"}},
	})
	if err != nil {
		return fmt.Errorf("enqueue decided approval: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE approvals_queue SET status = 'approved', decided_by = ?, decided_at = ?, decision_comment = ?
		 WHERE id = ? AND workspace_id = ?`,
		ownerID, now.UTC().Format(time.RFC3339), "[seed] approved for the quarter close", decidedID, workspaceID); err != nil {
		return fmt.Errorf("decide the seeded approval: %w", err)
	}

	cli.PrintSuccess(fmt.Sprintf(
		"Seeded %d inbox item(s) and 2 approval gate(s) into %s.\n  pending approval: %s\n  decided approval: %s\n  Remove them again with --clear.",
		written, workspaceID, pendingID, decidedID))
	return nil
}

func init() {
	// Declared per-command, like every other host-side command: requireLocalDB
	// refuses when the CLI is pointed at a server, and --local is how the
	// operator says the local file is what they meant. localdb_flag_guard
	// fails the build if a command uses that gate without offering the flag.
	adminSeedInboxCmd.Flags().Bool("local", false, localOnlyFlagHelp)
	adminSeedInboxCmd.Flags().String("workspace", "", "Workspace id to seed (required)")
	adminSeedInboxCmd.Flags().Bool("clear", false, "Delete the rows this command wrote, and nothing else")
	adminCmd.AddCommand(adminSeedInboxCmd)
}
