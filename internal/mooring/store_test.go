package mooring

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

var templateOnce sync.Once
var templateData []byte
var templateErr error

func fixture(t *testing.T) (*Store, *database.DB, Authorize, Key) {
	t.Helper()
	templateOnce.Do(func() {
		path := filepath.Join(t.TempDir(), "template.db")
		db, err := database.Open("file:" + path)
		if err != nil {
			templateErr = err
			return
		}
		err = database.Migrate(t.Context(), db.DB, slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err == nil {
			_, err = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
		}
		closeErr := db.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			templateErr = err
			return
		}
		templateData, templateErr = os.ReadFile(path)
	})
	if templateErr != nil {
		t.Fatal(templateErr)
	}
	path := filepath.Join(t.TempDir(), "workbook.db")
	if err := os.WriteFile(path, templateData, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	for _, q := range []string{
		`INSERT INTO users(id,email) VALUES('owner','owner@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','workbook-test')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES('page','ws','book','Book','owner',1,'{}')`,
	} {
		if _, err = db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	auth := func(context.Context, *sql.Tx) (Principal, error) {
		return Principal{AuthorityID: "user:owner", MandateID: "human-import", Targets: []string{"*"}}, nil
	}
	return New(db.DB, Options{Enabled: true}), db, auth, Key{"ws", "page"}
}

type fakeEngine struct {
	calls   atomic.Int32
	prepare func(context.Context, Binding, State, Command) (Candidate, error)
}

func (e *fakeEngine) Identity() string { return "test-engine@1+sha256:test" }
func (e *fakeEngine) Prepare(ctx context.Context, b Binding, s State, c Command) (Candidate, error) {
	e.calls.Add(1)
	if e.prepare != nil {
		return e.prepare(ctx, b, s, c)
	}
	data := c.Snapshot
	effects := []string{"*"}
	if c.Kind == "cell.set" {
		data = json.RawMessage(`{"sheets":[{"B2":"42"}]}`)
		effects = []string{"Sheet1!B2"}
	}
	return NewCandidate(b, data, effects), nil
}
func imported(t *testing.T, s *Store, a Authorize, k Key, e Engine) Receipt {
	t.Helper()
	r, err := s.Execute(t.Context(), k, Request{MutationID: "import-1", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[{}]}`)}}, a, e)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func edit() Request {
	return Request{MutationID: "edit-1", MandateID: "human-import", BaseRev: 1, Generation: 1, Command: Command{Kind: "cell.set", Cell: "B2", Sheet: "Sheet1", Input: "42"}}
}

func TestImportEditReopenAndExactRetryAfterHeadAdvanced(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	initial := imported(t, s, a, k, e)
	if initial.Rev != 1 || initial.Durability != "node" {
		t.Fatalf("import receipt: %+v", initial)
	}
	req := edit()
	original, err := s.Execute(t.Context(), k, req, a, e)
	if err != nil {
		t.Fatal(err)
	}
	next := req
	next.MutationID = "edit-2"
	next.BaseRev = 2
	if _, err = s.Execute(t.Context(), k, next, a, e); err != nil {
		t.Fatal(err)
	}
	// A replacement host/service has no process-local receipts or engine state.
	reopened := New(db.DB, Options{Enabled: true})
	retry, err := reopened.Execute(t.Context(), k, req, a, e)
	if err != nil || !reflect.DeepEqual(original, retry) || e.calls.Load() != 3 {
		t.Fatalf("retry=%+v err=%v calls=%d", retry, err, e.calls.Load())
	}
	state, err := reopened.Read(t.Context(), k, a)
	if err != nil || state.Rev != 3 || string(state.Snapshot) != `{"sheets":[{"B2":"42"}]}` {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	for _, table := range []string{"gridcraft_revisions", "gridcraft_audit", "gridcraft_outbox"} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 3 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
	req.Command.Input = "99"
	if _, err = s.Execute(t.Context(), k, req, a, e); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed payload: %v", err)
	}
}

func TestConcurrentDuplicateDoesNotStartSecondCandidate(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	entered, release := make(chan struct{}), make(chan struct{})
	e.prepare = func(_ context.Context, b Binding, _ State, _ Command) (Candidate, error) {
		close(entered)
		<-release
		return NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{"Sheet1!B2"}), nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(t.Context(), k, edit(), a, e); done <- err }()
	<-entered
	_, err := s.Execute(t.Context(), k, edit(), a, e)
	close(release)
	if !errors.Is(err, ErrInProgress) {
		t.Errorf("duplicate: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if e.calls.Load() != 2 {
		t.Fatalf("engine started %d times", e.calls.Load())
	}
}

func TestCommitChecksRejectRevocationStaleBaseAndExtraEffects(t *testing.T) {
	for _, scenario := range []string{"revoked", "base", "effects", "binding", "hash", "engine-failure"} {
		t.Run(scenario, func(t *testing.T) {
			s, db, a, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, a, k, e)
			var revoked atomic.Bool
			auth := func(ctx context.Context, tx *sql.Tx) (Principal, error) {
				if revoked.Load() {
					return Principal{}, ErrForbidden
				}
				p, err := a(ctx, tx)
				p.Targets = []string{"Sheet1!B2"}
				return p, err
			}
			e.prepare = func(_ context.Context, b Binding, _ State, _ Command) (Candidate, error) {
				candidate := NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{"Sheet1!B2"})
				switch scenario {
				case "revoked":
					revoked.Store(true)
				case "base":
					if _, err := db.Exec("UPDATE gridcraft_heads SET head_rev=head_rev+1 WHERE page_id=?", k.PageID); err != nil {
						t.Fatal(err)
					}
				case "effects":
					candidate.Effects = append(candidate.Effects, "Sheet1!C9")
				case "binding":
					candidate.Binding.Generation++
				case "hash":
					candidate.Snapshot = json.RawMessage(`{"tampered":true}`)
				case "engine-failure":
					return Candidate{}, errors.New("engine died")
				}
				return candidate, nil
			}
			_, err := s.Execute(t.Context(), k, edit(), auth, e)
			want := map[string]error{"revoked": ErrForbidden, "base": ErrPrecondition, "effects": ErrEffects, "binding": ErrCandidate, "hash": ErrCandidate}[scenario]
			if err == nil || (want != nil && !errors.Is(err, want)) {
				t.Fatalf("got %v want %v", err, want)
			}
			for _, table := range []string{"gridcraft_revisions", "gridcraft_audit", "gridcraft_outbox"} {
				var n int
				if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 1 {
					t.Fatalf("partial commit %s=%d: %v", table, n, err)
				}
			}
		})
	}
}

func TestFailedEngineCanRetryAndReceiptRequiresCurrentAuthorization(t *testing.T) {
	s, _, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	e.prepare = func(context.Context, Binding, State, Command) (Candidate, error) {
		return Candidate{}, errors.New("crash before commit")
	}
	if _, err := s.Execute(t.Context(), k, edit(), a, e); err == nil {
		t.Fatal("accepted crash")
	}
	e.prepare = nil
	r, err := s.Execute(t.Context(), k, edit(), a, e)
	if err != nil || r.Rev != 2 {
		t.Fatalf("recovery=%+v %v", r, err)
	}
	denied := func(context.Context, *sql.Tx) (Principal, error) { return Principal{}, ErrForbidden }
	if _, err = s.Execute(t.Context(), k, edit(), denied, e); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked receipt: %v", err)
	}
	if _, err = s.Read(t.Context(), k, denied); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked read: %v", err)
	}
}

func TestCapacityAndDefaultOffRejectBeforeEngine(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	off := New(db.DB, Options{})
	if _, err := off.Execute(t.Context(), k, edit(), a, e); !errors.Is(err, ErrDisabled) {
		t.Fatalf("default flag: %v", err)
	}
	imported(t, s, a, k, e)
	// A live worker from another host incarnation occupies the only global slot.
	now := time.Now().UTC().Add(time.Minute).UnixMilli()
	if _, err := db.Exec(`INSERT INTO gridcraft_operations(workspace_id,page_id,authority_id,mutation_id,payload_hash,state,token,lease_until) VALUES('ws','page','other','busy','h','in_progress',1,?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(t.Context(), k, edit(), a, e); !errors.Is(err, ErrCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	if e.calls.Load() != 1 {
		t.Fatal("engine ran past capacity")
	}
}

func TestExpiredReservationTakeoverFencesOldCandidate(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	e.prepare = func(_ context.Context, b Binding, _ State, _ Command) (Candidate, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{"Sheet1!B2"}), nil
	}
	done := make(chan error, 1)
	go func() { _, err := s.Execute(t.Context(), k, edit(), a, e); done <- err }()
	<-entered
	if _, err := db.Exec(`UPDATE gridcraft_operations SET lease_until=0 WHERE mutation_id='edit-1'`); err != nil {
		t.Fatal(err)
	}
	// A new service incarnation tests the durable fence independently of the
	// old Store's physical-worker admission. Real deployment additionally needs
	// orphan-worker reconciliation, which this synthetic engine does not prove.
	r, err := New(db.DB, Options{Enabled: true}).Execute(t.Context(), k, edit(), a, e)
	close(release)
	if err != nil || r.Rev != 2 {
		t.Fatalf("takeover=%+v %v", r, err)
	}
	if err := <-done; !errors.Is(err, ErrFence) {
		t.Fatalf("old candidate: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM gridcraft_revisions`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("confirmed twice %d: %v", n, err)
	}
}

func TestAuditFailureRollsBackContentReceiptAndOutbox(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	imported(t, s, a, k, e)
	if _, err := db.Exec(`CREATE TRIGGER audit_fault BEFORE INSERT ON gridcraft_audit BEGIN SELECT RAISE(ABORT,'injected audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Execute(t.Context(), k, edit(), a, e); err == nil {
		t.Fatal("audit failure accepted")
	}
	v, err := s.Read(t.Context(), k, a)
	if err != nil || v.Rev != 1 {
		t.Fatalf("head after failed transaction: %+v %v", v, err)
	}
	if _, err = db.Exec(`DROP TRIGGER audit_fault`); err != nil {
		t.Fatal(err)
	}
	r, err := s.Execute(t.Context(), k, edit(), a, e)
	if err != nil || r.Rev != 2 {
		t.Fatalf("retry %+v %v", r, err)
	}
	status, err := s.Status(t.Context(), k, "edit-1", a)
	if err != nil || status.Delivery != "pending" || status.Receipt.Rev != 2 || status.EffectInHead != "present" {
		t.Fatalf("status %+v %v", status, err)
	}
	if _, err = db.Exec(`UPDATE gridcraft_operations SET receipt='{}' WHERE state='applied'`); err == nil {
		t.Fatal("mutable confirmed receipt")
	}
}

func TestWorkbookStorageRejectsWrongWorkspaceAndOtherContent(t *testing.T) {
	s, db, a, k := fixture(t)
	e := &fakeEngine{}
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('other','Other','other-ws')`); err != nil {
		t.Fatal(err)
	}
	k.WorkspaceID = "other"
	if _, err := s.Execute(t.Context(), k, Request{MutationID: "wrong-ws", MandateID: "human-import", Command: Command{Kind: "import", Snapshot: json.RawMessage(`{"sheets":[]}`)}}, a, e); err == nil {
		t.Fatal("cross-workspace Page accepted")
	}
	k.WorkspaceID = "ws"
	imported(t, s, a, k, e)
	if _, err := db.Exec(`INSERT INTO documents(workspace_id,page_id,schema_version,head_rev,epoch,snapshot_rev,snapshot,projection,generation) VALUES('ws','page',1,0,0,0,x'01','[]',1)`); err == nil {
		t.Fatal("Document attached over workbook")
	}
}

func TestWorkerAdmissionSeparatelyBoundsGlobalWorkspaceAndArtifact(t *testing.T) {
	for _, scope := range []string{"global", "workspace", "artifact"} {
		t.Run(scope, func(t *testing.T) {
			_, db, a, k := fixture(t)
			engine := &fakeEngine{}
			s := New(db.DB, Options{Enabled: true, MaxWorkers: 4, MaxWorkspaceWorkers: 4})
			imported(t, s, a, k, engine)
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('other-ws','Other','other-admission')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('other-page','ws','other','Other','owner','{}'),('other-ws-page','other-ws','other','Other','owner','{}')`); err != nil {
				t.Fatal(err)
			}
			busyWS, busyPage := "ws", "page"
			switch scope {
			case "global":
				s.options.MaxWorkers = 1
				busyWS, busyPage = "other-ws", "other-ws-page"
			case "workspace":
				s.options.MaxWorkspaceWorkers = 1
				busyPage = "other-page"
			}
			if _, err := db.Exec(`INSERT INTO gridcraft_operations(workspace_id,page_id,authority_id,mutation_id,payload_hash,state,token,lease_until) VALUES(?,?,'other','busy','hash','in_progress',1,?)`, busyWS, busyPage, time.Now().Add(time.Minute).UnixMilli()); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Execute(t.Context(), k, edit(), a, engine); !errors.Is(err, ErrCapacity) {
				t.Fatalf("admission %s: %v", scope, err)
			}
			if engine.calls.Load() != 1 {
				t.Fatal("hidden work started past capacity")
			}
		})
	}
}

func TestExpiredLeaseDoesNotFreeStillRunningWorker(t *testing.T) {
	for _, scope := range []string{"global", "workspace", "artifact"} {
		t.Run(scope, func(t *testing.T) {
			s, db, a, k := fixture(t)
			e := &fakeEngine{}
			imported(t, s, a, k, e)
			other := k
			if scope != "artifact" {
				other.PageID = "another-page"
				if scope == "global" {
					other.WorkspaceID = "another-ws"
					if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('another-ws','Another','another-live-worker')`); err != nil {
						t.Fatal(err)
					}
					s.options.MaxWorkers = 1
				} else {
					s.options.MaxWorkspaceWorkers = 1
				}
				if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,private_audience,spec_json) VALUES(?,?,'another','Another','owner',1,'{}')`, other.PageID, other.WorkspaceID); err != nil {
					t.Fatal(err)
				}
				imported(t, s, a, other, e)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			e.prepare = func(_ context.Context, b Binding, _ State, _ Command) (Candidate, error) {
				close(entered)
				<-release // models cleanup that continues after the lease deadline
				return NewCandidate(b, json.RawMessage(`{"sheets":[]}`), []string{"Sheet1!B2"}), nil
			}
			done := make(chan error, 1)
			go func() { _, err := s.Execute(t.Context(), k, edit(), a, e); done <- err }()
			<-entered
			if _, err := db.Exec(`UPDATE gridcraft_operations SET lease_until=0 WHERE state='in_progress'`); err != nil {
				close(release)
				<-done
				t.Fatal(err)
			}
			second := edit()
			second.MutationID = "second-live-worker"
			unexpected := &fakeEngine{}
			_, err := s.Execute(t.Context(), other, second, a, unexpected)
			close(release)
			<-done
			if !errors.Is(err, ErrCapacity) || unexpected.calls.Load() != 0 {
				t.Fatalf("live %s capacity lost: err=%v calls=%d", scope, err, unexpected.calls.Load())
			}
			if _, err := s.Execute(t.Context(), other, second, a, unexpected); err != nil || unexpected.calls.Load() != 1 {
				t.Fatalf("capacity did not return after cleanup: err=%v calls=%d", err, unexpected.calls.Load())
			}
		})
	}
}
