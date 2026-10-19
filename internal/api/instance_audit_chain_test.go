package api

import (
	"context"
	"sync"

	"encoding/json"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstanceAuditInsertAcknowledgement(t *testing.T) {
	for _, body := range []string{
		`SELECT RAISE(IGNORE);`,
		`DELETE FROM instance_audit_logs WHERE id=NEW.id;`,
		`UPDATE instance_audit_logs SET metadata='changed' WHERE id=NEW.id;`,
	} {
		t.Run(body[:6], func(t *testing.T) {
			f := newTokenAuditFixture(t, "create")
			when := "AFTER"
			if strings.Contains(body, "IGNORE") {
				when = "BEFORE"
			}
			execOrFatal(t, f.db, `CREATE TRIGGER alter_audit `+when+` INSERT ON instance_audit_logs BEGIN `+body+` END`)
			rr := f.invoke()
			if rr.Code != 500 {
				t.Errorf("unacknowledged audit returned%d", rr.Code)
			}
			f.assertMutation(t, "create", false)
		})
	}
}

func TestInstanceAuditReaderIntegrityAndTailRefusal(t *testing.T) {
	f := newInstanceFixture(t)
	execOrFatal(t, f.db, `INSERT INTO instance_audit_logs(id,action,entity_type,entity_id,created_at) VALUES('legacy-f1','legacy','test','legacy-f1','2020-01-01')`)
	read := func(id string) string {
		t.Helper()
		rr := f.do(f.boss, "GET", "/api/v1/admin/instance/audit?limit=1&entity_id="+id, "")
		if rr.Code != 200 {
			t.Fatalf("audit reader=%d", rr.Code)
		}
		var rows []instanceAuditRow
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 {
			t.Fatalf("bounded reader rows=%d", len(rows))
		}
		return rows[0].Integrity
	}
	if got := read("legacy-f1"); got != "legacy_unverified" {
		t.Fatalf("legacy=%s", got)
	}
	h := NewCLITokenHandler(f.db, newTestLogger())
	create := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{}`))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "boss"}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr
	}
	if rr := create(); rr.Code != http.StatusOK {
		t.Fatalf("positive create=%d", rr.Code)
	}
	var id string
	if err := f.db.QueryRow(`SELECT entity_id FROM instance_audit_logs WHERE action='auth.cli_token_created'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if got := read(id); got != "entry_verified" {
		t.Fatalf("honest row=%s", got)
	}
	// NULL -> '{}' has the same old COALESCE projection, but different stored bytes.
	execOrFatal(t, f.db, `UPDATE instance_audit_logs SET metadata='{}' WHERE entity_id=?`, id)
	if got := read(id); got != "invalid" {
		t.Fatalf("nullable tamper=%s", got)
	}
	var before int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if rr := create(); rr.Code != 500 {
		t.Errorf("invalid tail allowed create=%d", rr.Code)
	}
	var after int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("invalid tail did not roll back token")
	}
}

func TestCLITokenRepeatedRevokeIsAudited(t *testing.T) {
	f := newTokenAuditFixture(t, "revoke")
	for i := 0; i < 2; i++ {
		if rr := f.invoke(); rr.Code != 200 {
			t.Fatalf("revoke%d=%d", i, rr.Code)
		}
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='auth.cli_token_revoked' AND entity_id='f1-token'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("repeated successful revoke audits=%d", n)
	}
}

func TestCLITokenEmptyBulkDoesNotInventEvents(t *testing.T) {
	for _, door := range []string{"password", "suspend"} {
		t.Run(door, func(t *testing.T) {
			f := newTokenAuditFixture(t, door)
			execOrFatal(t, f.db, `UPDATE cli_tokens SET revoked_at='2020-01-01' WHERE id='f1-token'`)
			if rr := f.invoke(); rr.Code != 200 {
				t.Fatalf("empty bulk=%d", rr.Code)
			}
			var n int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='auth.cli_token_revoked'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("empty bulk invented%d token events", n)
			}
		})
	}
}

func TestInstanceAuditConcurrentTokenCreation(t *testing.T) {
	db := testutil.MigratedDB(t).DB
	// Hold two distinct physical connections and verify production DSN pragmas.
	for i := 0; i < 2; i++ {
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		var fk int
		var mode string
		if err := conn.QueryRowContext(t.Context(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(t.Context(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || mode != "wal" {
			t.Fatalf("production connection settings: FK%d journal%s", fk, mode)
		}
	}
	if db.Stats().InUse < 2 {
		t.Fatal("multi-connection control did not hold two connections")
	}
	user := seedTestUser(t, db)
	h := NewCLITokenHandler(db, newTestLogger())
	start := make(chan struct{})
	results := make(chan int, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{}`))
			req = req.WithContext(withUser(req.Context(), &AuthUser{ID: user}))
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			results <- rr.Code
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for status := range results {
		if status != 200 {
			t.Errorf("concurrent creation=%d", status)
		}
	}
	rows, err := db.Query(`SELECT ` + instanceAuditColumns + `,` + instanceAuditChainColumns + ` FROM instance_audit_logs a` + instanceAuditPredecessor + ` ORDER BY a.chain_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := int64(0)
	prev := ""
	for rows.Next() {
		var f journal.InstanceAuditFields
		var p instanceAuditProof
		if err := rows.Scan(p.scanArgs(&f)...); err != nil {
			t.Fatal(err)
		}
		count++
		if p.seq.Int64 != count || p.prev.String != prev || p.integrity(f) != "entry_verified" {
			t.Error("concurrent chain has duplicate/gap/broken link")
		}
		prev = p.hash.String
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("audit rows=%d want8", count)
	}
}

func TestInstanceAuditMiddleGapRefusesTailExtension(t *testing.T) {
	f := newInstanceFixture(t)
	h := NewCLITokenHandler(f.db, newTestLogger())
	create := func() int {
		req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{}`))
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: "boss"}))
		rr := httptest.NewRecorder()
		h.Create(rr, req)
		return rr.Code
	}
	for i := 0; i < 2; i++ {
		if status := create(); status != 200 {
			t.Fatalf("positive creation=%d", status)
		}
	}
	var seq int64
	var id string
	if err := f.db.QueryRow(`SELECT chain_seq,entity_id FROM instance_audit_logs ORDER BY chain_seq DESC LIMIT 1`).Scan(&seq, &id); err != nil {
		t.Fatal(err)
	}
	if seq < 3 {
		t.Fatal("positive middle-gap fixture needs three chained rows")
	}
	execOrFatal(t, f.db, `DELETE FROM instance_audit_logs WHERE chain_seq=?`, seq-1)
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/audit?entity_id="+id, "")
	var entries []instanceAuditRow
	if rr.Code != 200 {
		t.Fatalf("reader=%d", rr.Code)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Integrity != "invalid" {
		t.Fatal("missing predecessor not reported")
	}
	var before, after int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if status := create(); status != 500 {
		t.Errorf("gap tail extended=%d", status)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("gap tail failed to roll back token")
	}
}

func TestInstanceSuspendWithoutActorFailsClosed(t *testing.T) {
	f := newInstanceFixture(t)
	h := NewInstanceAdminHandler(f.db, newTestLogger(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/api/v1/admin/instance/people/carol/suspend", strings.NewReader(`{}`))
	req.SetPathValue("userId", "carol")
	rr := httptest.NewRecorder()
	h.Suspend(rr, req)
	if rr.Code != 401 {
		t.Fatalf("missing actor=%d", rr.Code)
	}
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM users WHERE id='carol' AND suspended_at IS NOT NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("missing actor mutated suspension")
	}
}
