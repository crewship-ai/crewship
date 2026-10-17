package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func repeatedCustodyFixture(t *testing.T, kind string) (*instanceFixture, *journal.Writer) {
	t.Helper()
	f := custodyRouterFixture(t, kind)
	f.r.Shutdown()
	writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = writer.Close() })
	var err error
	f.r, err = NewRouter(f.db, "this-is-a-32-char-test-secret-pad", newTestLogger(), WithJournal(writer))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.r.Shutdown)
	return f, writer
}

func TestOutputCustodyRepeatedTransfersReopenAdminNotice(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		t.Run(kind, func(t *testing.T) {
			f, writer := repeatedCustodyFixture(t, kind)
			mustExec(t, f.db, `UPDATE pages SET slug='private-custody-slug-canary',name='Private custody title canary' WHERE id='private-output'`)
			// A former MEMBER owner needs independently valid read authority to
			// become a successor again; membership alone is deliberately insufficient.
			mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('private-output','user','departing','read','wsadmin')`)
			protected := []string{"page_grants", "page_versions", "page_artifacts", "page_artifact_revisions", "page_boards", "page_board_elements", "workspace_members", "crew_members"}
			before := custodySnapshot(t, f.db, protected...)
			member := mintTokenFor(t, f.db, "ungranted", "repeatcustodymember00000000000000")
			path := "/api/v1/workspaces/ws-old/page-ownership/private-output"
			owners := []string{"departing", "reader", "boss", "departing"}
			noticeID := ""
			for i := 1; i < len(owners); i++ {
				body := fmt.Sprintf(`{"expected_owner_user_id":%q,"successor_user_id":%q}`, owners[i-1], owners[i])
				response := f.do(f.wsAdmin, "POST", path, body)
				wantCode(t, response, 200, fmt.Sprintf("explicit transfer %d (%s -> %s)", i, owners[i-1], owners[i]))
				var page pageOwnershipSummary
				if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.OwnerUserID != owners[i] {
					t.Fatalf("committed custody response: %+v %v", page, err)
				}
				var id, storedOwner string
				var count int
				if err := f.db.QueryRow(`SELECT COUNT(*),MIN(id) FROM inbox_items WHERE workspace_id='ws-old' AND kind=? AND source_id='private-output'`, pagesTransferInboxKind).Scan(&count, &id); err != nil || count != 1 {
					t.Fatalf("expected one refreshed administrator card: %d %v", count, err)
				}
				if noticeID != "" && id != noticeID {
					t.Fatal("repeated transfer replaced the existing notice identity")
				}
				noticeID = id
				if err := f.db.QueryRow(`SELECT owner_user_id FROM pages WHERE id='private-output'`).Scan(&storedOwner); err != nil || storedOwner != owners[i] {
					t.Fatalf("committed custody owner: %s %v", storedOwner, err)
				}
				inboxPath := "/api/v1/inbox/" + noticeID + "?workspace_id=ws-old"
				for _, token := range []string{f.wsAdmin, f.boss} {
					card := f.do(token, "GET", inboxPath, "")
					wantCode(t, card, 200, "administrator reads refreshed notice")
					var item inboxItemResponse
					if err := json.Unmarshal(card.Body.Bytes(), &item); err != nil {
						t.Fatal(err)
					}
					if item.State != "unread" || item.TargetRole != "ADMIN" || item.ResolvedAt != "" || item.ReadAt != "" || item.ResolvedByUserID != "" || item.ResolvedAction != "" || item.Payload["from_user_id"] != owners[i-1] || item.Payload["to_user_id"] != owners[i] || item.Payload["actor_user_id"] != "wsadmin" || item.Payload["reason"] != "explicit_human_transfer" {
						t.Fatalf("transfer did not publish fresh administrator news: %+v", item)
					}
				}
				wantCode(t, f.do(member, "GET", inboxPath, ""), 404, "ungranted member cannot read admin custody notice")
				var reads int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id=?`, noticeID).Scan(&reads); err != nil || reads != 0 {
					t.Fatalf("prior per-user read markers survived new transfer: %d %v", reads, err)
				}
				if after := custodySnapshot(t, f.db, protected...); after != before {
					t.Fatal("repeated custody changed source, scene, history, grants or memberships")
				}
				if i < len(owners)-1 {
					for _, token := range []string{f.wsAdmin, f.boss} {
						wantCode(t, f.do(token, "PATCH", inboxPath, `{"state":"read"}`), 200, "administrator reads prior transfer")
					}
					if err := f.db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id=?`, noticeID).Scan(&reads); err != nil || reads != 2 {
						t.Fatalf("real Inbox routes did not record both admin reads: %d %v", reads, err)
					}
					wantCode(t, f.do(f.wsAdmin, "PATCH", inboxPath, `{"state":"resolved","resolved_action":"acknowledged"}`), 200, "resolve prior transfer notice")
				}
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			audit := f.do(member, "GET", "/api/v1/journal?workspace_id=ws-old&entry_type=page.owner_transferred", "")
			wantCode(t, audit, 200, "read immutable per-transfer audit")
			var doc struct {
				Entries []struct {
					Payload map[string]any `json:"payload"`
				} `json:"entries"`
			}
			if err := json.Unmarshal(audit.Body.Bytes(), &doc); err != nil || len(doc.Entries) != 3 {
				t.Fatalf("one refreshed card must retain three committed journal events: %d %v", len(doc.Entries), err)
			}
			seen := map[string]bool{}
			for _, entry := range doc.Entries {
				seen[fmt.Sprint(entry.Payload["from_user_id"], "->", entry.Payload["to_user_id"])] = true
			}
			for i := 1; i < len(owners); i++ {
				if !seen[owners[i-1]+"->"+owners[i]] {
					t.Fatalf("journal lost committed custody history: %v", seen)
				}
			}
			for _, secret := range []string{"private-custody-slug-canary", "Private custody title canary", "NEVER_EXPOSE_THIS", "page_slug", "page_name"} {
				if strings.Contains(audit.Body.String(), secret) {
					t.Fatalf("repeated transfer leaked private metadata through journal: %s", secret)
				}
			}
		})
	}
}

func TestOutputCustodyRepeatedNoticeFailureRollsBackEverything(t *testing.T) {
	for _, kind := range []string{"artifact", "board"} {
		for _, failure := range []string{"notice-update", "read-markers"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				f, writer := repeatedCustodyFixture(t, kind)
				path := "/api/v1/workspaces/ws-old/page-ownership/private-output"
				wantCode(t, f.do(f.wsAdmin, "POST", path, `{"expected_owner_user_id":"departing","successor_user_id":"reader"}`), 200, "first transfer")
				var id string
				if err := f.db.QueryRow(`SELECT id FROM inbox_items WHERE workspace_id='ws-old' AND source_id='private-output'`).Scan(&id); err != nil {
					t.Fatal(err)
				}
				wantCode(t, f.do(f.wsAdmin, "PATCH", "/api/v1/inbox/"+id+"?workspace_id=ws-old", `{"state":"read"}`), 200, "read first notice")
				if err := writer.Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
				trigger := `CREATE TRIGGER reject_repeat_notice BEFORE UPDATE ON inbox_items WHEN OLD.source_id='private-output' BEGIN SELECT RAISE(ABORT,'notice update unavailable'); END`
				if failure == "read-markers" {
					trigger = `CREATE TRIGGER reject_repeat_notice BEFORE DELETE ON inbox_item_reads BEGIN SELECT RAISE(ABORT,'read markers unavailable'); END`
				}
				mustExec(t, f.db, trigger)
				tables := append(append([]string{}, custodyProtectedTables...), "inbox_item_reads", "journal_entries")
				before := custodySnapshot(t, f.db, tables...)
				body := `{"expected_owner_user_id":"reader","successor_user_id":"boss"}`
				wantCode(t, f.do(f.wsAdmin, "POST", path, body), 500, "failed repeated notice must reject transfer")
				if err := writer.Flush(t.Context()); err != nil {
					t.Fatal(err)
				}
				if after := custodySnapshot(t, f.db, tables...); after != before {
					t.Fatal("failed notice update/read reset committed partial custody, notice or journal")
				}
				mustExec(t, f.db, `DROP TRIGGER reject_repeat_notice`)
				wantCode(t, f.do(f.wsAdmin, "POST", path, body), 200, "same transfer succeeds after storage recovers")
			})
		}
	}
}
