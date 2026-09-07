package providerpool

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func poolFixture(t *testing.T) (*Store, *sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pool.db")
	db := testutil.MigratedDBAt(t, path).DB
	execPoolSQL(t, db, `INSERT INTO users(id, email) VALUES ('u', 'pool@example.test'), ('other', 'other@example.test')`)
	execPoolSQL(t, db, `INSERT INTO workspaces(id, name, slug) VALUES ('ws', 'Pool', 'pool'), ('foreign', 'Foreign', 'foreign')`)
	for _, id := range []string{"a", "b", "c", "foreign-account"} {
		workspace := "ws"
		if id == "foreign-account" {
			workspace = "foreign"
		}
		execPoolSQL(t, db, `INSERT INTO credentials(id, workspace_id, name, encrypted_value, type, provider, created_by)
			VALUES (?, ?, ?, 'test-ciphertext-never-decrypted', 'PROVIDER_LOGIN', 'OPENAI', 'u')`, id, workspace, id)
		execPoolSQL(t, db, `INSERT INTO credential_fields(credential_id, key, value, is_secret) VALUES (?, 'mode', 'subscription', 0)`, id)
	}
	return NewStore(db), db, path
}

func execPoolSQL(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func fixturePool() Pool {
	return Pool{ID: "pool", WorkspaceID: "ws", Name: "OpenAI team", CreatedBy: "u",
		Policy: Policy{Provider: "OPENAI", Mode: "subscription"}, Members: []Member{{CredentialID: "a"}, {CredentialID: "b"}}}
}

func TestStoreSelectionDurableAndReadOnly(t *testing.T) {
	s, db, path := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	first, err := s.Choose(ctx, "ws", "pool", now)
	if err != nil || first.ID != "a" || first.LastSelected != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	for range 3 {
		_, candidates, err := s.Snapshot(ctx, "ws", "pool")
		if err != nil || len(candidates) != 2 {
			t.Fatalf("snapshot=%+v err=%v", candidates, err)
		}
	}
	// A separate DB handle and Store have no in-memory cursor to inherit.
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second, err := NewStore(reopened.DB).Choose(ctx, "ws", "pool", now)
	if err != nil || second.ID != "b" || second.LastSelected != 2 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	var bindings int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_bindings`).Scan(&bindings); err != nil {
		t.Fatal(err)
	}
	if bindings != 0 {
		t.Fatal("creating/selecting a pool implicitly granted access")
	}
}

func TestStoreConcurrentSelections(t *testing.T) {
	s, _, _ := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	const starts = 20
	results := make(chan Candidate, starts)
	errs := make(chan error, starts)
	var wg sync.WaitGroup
	for range starts {
		wg.Add(1)
		go func() { defer wg.Done(); c, err := s.Choose(ctx, "ws", "pool", time.Now()); results <- c; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int64]bool{}
	counts := map[string]int{}
	for c := range results {
		if seen[c.LastSelected] {
			t.Fatalf("duplicate sequence %d", c.LastSelected)
		}
		seen[c.LastSelected] = true
		counts[c.ID]++
	}
	if len(seen) != starts || counts["a"] != starts/2 || counts["b"] != starts/2 {
		t.Fatalf("sequences=%v counts=%v", seen, counts)
	}
}

func TestStoreMembershipValidationAndRollback(t *testing.T) {
	for _, tc := range []struct {
		name, sql string
		want      error
	}{
		{"different owner", `UPDATE credentials SET created_by = 'other' WHERE id = 'b'`, ErrCrossOwner},
		{"wrong provider", `UPDATE credentials SET provider = 'GOOGLE' WHERE id = 'b'`, ErrInvalid},
		{"wrong mode", `UPDATE credential_fields SET value = 'api_key' WHERE credential_id = 'b' AND key = 'mode'`, ErrInvalid},
		{"ordinary secret", `UPDATE credentials SET type = 'SECRET' WHERE id = 'b'`, ErrInvalid},
		{"legacy subscription needs import", `UPDATE credentials SET type = 'AI_CLI_TOKEN' WHERE id = 'b'`, ErrInvalid},
		{"malformed expiry", `INSERT INTO credential_fields(credential_id,key,value,is_secret) VALUES ('b','expires_at','nonsense',0)`, ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db, _ := poolFixture(t)
			execPoolSQL(t, db, tc.sql)
			if err := s.Create(context.Background(), fixturePool()); !errors.Is(err, tc.want) {
				t.Fatalf("create=%v want=%v", err, tc.want)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM provider_login_pools`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatal("invalid create left a partial pool")
			}
		})
	}
}

