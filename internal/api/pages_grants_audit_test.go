package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Synchronous native persistence makes fault evidence deterministic on the old
// best-effort Emit path too. The transactional method remains the real writer.
type pageGrantAuditEmitter struct{ *journal.Writer }

func (e pageGrantAuditEmitter) Emit(ctx context.Context, entry journal.Entry) (string, error) {
	return e.EmitSync(ctx, entry)
}

type pageGrantAuditFixture struct {
	h               *PageHandler
	ws, actor, page string
	key             []byte
	published       atomic.Int64
}

func newPageGrantAuditFixture(t *testing.T) *pageGrantAuditFixture {
	t.Helper()
	h, _, ws, actor, page := pagesGrantFixture(t, "")
	f := &pageGrantAuditFixture{h: h, ws: ws, actor: actor, page: page, key: []byte("page-grant-configured-test-chain")}
	writer := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{ChainKey: f.key, FlushInterval: time.Hour})
	t.Cleanup(func() { writer.Close() })
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: ws, Type: journal.EntryPageSpecChanged, ActorType: journal.ActorUser, ActorID: actor, Summary: "Native chain calibration"}); err != nil {
		t.Fatal(err)
	}
	writer.AddCommitObserver(func([]journal.Entry) { f.published.Add(1) })
	h.SetJournal(pageGrantAuditEmitter{writer})
	f.chain(t)
	return f
}
func (f *pageGrantAuditFixture) chain(t *testing.T) {
	t.Helper()
	result, err := journal.VerifyChainWithKey(t.Context(), f.h.db, f.ws, f.key)
	if err != nil || !result.OK {
		t.Fatalf("configured chain: %+v error=%v", result, err)
	}
}
func (f *pageGrantAuditFixture) call(t *testing.T, method, level string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/pages/fleet-201/grants?subject_type=crew&subject=engine"
	if level != "" {
		target += "&level=" + level
	}
	body := fmt.Sprintf(`{"subject_type":"crew","subject":"engine","level":%q}`, level)
	return pagesGrantCall(t, f.h, method, target, f.ws, f.actor, "OWNER", "fleet-201", body)
}
func (f *pageGrantAuditFixture) count(t *testing.T, table, where string) int {
	t.Helper()
	var n int
	if err := f.h.db.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE " + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func (f *pageGrantAuditFixture) seed(t *testing.T, levels ...string) {
	t.Helper()
	for _, level := range levels {
		if rr := f.call(t, "PUT", level); rr.Code != 200 {
			t.Fatalf("healthy grant status=%d", rr.Code)
		}
	}
	f.chain(t)
	f.published.Store(0)
}

