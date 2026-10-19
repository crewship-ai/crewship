package backup_test

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

const boardBackupFormat = "crewship-board-excalidraw-0.18.1-v1"
const boardBackupMutation = "original-board-init"

func seedBackupBoard(t *testing.T, db *sql.DB, workspace, page, owner, author string) string {
	t.Helper()
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte("init\n{\"mutation_id\":\""+boardBackupMutation+"\"}")))
	for _, query := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES(?,?,?,?,?,1,'{}')`, []any{page, workspace, page, "Private Board", owner}},
		{`INSERT INTO page_boards(page_id,seq,format) VALUES(?,0,?)`, []any{page, boardBackupFormat}},
		{`INSERT INTO page_board_mutations(board_page_id,mutation_id,author_user_id,fingerprint,result_seq,created_at) VALUES(?,?,?,?,0,'2026-10-19T20:00:00.000Z')`, []any{page, boardBackupMutation, author, fingerprint}},
	} {
		if _, err := db.Exec(query.sql, query.args...); err != nil {
			t.Fatal(err)
		}
	}
	return fingerprint
}

// Real migrated-schema discovery and scoped dump must remain usable even when
// Board routes are disabled. Board receipts belong to their Page, not all Pages.
func TestBoardBackupDiscoveryAndWorkspaceScope(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('foreign-board-workspace','Foreign','foreign-board-workspace')`); err != nil {
		t.Fatal(err)
	}
	seedBackupBoard(t, source, workspace, "private-board", "u_admin", "u_admin")
	seedBackupBoard(t, source, "foreign-board-workspace", "foreign-board", "u_admin", "u_admin")
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatalf("Board migration breaks the existing scoped dump: %v", err)
	}
	scoped, err := backup.DiscoverScopedTables(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	include, _, err := backup.CategoriseScopedTables(scoped, backup.BackupTableIntent)
	if err != nil {
		t.Fatal(err)
	}
	included := map[string]bool{}
	for _, table := range include {
		included[table.Name] = true
	}
	for table, column := range map[string]string{"page_boards": "page_id", "page_board_mutations": "board_page_id"} {
		if !included[table] {
			t.Errorf("%s is not included by discovered backup intent", table)
		}
		if backup.TableCategory[table] != backup.CategoryPages {
			t.Errorf("%s is not carried by the Pages category", table)
		}
		rows := dump.Tables[table]
		if len(rows) != 1 || rows[0][column] != "private-board" {
			t.Fatalf("%s leaked or lost workspace-scoped Board rows: %+v", table, rows)
		}
	}
}

// T-BAK-02 subset: the actual encrypted Pages bundle must preserve private Board
// custody, format/sequence and immutable mutation identity in same/fork restore.
// This synthetic fixture is not an RPO/RTO or live restore qualification.
func TestBoardEncryptedBackupSameAndForkRestore(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedWorkspace(t, source)
	if _, err := source.Exec(`INSERT INTO users(id,email) VALUES('board-historical-author','board-author@example.test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('foreign-board-workspace','Foreign','foreign-board-workspace')`); err != nil {
		t.Fatal(err)
	}
	fingerprint := seedBackupBoard(t, source, workspace, "private-board", "u_admin", "board-historical-author")
	seedBackupBoard(t, source, "foreign-board-workspace", "foreign-board", "u_admin", "u_admin")
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	const passphrase = "synthetic-board-roundtrip"
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: workspace, OutputDir: t.TempDir(), Passphrase: passphrase, Actor: actor, Categories: []string{backup.CategoryPages}})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: passphrase, Actor: actor}
			if fork {
				options.AsWorkspace = "board-fork"
			}
			result, err := backup.RestoreBackup(t.Context(), target, options)
			if err != nil {
				t.Fatal(err)
			}
			var page, restoredWorkspace, owner, format, mutation, author, storedFingerprint, created string
			var private, seq, resultSeq int64
			err = target.QueryRow(`SELECT p.id,p.workspace_id,p.owner_user_id,p.private_audience,b.format,b.seq,m.mutation_id,m.author_user_id,m.fingerprint,m.result_seq,m.created_at FROM page_boards b JOIN pages p ON p.id=b.page_id JOIN page_board_mutations m ON m.board_page_id=b.page_id`).Scan(&page, &restoredWorkspace, &owner, &private, &format, &seq, &mutation, &author, &storedFingerprint, &resultSeq, &created)
			if err != nil {
				t.Fatal(err)
			}
			if restoredWorkspace != result.RestoredWorkspaceID || owner != "u_admin" || private != 1 || format != boardBackupFormat || seq != 0 || mutation != boardBackupMutation || author != "board-historical-author" || storedFingerprint != fingerprint || resultSeq != 0 || created != "2026-10-19T20:00:00.000Z" {
				t.Fatalf("Board custody/state/receipt changed: page=%s workspace=%s owner=%s private=%d format=%s seq=%d mutation=%s author=%s fingerprint=%s result=%d created=%s", page, restoredWorkspace, owner, private, format, seq, mutation, author, storedFingerprint, resultSeq, created)
			}
			if fork && (page == "private-board" || restoredWorkspace == workspace) {
				t.Fatal("fork retained source Page/workspace anchors")
			}
			if !fork && (page != "private-board" || restoredWorkspace != workspace) {
				t.Fatal("same-workspace restore changed Board anchors")
			}
			for _, table := range []string{"page_boards", "page_board_mutations"} {
				var count int
				if err := target.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
					t.Fatalf("%s foreign Board leaked: count=%d err=%v", table, count, err)
				}
			}
			rows, err := target.Query(`PRAGMA foreign_key_check`)
			if err != nil {
				t.Fatal(err)
			}
			violated := rows.Next()
			if err := rows.Err(); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			rows.Close()
			if violated {
				t.Fatal("Board restore left a broken foreign key")
			}
			if _, err := target.Exec(`UPDATE pages SET private_audience=0 WHERE id=?`, page); err == nil {
				t.Fatal("restored Board lost private custody constraint")
			}
			if _, err := target.Exec(`DELETE FROM users WHERE id='board-historical-author'`); err != nil {
				t.Fatal(err)
			}
			var remainingAuthor sql.NullString
			if err := target.QueryRow(`SELECT author_user_id FROM page_board_mutations WHERE board_page_id=? AND mutation_id=?`, page, boardBackupMutation).Scan(&remainingAuthor); err != nil || remainingAuthor.Valid {
				t.Fatalf("historical author FK was not preserved: %+v %v", remainingAuthor, err)
			}
		})
	}
	var foreign int
	if err := source.QueryRow(`SELECT count(*) FROM page_boards WHERE page_id='foreign-board'`).Scan(&foreign); err != nil || foreign != 1 {
		t.Fatal("backup/restore changed source foreign Board")
	}
}