func TestStoreRevalidatesChangedAccountsAndRollsBackTurns(t *testing.T) {
	s, db, _ := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	execPoolSQL(t, db, `UPDATE credentials SET created_by = 'other' WHERE id = 'b'`)
	if _, err := s.Choose(ctx, "ws", "pool", time.Now()); !errors.Is(err, ErrCrossOwner) {
		t.Fatalf("changed owner: %v", err)
	}
	execPoolSQL(t, db, `UPDATE provider_login_pools SET allow_cross_owner = 1 WHERE id = 'pool'`)
	got, err := s.Choose(ctx, "ws", "pool", time.Now())
	if err != nil || got.LastSelected != 1 {
		t.Fatalf("failed selection consumed turn: %+v %v", got, err)
	}
	execPoolSQL(t, db, `UPDATE credentials SET deleted_at = datetime('now') WHERE id IN ('a','b')`)
	if _, err := s.Choose(ctx, "ws", "pool", time.Now()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("all revoked: %v", err)
	}
}

func TestStoreTenantIsolationAndDatabaseGuards(t *testing.T) {
	s, db, _ := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Choose(ctx, "foreign", "pool", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant choose: %v", err)
	}
	if _, _, err := s.Snapshot(ctx, "foreign", "pool"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant snapshot: %v", err)
	}
	for _, query := range []string{
		`INSERT INTO provider_login_pool_members(pool_id,credential_id) VALUES ('pool','foreign-account')`,
		`UPDATE provider_login_pool_members SET credential_id = 'foreign-account' WHERE credential_id = 'a'`,
		`UPDATE provider_login_pools SET workspace_id = 'foreign' WHERE id = 'pool'`,
		`UPDATE credentials SET workspace_id = 'foreign' WHERE id = 'a'`,
	} {
		if _, err := db.Exec(query); err == nil {
			t.Fatalf("tenant guard accepted %s", query)
		}
	}
	// Preserve one ordinary secret per scope/slot.
	execPoolSQL(t, db, `INSERT INTO credential_bindings(id,workspace_id,credential_id,scope,slot) VALUES ('binding','ws','a','WORKSPACE','OPENAI_LOGIN')`)
	if _, err := db.Exec(`INSERT INTO credential_bindings(id,workspace_id,credential_id,scope,slot) VALUES ('collision','ws','b','WORKSPACE','OPENAI_LOGIN')`); err == nil {
		t.Fatal("ordinary binding uniqueness was weakened")
	}
}