func TestPageGrantAuditSnapshots(t *testing.T) {
	f := newPageGrantAuditFixture(t)
	f.seed(t, "read", "produce", "write")
	// Reissue still creates one fact and re-anchors the actual stored row.
	if rr := f.call(t, "PUT", "read"); rr.Code != 200 {
		t.Fatalf("reissue status=%d", rr.Code)
	}
	var raw string
	if err := f.h.db.QueryRow(`SELECT payload FROM journal_entries WHERE entry_type='page.grant_added' ORDER BY seq DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"previous_grant", "grant"} {
		var row map[string]any
		if err := json.Unmarshal(payload[key], &row); err != nil || row["granted_by_user_id"] != f.actor || row["level"] != "read" {
			t.Errorf("reissue missing full %s snapshot: %v", key, err)
		}
	}
	// A departed crew remains revocable by its stored ID, at every level.
	if _, err := f.h.db.Exec(`UPDATE crews SET deleted_at='2026-10-19' WHERE id='crew-engine'`); err != nil {
		t.Fatal(err)
	}
	rr := pagesGrantCall(t, f.h, "DELETE", "/grants?subject_type=crew&subject=crew-engine", f.ws, f.actor, "OWNER", "fleet-201", "")
	if rr.Code != 200 {
		t.Fatalf("departed multi-level revoke status=%d", rr.Code)
	}
	if n := f.count(t, "page_grants", "subject_id='crew-engine'"); n != 0 {
		t.Fatalf("surviving grants=%d", n)
	}
	if n := f.count(t, "journal_entries", "entry_type='page.grant_removed'"); n != 3 {
		t.Fatalf("removed events=%d", n)
	}
	rows, err := f.h.db.Query(`SELECT payload FROM journal_entries WHERE entry_type='page.grant_removed'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var event map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			t.Fatal(err)
		}
		if string(event["grant"]) != "null" || len(event["previous_grant"]) == 0 {
			t.Error("revoke missing complete removed snapshot and null resulting grant")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	before := f.published.Load()
	if rr := f.call(t, "DELETE", ""); rr.Code != 404 {
		t.Fatalf("absent revoke status=%d", rr.Code)
	}
	if f.published.Load() != before {
		t.Error("absent revoke published an event")
	}
	f.chain(t)
}

func TestPageGrantAuditRollback(t *testing.T) {
	for _, door := range []string{"create", "reissue", "remove"} {
		for _, fault := range []string{"abort", "ignore", "undo", "erase_event", "deferred_commit", "summary", "actor_type", "earlier_event"} {
			if fault == "earlier_event" && door != "remove" {
				continue
			}
			t.Run(door+"/"+fault, func(t *testing.T) {
				f := newPageGrantAuditFixture(t)
				expected := 0
				if door != "create" {
					f.seed(t, "read", "produce", "write")
					expected = 3
				}
				beforeEvents := f.count(t, "journal_entries", "entry_type IN ('page.grant_added','page.grant_removed')")
				var before string
				if err := f.h.db.QueryRow(`SELECT COALESCE(json_group_array(json_array(level,panel_ids,granted_by_user_id,granted_at)),'[]') FROM (SELECT * FROM page_grants ORDER BY level)`).Scan(&before); err != nil {
					t.Fatal(err)
				}
				action := "SELECT RAISE(ABORT,'synthetic audit refusal');"
				timing := "BEFORE"
				switch fault {
				case "ignore":
					action = "SELECT RAISE(IGNORE);"
				case "undo":
					timing = "AFTER"
					action = "DELETE FROM page_grants WHERE page_id='" + f.page + "';"
					if door == "remove" {
						action = fmt.Sprintf(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('%s','crew','crew-engine','read','%s');`, f.page, f.actor)
					}
				case "summary", "actor_type":
					timing = "AFTER"
					value := "tampered summary"
					if fault == "actor_type" {
						value = "system"
					}
					action = "UPDATE journal_entries SET " + fault + "='" + value + "' WHERE id=NEW.id;"
				case "earlier_event":
					timing = "AFTER"
					action = `UPDATE journal_entries SET summary='changed by later append' WHERE workspace_id=NEW.workspace_id AND entry_type='page.grant_removed' AND json_extract(payload,'$.level')='produce';`
				case "erase_event":
					timing = "AFTER"
					action = "DELETE FROM journal_entries WHERE id=NEW.id;"
				case "deferred_commit":
					if _, err := f.h.db.Exec(`CREATE TABLE grant_audit_parent(id INTEGER PRIMARY KEY); CREATE TABLE grant_audit_child(id INTEGER REFERENCES grant_audit_parent(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
						t.Fatal(err)
					}
					timing = "AFTER"
					action = "INSERT INTO grant_audit_child VALUES(1);"
				}
				// On multi-level revoke, fail the second audit after the first succeeded.
				condition := "NEW.entry_type IN ('page.grant_added','page.grant_removed')"
				if door == "remove" && (fault == "abort" || fault == "ignore" || fault == "earlier_event") {
					condition += " AND json_extract(NEW.payload,'$.level')='read'"
				}
				if _, err := f.h.db.Exec("CREATE TRIGGER grant_audit_fault " + timing + " INSERT ON journal_entries WHEN " + condition + " BEGIN " + action + " END"); err != nil {
					t.Fatal(err)
				}
				method := "PUT"
				level := "read"
				if door == "remove" {
					method = "DELETE"
					level = ""
				}
				rr := f.call(t, method, level)
				if rr.Code != 500 {
					t.Errorf("audit fault committed mutation: status=%d, want 500", rr.Code)
				}
				if n := f.count(t, "page_grants", "1=1"); n != expected {
					t.Errorf("grant rollback count=%d want=%d", n, expected)
				}
				var after string
				if err := f.h.db.QueryRow(`SELECT COALESCE(json_group_array(json_array(level,panel_ids,granted_by_user_id,granted_at)),'[]') FROM (SELECT * FROM page_grants ORDER BY level)`).Scan(&after); err != nil {
					t.Fatal(err)
				}
				if after != before {
					t.Error("grant tuple changed despite audit failure")
				}
				if n := f.count(t, "journal_entries", "entry_type IN ('page.grant_added','page.grant_removed')"); n != beforeEvents {
					t.Errorf("audit rollback count=%d want=%d", n, beforeEvents)
				}
				if f.published.Load() != 0 {
					t.Error("notification before successful business commit")
				}
				if _, err := f.h.db.Exec(`DROP TRIGGER grant_audit_fault`); err != nil {
					t.Fatal(err)
				}
				f.chain(t)
			})
		}
	}
}

func TestPageGrantAuditCurrentIssuer(t *testing.T) {
	for _, change := range []string{"stale_admin", "suspended", "restricted", "body_race", "sql_error"} {
		for _, method := range []string{"PUT", "DELETE"} {
			t.Run(change+"/"+method, func(t *testing.T) {
				f := newPageGrantAuditFixture(t)
				f.seed(t, "read")
				mutate := func() {
					var err error
					switch change {
					case "stale_admin", "body_race":
						// Remove actual ownership too; stale context OWNER must not authorize.
						_, err = f.h.db.Exec(`UPDATE pages SET owner_user_id=NULL,owner_crew_id='crew-engine' WHERE id=?`, f.page)
						if err == nil {
							_, err = f.h.db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE user_id=?`, f.actor)
						}
					case "suspended":
						_, err = f.h.db.Exec(`UPDATE users SET suspended_at='2026-10-19' WHERE id=?`, f.actor)
					case "restricted":
						_, err = f.h.db.Exec(`UPDATE workspace_members SET role='MEMBER',access_mode='restricted' WHERE user_id=?`, f.actor)
					case "sql_error":
						_, err = f.h.db.Exec(`ALTER TABLE workspace_members RENAME TO unavailable_members`)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				req := pagesGrantRequest(t, method, "/grants?subject_type=crew&subject=engine&level=read", f.ws, f.actor, "OWNER", `{"subject_type":"crew","subject":"engine","level":"read"}`)
				if change == "body_race" && method == "PUT" {
					req.Body = structReadCloser{&authorizationAuditBody{reader: strings.NewReader(`{"subject_type":"crew","subject":"engine","level":"read"}`), before: mutate}}
				} else {
					mutate()
				}
				req.SetPathValue("slug", "fleet-201")
				rr := httptest.NewRecorder()
				if method == "PUT" {
					f.h.PutGrant(rr, req)
				} else {
					f.h.DeleteGrant(rr, req)
				}
				want := 403
				if change == "sql_error" {
					want = 500
				}
				if rr.Code != want {
					t.Errorf("current issuer status=%d want=%d", rr.Code, want)
				}
				if n := f.count(t, "page_grants", "1=1"); n != 1 {
					t.Error("unauthorized mutation changed grants")
				}
				if f.published.Load() != 0 {
					t.Error("unauthorized mutation notified")
				}
			})
		}
	}
}

type structReadCloser struct{ *authorizationAuditBody }

func (structReadCloser) Close() error { return nil }

func TestPageGrantAuditRejectsReplacementPage(t *testing.T) {
	f := newPageGrantAuditFixture(t)
	f.seed(t, "read")
	// Copy the actual native Page row, not an invented reduced schema. A later
	// audit trigger must not replace the target and pass the final slug lookup.
	if _, err := f.h.db.Exec(`CREATE TABLE replacement_page AS SELECT * FROM pages WHERE id=?;
UPDATE replacement_page SET id='replacement-page'`, f.page); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.db.Exec(`CREATE TRIGGER replace_page AFTER INSERT ON journal_entries WHEN NEW.entry_type='page.grant_removed' BEGIN DELETE FROM pages WHERE id='` + f.page + `'; INSERT INTO pages SELECT * FROM replacement_page; END`); err != nil {
		t.Fatal(err)
	}
	if rr := f.call(t, "DELETE", "read"); rr.Code != 500 {
		t.Errorf("replacement target committed: status=%d", rr.Code)
	}
	var old, replacement, panels int
	if err := f.h.db.QueryRow(`SELECT (SELECT COUNT(*) FROM pages WHERE id=?),(SELECT COUNT(*) FROM pages WHERE id='replacement-page'),(SELECT COUNT(*) FROM page_panels WHERE page_id=?)`, f.page, f.page).Scan(&old, &replacement, &panels); err != nil {
		t.Fatal(err)
	}
	if old != 1 || replacement != 0 || panels != 2 || f.published.Load() != 0 {
		t.Errorf("target replacement escaped rollback: old=%d replacement=%d panels=%d notify=%d", old, replacement, panels, f.published.Load())
	}
}
