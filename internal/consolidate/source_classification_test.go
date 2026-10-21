package consolidate

import (
	"fmt"
	"github.com/crewship-ai/crewship/internal/journal"
	"testing"
	"time"
)

func TestCompactorNeverExpiresSourceClassificationMarkers(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	writer := journal.NewWriter(db, quietLogger(), journal.WriterOptions{FlushSize: 1})
	defer writer.Close()
	old := time.Now().UTC().Add(-90 * 24 * time.Hour)
	for i := 0; i < 12; i++ {
		for _, kind := range []journal.EntryType{journal.EntryEscalationSourceClassified, journal.EntryExecOutputChunk} {
			if _, err := writer.Emit(t.Context(), journal.Entry{WorkspaceID: "ws_test", Type: kind, ActorType: journal.ActorSystem, TS: old, Summary: "Source classification retention test", Payload: map[string]any{}, Refs: map[string]any{"escalation_id": fmt.Sprint("source-", i)}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Flush(t.Context()); err != nil {
		t.Fatal(err)
	}
	compact := Compactor{DB: db, Journal: writer, Logger: quietLogger()}
	if _, err := compact.Run(t.Context(), "ws_test", 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws_test' AND entry_type='escalation.source_classified'`).Scan(&count); err != nil || count != 12 {
		t.Fatalf("source classification expired%d %v", count, err)
	}
	var chunks int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='ws_test' AND entry_type='exec.output_chunk'`).Scan(&chunks); err != nil || chunks != 0 {
		t.Fatalf("compaction positive control%d %v", chunks, err)
	}
}