func TestStoreAvailabilityPersistenceAndStaleObservations(t *testing.T) {
	s, _, path := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	obs := Observation{Generation: Generation("test-ciphertext-never-decrypted"), At: now, CooldownUntil: now.Add(time.Hour), Source: "native_cli"}
	if err := s.RecordObservation(ctx, "foreign", "a", obs); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross tenant observation: %v", err)
	}
	if err := s.RecordObservation(ctx, "ws", "a", obs); err != nil {
		t.Fatal(err)
	}
	obs.At = now.Add(time.Second)
	obs.CooldownUntil = now.Add(time.Minute)
	obs.BlockedReason = "billing"
	if err := s.RecordObservation(ctx, "ws", "a", obs); err != nil {
		t.Fatal(err)
	}
	newest := int64(2)
	obs.At = now.Add(-time.Second)
	obs.CooldownUntil = now.Add(24 * time.Hour)
	obs.BlockedReason = "authentication"
	if err := s.RecordObservation(ctx, "ws", "a", obs); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = NewStore(reopened.DB)
	_, cs, err := s.Snapshot(ctx, "ws", "pool")
	if err != nil {
		t.Fatal(err)
	}
	if !cs[0].Blocked || !cs[0].CooldownUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("observation shortened/overwritten: %+v", cs[0])
	}
	got, err := s.Choose(ctx, "ws", "pool", now.Add(2*time.Hour))
	if err != nil || got.ID != "b" {
		t.Fatalf("billing block retried after cooldown: %+v %v", got, err)
	}
	for _, pair := range []struct {
		ws       string
		revision int64
	}{{"foreign", newest}, {"ws", 1}} {
		if cleared, err := s.ClearObservation(ctx, pair.ws, "a", pair.revision); err != nil || cleared {
			t.Fatalf("unsafe clear=%v err=%v", cleared, err)
		}
	}
	if cleared, err := s.ClearObservation(ctx, "ws", "a", newest); err != nil || !cleared {
		t.Fatalf("verified clear=%v err=%v", cleared, err)
	}
	got, err = s.Choose(ctx, "ws", "pool", now.Add(2*time.Hour))
	if err != nil || got.ID != "a" {
		t.Fatalf("cleared account not recovered: %+v %v", got, err)
	}
	obs.At = now.Add(3 * time.Hour)
	obs.CooldownUntil = now.Add(4 * time.Hour)
	if err := s.RecordObservation(ctx, "ws", "a", obs); err != nil {
		t.Fatal(err)
	}
	if cleared, err := s.ClearObservation(ctx, "ws", "a", 1); err != nil || cleared {
		t.Fatalf("old revision cleared new observation: %v %v", cleared, err)
	}
	current, err := s.ReadObservation(ctx, "ws", "a")
	if err != nil || current.Revision != 4 {
		t.Fatalf("read observation=%+v %v", current, err)
	}
	if _, err := s.ReadObservation(ctx, "foreign", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign observation leaked: %v", err)
	}
}

func TestStoreEqualTimestampObservationCannotEraseStrongerFailure(t *testing.T) {
	s, _, _ := poolFixture(t)
	ctx := context.Background()
	now := time.Now()
	obs := Observation{Generation: Generation("test-ciphertext-never-decrypted"), At: now, CooldownUntil: now.Add(time.Second), Source: "provider_http"}
	if err := s.RecordObservation(ctx, "ws", "a", obs); err != nil {
		t.Fatal(err)
	}
	obs.BlockedReason = "billing"
	obs.CooldownUntil = obs.CooldownUntil.Add(time.Nanosecond)
	if err := s.RecordObservation(ctx, "ws", "a", obs); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadObservation(ctx, "ws", "a")
	if err != nil || got.Revision != 2 || got.BlockedReason != "billing" || !got.CooldownUntil.Equal(obs.CooldownUntil) {
		t.Fatalf("equal timestamp observation lost precision/severity: %+v %v", got, err)
	}
	if cleared, err := s.ClearObservation(ctx, "ws", "a", 1); err != nil || cleared {
		t.Fatalf("stale clear=%v %v", cleared, err)
	}
}

func TestStoreInvalidObservations(t *testing.T) {
	s, _, _ := poolFixture(t)
	now := time.Now()
	for i, obs := range []Observation{
		{},
		{At: now, Source: "native_cli"},
		{At: now, Source: "tool_output", CooldownUntil: now.Add(time.Minute)},
		{At: now, Source: "native_cli", CooldownUntil: now},
		{At: now, Source: "provider_http", BlockedReason: "raw-secret-error-message"},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			obs.Generation = Generation("test-ciphertext-never-decrypted")
			if err := s.RecordObservation(context.Background(), "ws", "a", obs); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid observation: %v", err)
			}
		})
	}
}

func TestStoreTenantReadGuardSurvivesMissingWriteTrigger(t *testing.T) {
	s, db, _ := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	execPoolSQL(t, db, `DROP TRIGGER trg_provider_login_pool_member_workspace_insert`)
	execPoolSQL(t, db, `INSERT INTO provider_login_pool_members(pool_id,credential_id) VALUES ('pool','foreign-account')`)
	if _, err := s.Choose(ctx, "ws", "pool", time.Now()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("selection silently accepted a cross-workspace member: %v", err)
	}
	if _, _, err := s.Snapshot(ctx, "ws", "pool"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("snapshot leaked a cross-workspace member: %v", err)
	}
}

