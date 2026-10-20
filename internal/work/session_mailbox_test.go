package work

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

func mailboxRequest() SessionMessageRequest {
	return SessionMessageRequest{WorkspaceID: "ws1", UserID: "human", SessionID: "chat", IntentKey: "intent-one", AgentID: "agent", CrewID: "crew", Input: SessionMessageInput{Body: "private instruction", AttachmentsJSON: []byte(`[]`), IntentJSON: []byte(`{"page":"page-one","rev":1}`), TargetAgentID: "explicit-agent"}, AuthorityJSON: []byte(`{"captured":"original"}`)}
}
func mailboxAccept(s *Store, db *sql.DB, req SessionMessageRequest, hook SessionMessageHook) (SessionMessageReceipt, error) {
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	defer tx.Rollback()
	receipt, err := s.AcceptSessionMessageTx(ctx, tx, req, hook)
	if err != nil {
		return SessionMessageReceipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return SessionMessageReceipt{}, err
	}
	return receipt, nil
}
func allowMailbox(context.Context, *sql.Tx, SessionMessageAdmission) error { return nil }

func TestSessionMailboxLostACKReturnsOriginalAuthorityAndIdentity(t *testing.T) {
	s, db, clock := newTestStore(t)
	req := mailboxRequest()
	first, err := mailboxAccept(s, db, req, allowMailbox)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Hour)
	req.AuthorityJSON = []byte(`{"captured":"recaptured-not-a-renewal"}`)
	req.AgentID = "new-default-agent"
	req.CrewID = "new-default-crew"
	req.Input.IntentJSON = []byte(`{ "rev":1, "page":"page-one" }`)
	var seen SessionMessageAdmission
	retry, err := mailboxAccept(NewStore(db).WithClock(clock.Now), db, req, func(_ context.Context, _ *sql.Tx, a SessionMessageAdmission) error { seen = a; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !retry.Duplicate || first.MessageID != retry.MessageID || first.WorkID != retry.WorkID || first.Seq != retry.Seq || !first.AcceptedAt.Equal(retry.AcceptedAt) {
		t.Fatalf("lost ACK retry changed acceptance: %+v %+v", first, retry)
	}
	if string(seen.AuthorityJSON) != `{"captured":"original"}` || seen.MessageID != first.MessageID || !seen.Duplicate || seen.AgentID != "agent" || seen.CrewID != "crew" {
		t.Fatalf("retry renewed authority: %+v", seen)
	}
	for _, table := range []string{"session_mailbox", "work_items", "work_events"} {
		if n := countRows(t, db, table); n != 1 {
			t.Fatalf("%s=%d", table, n)
		}
	}
	var source, ref, author string
	if err = db.QueryRow(`SELECT source,source_ref,authorized_by_user_id FROM work_items WHERE id=?`, first.WorkID).Scan(&source, &ref, &author); err != nil {
		t.Fatal(err)
	}
	if source != string(SourceChat) || ref != first.MessageID || author != req.UserID {
		t.Fatal("work lost original chat/human binding")
	}
}
func TestSessionMailboxChangedRequestConflicts(t *testing.T) {
	for _, change := range []string{"body", "intent", "attachments", "target", "model", "effort"} {
		t.Run(change, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			req := mailboxRequest()
			if _, err := mailboxAccept(s, db, req, allowMailbox); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "body":
				req.Input.Body = "different"
			case "intent":
				req.Input.IntentJSON = []byte(`{"page":"page-two","rev":1}`)
			case "attachments":
				req.Input.AttachmentsJSON = []byte(`["different"]`)
			case "target":
				req.Input.TargetAgentID = "other-agent"
			case "model":
				req.Input.Model = "explicit-model"
			case "effort":
				req.Input.Effort = "high"
			}
			_, err := mailboxAccept(s, db, req, allowMailbox)
			if !errors.Is(err, ErrSessionMessageConflict) {
				t.Fatalf("changed request=%v", err)
			}
			if countRows(t, db, "session_mailbox") != 1 || countRows(t, db, "work_items") != 1 {
				t.Fatal("conflict wrote duplicate work")
			}
		})
	}
}
func TestSessionMailboxSealPrecedesPrivatePersistenceAndRollsBack(t *testing.T) {
	for _, failure := range []string{"none", "hook", "mailbox"} {
		t.Run(failure, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			for _, q := range []string{`CREATE TABLE mailbox_seal (id TEXT PRIMARY KEY)`, `CREATE TRIGGER require_seal_work BEFORE INSERT ON work_items WHEN NOT EXISTS(SELECT 1 FROM mailbox_seal) BEGIN SELECT RAISE(ABORT,'unsealed work'); END`, `CREATE TRIGGER require_seal_mailbox BEFORE INSERT ON session_mailbox WHEN NOT EXISTS(SELECT 1 FROM mailbox_seal) BEGIN SELECT RAISE(ABORT,'unsealed mailbox'); END`} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "mailbox" {
				if _, err := db.Exec(`CREATE TRIGGER fail_mailbox BEFORE INSERT ON session_mailbox BEGIN SELECT RAISE(ABORT,'mailbox failed'); END`); err != nil {
					t.Fatal(err)
				}
			}
			denied := errors.New("current authority denied")
			_, err := mailboxAccept(s, db, mailboxRequest(), func(ctx context.Context, tx *sql.Tx, a SessionMessageAdmission) error {
				if a.MessageID == "" || a.AcceptedAt.IsZero() || a.Duplicate {
					t.Fatal("missing original server acceptance")
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO mailbox_seal VALUES('sealed')`); err != nil {
					return err
				}
				if failure == "hook" {
					return denied
				}
				return nil
			})
			want := 1
			if failure != "none" {
				want = 0
				if err == nil {
					t.Fatal("failure committed")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			for _, table := range []string{"session_mailbox", "work_items", "work_events", "mailbox_seal"} {
				if n := countRows(t, db, table); n != want {
					t.Fatalf("partial %s=%d want%d", table, n, want)
				}
			}
		})
	}
}
func TestSessionMailboxRequiresHostHookAndValidInput(t *testing.T) {
	s, db, _ := newTestStore(t)
	req := mailboxRequest()
	if _, err := mailboxAccept(s, db, req, nil); err == nil {
		t.Fatal("missing live-authority/seal hook accepted")
	}
	req.Input.IntentJSON = []byte(`{"page":"first","page":"ambiguous"}`)
	if _, err := mailboxAccept(s, db, req, allowMailbox); err == nil {
		t.Fatal("ambiguous intent accepted")
	}
	if countRows(t, db, "session_mailbox") != 0 || countRows(t, db, "work_items") != 0 {
		t.Fatal("invalid admission persisted")
	}
}
func TestSessionMailboxConcurrentRetryAndScopedKey(t *testing.T) {
	s, db, clock := newTestStore(t)
	var path string
	if err := db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	second, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	otherStore := NewStore(second.DB).WithClock(clock.Now).WithIDs(s.idFn)
	req := mailboxRequest()
	const n = 8
	var wg sync.WaitGroup
	receipts := make(chan SessionMessageReceipt, n)
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store, handle := s, db
			if i%2 == 1 {
				store, handle = otherStore, second.DB
			}
			r, e := mailboxAccept(store, handle, req, allowMailbox)
			receipts <- r
			errs <- e
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original SessionMessageReceipt
	for r := range receipts {
		r.Duplicate = false
		if original.MessageID == "" {
			original = r
		} else if !reflect.DeepEqual(original, r) {
			t.Fatal("concurrent retry minted another acceptance")
		}
	}
	for _, change := range []string{"workspace", "human", "chat"} {
		r := req
		switch change {
		case "workspace":
			r.WorkspaceID = "other-workspace"
		case "human":
			r.UserID = "other-human"
		case "chat":
			r.SessionID = "other-chat"
		}
		got, err := mailboxAccept(s, db, r, allowMailbox)
		if err != nil || got.MessageID == original.MessageID {
			t.Fatalf("scope key aliased: %+v %v", got, err)
		}
	}
	if countRows(t, db, "session_mailbox") != 4 || countRows(t, db, "work_items") != 4 {
		t.Fatal("scoped admissions missing or duplicated")
	}
}
func TestSessionMailboxRetryRechecksOriginalProofWithoutGrantRefresh(t *testing.T) {
	s, db, _ := newTestStore(t)
	req := mailboxRequest()
	if _, err := mailboxAccept(s, db, req, allowMailbox); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("original permission revoked")
	req.Input.Body = "changed while hidden"
	_, err := mailboxAccept(s, db, req, func(context.Context, *sql.Tx, SessionMessageAdmission) error { return denied })
	if !errors.Is(err, denied) {
		t.Fatalf("denied retry returned ACK: %v", err)
	}
}

func TestSessionMailboxRejectsAmbiguousOrIncompleteRequest(t *testing.T) {
	for _, invalid := range []string{"nested duplicate", "trailing", "null attachments", "missing human", "missing chat", "missing intent key", "missing authority"} {
		t.Run(invalid, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			req := mailboxRequest()
			switch invalid {
			case "nested duplicate":
				req.Input.IntentJSON = []byte(`{"target":{"id":"one","id":"two"}}`)
			case "trailing":
				req.Input.IntentJSON = []byte(`{} {}`)
			case "null attachments":
				req.Input.AttachmentsJSON = []byte(`null`)
			case "missing human":
				req.UserID = ""
			case "missing chat":
				req.SessionID = ""
			case "missing intent key":
				req.IntentKey = ""
			case "missing authority":
				req.AuthorityJSON = nil
			}
			if _, err := mailboxAccept(s, db, req, allowMailbox); err == nil {
				t.Fatal("invalid admission accepted")
			}
			if countRows(t, db, "work_items") != 0 || countRows(t, db, "session_mailbox") != 0 {
				t.Fatal("invalid admission persisted")
			}
		})
	}
}
func TestSessionMailboxCapacityRefusesNewButRetainsOriginalACK(t *testing.T) {
	s, db, _ := newTestStore(t)
	s.ingressLimits.WorkspaceNonTerminal = 1
	req := mailboxRequest()
	first, err := mailboxAccept(s, db, req, allowMailbox)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := mailboxAccept(s, db, req, allowMailbox)
	if err != nil || retry.WorkID != first.WorkID {
		t.Fatal("fullness refused original ACK")
	}
	req.IntentKey = "new-intent"
	if _, err := mailboxAccept(s, db, req, allowMailbox); !errors.Is(err, ErrWorkspaceFull) {
		t.Fatalf("new admission escaped existing ingress cap: %v", err)
	}
	if countRows(t, db, "work_items") != 1 || countRows(t, db, "session_mailbox") != 1 {
		t.Fatal("capacity refusal partially persisted")
	}
}

func TestSessionMailboxRetainsFullEnvelopeDigest(t *testing.T) {
	s, db, _ := newTestStore(t)
	ack, err := mailboxAccept(s, db, mailboxRequest(), allowMailbox)
	if err != nil {
		t.Fatal(err)
	}
	var input, digest string
	if err = db.QueryRow(`SELECT input_json,input_sha256 FROM work_items WHERE id=?`, ack.WorkID).Scan(&input, &digest); err != nil {
		t.Fatal(err)
	}
	if digest != mailboxDigest([]byte(input)) {
		t.Fatal("ledger digest does not protect the captured envelope")
	}
}
func TestSessionMailboxTamperedEnvelopeNeverReachesAuthorityHook(t *testing.T) {
	s, db, _ := newTestStore(t)
	req := mailboxRequest()
	ack, err := mailboxAccept(s, db, req, allowMailbox)
	if err != nil {
		t.Fatal(err)
	}
	var input string
	if err = db.QueryRow(`SELECT input_json FROM work_items WHERE id=?`, ack.WorkID).Scan(&input); err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(input, `"captured":"original"`, `"captured":"tampered"`, 1)
	if tampered == input {
		t.Fatal("test did not alter captured proof")
	}
	if _, err = db.Exec(`UPDATE work_items SET input_json=? WHERE id=?`, tampered, ack.WorkID); err != nil {
		t.Fatal(err)
	}
	called := false
	_, err = mailboxAccept(s, db, req, func(context.Context, *sql.Tx, SessionMessageAdmission) error { called = true; return nil })
	if err == nil || called {
		t.Fatalf("tampered captured proof reached authority/ACK: called=%t err=%v", called, err)
	}
}
func TestSessionMailboxRejectsChangedAcceptanceBindings(t *testing.T) {
	for _, column := range []string{"accepted time", "sequence", "work scope", "work agent", "mailbox body"} {
		t.Run(column, func(t *testing.T) {
			s, db, _ := newTestStore(t)
			req := mailboxRequest()
			ack, err := mailboxAccept(s, db, req, allowMailbox)
			if err != nil {
				t.Fatal(err)
			}
			var statement string
			switch column {
			case "accepted time":
				statement = `UPDATE session_mailbox SET created_at='2030-01-01T00:00:00Z' WHERE work_id=?`
			case "sequence":
				statement = `UPDATE session_mailbox SET seq=99 WHERE work_id=?`
			case "work scope":
				statement = `UPDATE work_items SET workspace_id='other' WHERE id=?`
			case "work agent":
				statement = `UPDATE work_items SET agent_id='other' WHERE id=?`
			case "mailbox body":
				statement = `UPDATE session_mailbox SET body='altered' WHERE work_id=?`
			}
			if _, err = db.Exec(statement, ack.WorkID); err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = mailboxAccept(s, db, req, func(context.Context, *sql.Tx, SessionMessageAdmission) error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("altered binding reached authority/ACK: called=%t err=%v", called, err)
			}
		})
	}
}
