package backup_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestIssueMutationOutboxRestoreAndForkKeepDeliveryBoundaries(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	entry := journal.Entry{ID: "source-issue-journal", WorkspaceID: workspace, CrewID: "c_alpha", MissionID: "history-live", TS: time.Date(2026, 10, 14, 8, 0, 0, 123000000, time.UTC), Type: journal.EntryMissionStatus, Severity: journal.SeverityInfo, ActorType: journal.ActorUser, ActorID: "u_admin", Summary: "priority_changed: high", TraceID: "issue-activity:source-event", SpanID: "source-event", Payload: map[string]any{"action": "priority_changed", "details": "high"}, Refs: map[string]any{"mission_id": "history-live", "activity_id": "source-event", "seq": 1}}
	writer := journal.NewWriter(source, nil, journal.WriterOptions{})
	if _, _, err := writer.EmitReplaySync(t.Context(), entry); err != nil {
		writer.Close()
		t.Fatal(err)
	}
	writer.Close()
	for _, tc := range []struct {
		id        string
		different bool
	}{{"matching-audit", false}, {"different-audit", true}} {
		wanted := entry
		if tc.different {
			wanted.Summary = "Different immutable audit content"
		}
		raw, err := json.Marshal(map[string]any{"journal": wanted})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = source.Exec(`INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,kind,payload,state,lease_token,lease_expires_at,next_attempt_at,created_at) VALUES(?,?,'history-live','journal',?,'processing','old-process','2000-01-01T00:00:00.000Z','2000-01-01T00:00:00.000Z','2026-10-14T08:00:00.000Z')`, tc.id, workspace, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	const privateBody = "PRIVATE-COMMENT-BODY-NOT-IN-CUSTOM-BUNDLE"
	mentionPayload, _ := json.Marshal(map[string]any{"mention": map[string]any{"CommentBody": privateBody}})
	if _, err := source.Exec(`INSERT INTO issue_mutation_outbox(id,workspace_id,mission_id,comment_id,kind,payload,next_attempt_at,created_at) VALUES('pending-private-mention',?,'history-live','origin-comment','mention',?,'2000-01-01T00:00:00.000Z','2026-10-14T08:00:00.000Z')`, workspace, string(mentionPayload)); err != nil {
		t.Fatal(err)
	}
	// The shared fixture includes an unsigned sequence-zero legacy journal row.
	// Preserve its existing verification verdict; recovery must neither rewrite
	// that history nor alter the newly signed outbox projection's HMAC bytes.
	sourceChain, err := journal.VerifyChain(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	type signedReceipt struct {
		Seq           int64
		Previous, MAC string
	}
	var sourceSigned signedReceipt
	if err = source.QueryRow(`SELECT seq,COALESCE(prev_hash,''),COALESCE(entry_hash,'') FROM journal_entries WHERE id=?`, entry.ID).Scan(&sourceSigned.Seq, &sourceSigned.Previous, &sourceSigned.MAC); err != nil || sourceSigned.Seq < 1 || sourceSigned.MAC == "" {
		t.Fatalf("source signed receipt missing: %+v %v", sourceSigned, err)
	}

	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	target := openMigratedDB(t)
	if err = backup.RestoreDump(t.Context(), target, dump); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ id, state string }{{"matching-audit", "done"}, {"different-audit", "processing"}, {"pending-private-mention", "pending"}} {
		var state string
		if err = target.QueryRow(`SELECT state FROM issue_mutation_outbox WHERE id=?`, tc.id).Scan(&state); err != nil || state != tc.state {
			t.Fatalf("restored delivery %s state=%s expected=%s: %v", tc.id, state, tc.state, err)
		}
	}
	var compact string
	if err = target.QueryRow(`SELECT payload FROM issue_mutation_outbox WHERE id='matching-audit'`).Scan(&compact); err != nil || compact != "{}" {
		t.Fatalf("restored completed audit retained payload: %q %v", compact, err)
	}
	chain, err := journal.VerifyChain(t.Context(), target, workspace)
	if err != nil || !reflect.DeepEqual(chain, sourceChain) {
		t.Fatalf("restored audit verification changed: source=%+v restored=%+v err=%v", sourceChain, chain, err)
	}
	var restoredSigned signedReceipt
	if err = target.QueryRow(`SELECT seq,COALESCE(prev_hash,''),COALESCE(entry_hash,'') FROM journal_entries WHERE id=?`, entry.ID).Scan(&restoredSigned.Seq, &restoredSigned.Previous, &restoredSigned.MAC); err != nil || restoredSigned != sourceSigned {
		t.Fatalf("restored signed receipt changed: source=%+v restored=%+v err=%v", sourceSigned, restoredSigned, err)
	}

	if err = backup.RemapIDs(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	dump.Tables["workspaces"][0]["slug"] = "outbox-fork"
	if err = backup.RestoreDump(t.Context(), source, dump); err != nil {
		t.Fatal(err)
	}
	forkWorkspace := dump.Tables["workspaces"][0]["id"].(string)
	var active int
	if err = source.QueryRow(`SELECT COUNT(*) FROM issue_mutation_outbox WHERE workspace_id=? AND (state<>'done' OR payload<>'{}' OR lease_token IS NOT NULL OR lease_expires_at IS NOT NULL)`, forkWorkspace).Scan(&active); err != nil || active != 0 {
		t.Fatalf("fork replays source effects: %d %v", active, err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "synthetic-custom-outbox-backup-passphrase"
	created, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase, Categories: []string{backup.CategoryRoutines, backup.CategoryAgents}})
	if err != nil {
		t.Fatal(err)
	}
	filtered := openMigratedDB(t)
	if _, err = backup.RestoreBackup(t.Context(), filtered, backup.RestoreOptions{Path: created.Path, Passphrase: passphrase, Actor: actor}); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"issue_mutation_outbox", "mission_comments"} {
		var count int
		if err = filtered.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("custom bundle exported excluded comment history via %s: %d %v", table, count, err)
		}
	}
}