func TestStoreUnavailableDoesNotConsumeTurn(t *testing.T) {
	s, db, _ := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, id := range []string{"a", "b"} {
		if err := s.RecordObservation(ctx, "ws", id, Observation{Generation: Generation("test-ciphertext-never-decrypted"), At: now, CooldownUntil: now.Add(time.Minute), Source: "native_cli"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Choose(ctx, "ws", "pool", now); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("all cooled: %v", err)
	}
	got, err := s.Choose(ctx, "ws", "pool", now.Add(time.Minute))
	if err != nil || got.ID != "a" || got.LastSelected != 1 {
		t.Fatalf("cooldown consumed turn or did not expire: %+v %v", got, err)
	}
	execPoolSQL(t, db, `INSERT INTO provider_login_refresh(credential_id,status) VALUES ('b','needs_relogin')`)
	got, err = s.Choose(ctx, "ws", "pool", now.Add(time.Minute))
	if err != nil || got.ID != "a" {
		t.Fatalf("needs_relogin selected: %+v %v", got, err)
	}
}

func TestStoreConcurrentObservations(t *testing.T) {
	s, _, _ := poolFixture(t)
	ctx := context.Background()
	now := time.Now()
	const events = 20
	errs := make(chan error, events)
	var wg sync.WaitGroup
	for i := range events {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Same timestamp exercises distinct revisions and max-deadline
			// merging regardless of which concurrent transaction wins first.
			errs <- s.RecordObservation(ctx, "ws", "a", Observation{
				Generation: Generation("test-ciphertext-never-decrypted"),
				At:         now, CooldownUntil: now.Add(time.Duration(i+1) * time.Minute), Source: "provider_http",
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ReadObservation(ctx, "ws", "a")
	if err != nil || got.Revision != events || !got.CooldownUntil.Equal(now.Add(events*time.Minute)) {
		t.Fatalf("concurrent observation lost: %+v %v", got, err)
	}
}

func TestStoreLateOldTokenFailureCannotBlockReplacement(t *testing.T) {
	s, db, _ := poolFixture(t)
	ctx := context.Background()
	if err := s.Create(ctx, fixturePool()); err != nil {
		t.Fatal(err)
	}
	_, candidates, err := s.Snapshot(ctx, "ws", "pool")
	if err != nil || len(candidates) != 2 {
		t.Fatalf("snapshot: %v", err)
	}
	old := candidates[0].Generation
	if old == "" {
		t.Fatal("delivery snapshot omitted access-material generation")
	}
	// A failure from the old request arrives AFTER refresh/re-import.
	execPoolSQL(t, db, `UPDATE credentials SET encrypted_value = 'replacement-ciphertext' WHERE id = 'a'`)
	err = s.RecordObservation(ctx, "ws", "a", Observation{Generation: old, At: time.Now(), BlockedReason: "authentication", Source: "native_cli"})
	if !errors.Is(err, ErrSuperseded) {
		t.Fatal("late old-token failure was applied to the replacement login")
	}
	if _, err := s.ReadObservation(ctx, "ws", "a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale failure persisted: %v", err)
	}
	current, err := s.Choose(ctx, "ws", "pool", time.Now())
	if err != nil || current.ID != "a" || current.Generation == old {
		t.Fatalf("replacement not usable: %+v %v", current, err)
	}
	if err := s.RecordObservation(ctx, "ws", "a", Observation{At: time.Now(), BlockedReason: "authentication", Source: "native_cli"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing generation accepted: %v", err)
	}
	if err := s.RecordObservation(ctx, "ws", "a", Observation{Generation: current.Generation, At: time.Now(), BlockedReason: "authentication", Source: "native_cli"}); err != nil {
		t.Fatal(err)
	}
	current, err = s.Choose(ctx, "ws", "pool", time.Now())
	if err != nil || current.ID != "b" {
		t.Fatalf("current-generation failure ignored: %+v %v", current, err)
	}
}
