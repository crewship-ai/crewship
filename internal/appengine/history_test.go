package appengine

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
)

// Live editing commits one confirmed cell at a time. History stores a full
// snapshot only at checkpoints and imports, plus each canonical command, so a
// workbook outlives far more than MaxRevisions cell commits.
func TestCellCommitsStoreCommandsAndCheckpointsWithinQuota(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	const commits = 1200
	for i := 0; i < commits; i++ {
		req := edit()
		req.MutationID = fmt.Sprintf("edit-%d", i)
		req.BaseRev = int64(i + 1)
		if _, err := s.Execute(t.Context(), k, req, a, e); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	head, err := s.Read(t.Context(), k, a)
	if err != nil || head.Rev != commits+1 || string(head.Snapshot) != `{"sheets":[{"B2":"42"}]}` {
		t.Fatalf("head=%+v err=%v", head, err)
	}
	var checkpoints int
	if err = db.QueryRow(`SELECT count(*) FROM gridcraft_revisions WHERE page_id=?`, k.PageID).Scan(&checkpoints); err != nil {
		t.Fatal(err)
	}
	// The import (rev 1) plus every 50th revision up to 1201.
	if checkpoints != 1+commits/CheckpointInterval {
		t.Fatalf("checkpoints=%d", checkpoints)
	}
	var kind, writer string
	var command []byte
	if err = db.QueryRow(`SELECT kind,writer,command FROM gridcraft_audit WHERE page_id=? AND rev=2`, k.PageID).Scan(&kind, &writer, &command); err != nil {
		t.Fatal(err)
	}
	if kind != "cells" || writer != "user:owner" || string(command) != `{"kind":"cells","cells":[{"sheet":"Sheet1","cell":"B2","input":"42"}]}` {
		t.Fatalf("audit kind=%s writer=%s command=%s", kind, writer, command)
	}
	if err = db.QueryRow(`SELECT kind,command FROM gridcraft_audit WHERE page_id=? AND rev=1`, k.PageID).Scan(&kind, &command); err != nil {
		t.Fatal(err)
	}
	// The imported snapshot lives in its checkpoint, never twice in the audit.
	if kind != "snapshot" || string(command) != `{"kind":"snapshot"}` {
		t.Fatalf("import audit kind=%s command=%s", kind, command)
	}
}

// The quota counts checkpoint snapshots and recorded commands, so history
// remains bounded even when no checkpoint is written.
func TestHistoryQuotaCountsCommandsAndCheckpoints(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	s.options.MaxHistoryBytes = 1 // already exceeded by the import checkpoint
	if _, err := s.Execute(t.Context(), k, edit(), a, e); !errors.Is(err, ErrQuota) {
		t.Fatalf("quota: %v", err)
	}
}

// Rows written before command history existed carry no command. They are
// marked as snapshot revisions with their authority as writer, so no later
// commit can be rebased across history whose cells are unknown.
func TestCommandHistoryMigrationBackfillsExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err = database.MigrateSkipping(t.Context(), db.DB, logger, "gridcraft_command_history"); err != nil {
		if !strings.Contains(err.Error(), `no migration is named "gridcraft_command_history"`) {
			t.Fatal(err)
		}
		if err = database.Migrate(t.Context(), db.DB, logger); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('owner','owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','legacy-history')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('page','ws','book','Book','owner',1,'{}')`,
		`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('page','ws',1,2,1,'{"sheets":[]}')`,
		`INSERT INTO gridcraft_revisions(page_id,rev,snapshot) VALUES('page',1,'{"sheets":[]}'),('page',2,'{"sheets":[]}')`,
		`INSERT INTO gridcraft_audit(page_id,rev,authority_id,mutation_id,mandate_id,base_rev,receipt) VALUES('page',1,'user:owner','import-1','human-import',0,'{}'),('page',2,'agent:a:run:r:mandate:m','edit-1','m',1,'{}')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err = database.Migrate(t.Context(), db.DB, logger); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT rev,kind,writer,command FROM gridcraft_audit WHERE page_id='page' ORDER BY rev`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []string{"1 snapshot user:owner", "2 snapshot agent:a:run:r:mandate:m"}
	var got []string
	for rows.Next() {
		var rev int
		var kind, writer string
		var command sql.NullString
		if err = rows.Scan(&rev, &kind, &writer, &command); err != nil {
			t.Fatal(err)
		}
		if command.Valid {
			t.Fatalf("legacy rev %d gained a command %q", rev, command.String)
		}
		got = append(got, fmt.Sprintf("%d %s %s", rev, kind, writer))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("backfill %v", got)
	}
	var revisions int
	if err = db.QueryRow(`SELECT count(*) FROM gridcraft_revisions`).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatalf("legacy revisions %d %v", revisions, err)
	}
	if _, err = db.Exec(`UPDATE gridcraft_audit SET kind='cells' WHERE rev=1`); err == nil {
		// A cells row must carry its command; NULL marks unknown history.
		t.Fatal("cells audit row without command accepted")
	}
	if _, err = db.Exec(`UPDATE gridcraft_audit SET command='not json' WHERE rev=1`); err == nil {
		t.Fatal("non-JSON command accepted")
	}
}
