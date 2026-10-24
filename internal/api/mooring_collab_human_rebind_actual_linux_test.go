//go:build linux && mooring_rust_integration

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// Actual native process identities/profile checks and canonical FULL SQL.
// BOOTTIME and authenticated ingress are explicit unit fixtures in this test;
// the separate ActualHTTPRustStore test qualifies actual HTTP/BOOTTIME/storage.
func TestCollaborationHumanActualRuntimeReplacementPreservesAdmission(t *testing.T) {
	binary := os.Getenv("MOORING_TEST_RUST_BINARY")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("explicit pinned integration binary required", err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != os.Getenv("MOORING_TEST_RUST_SHA256") {
		t.Fatal("binary digest mismatch")
	}
	if source, err := hex.DecodeString(os.Getenv("MOORING_TEST_RUST_SOURCE")); err != nil || len(source) != 20 {
		t.Fatal("source provenance required")
	}
	rig, unit, owner, originalContext, clock := collaborationAdmissionFixture(t)
	versions := mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	start := func() *mooring.Runtime {
		t.Helper()
		runtime, err := mooring.StartRuntime(t.Context(), mooring.RuntimeConfig{Enabled: true, Binary: binary, Versions: versions, ControlProfile: mooring.PrivateCausalPrepareProfile})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = runtime.Close() })
		return runtime
	}
	first, second := start(), start()
	registry, err := newCollaborationCausalHumanRegistry(true, unit.pages, unit.owners, first, clock, unit.fence, unit.scope, versions, 8)
	if err != nil {
		t.Fatal(err)
	}
	sessionID := registry.hostSessionID()
	ticket, admission, err := registry.issue(originalContext, owner, true)
	if err != nil {
		t.Fatal(err)
	}
	old, err := registry.storeBinding()
	if err != nil {
		t.Fatal(err)
	}
	joined, err := registry.join(t.Context(), first, owner, ticket, "before-runtime-death")
	if err != nil {
		t.Fatal(err)
	}
	spareTicket, err := registry.reconnect(t.Context(), old, owner, admission.participant)
	if err != nil {
		t.Fatal(err)
	}
	check := func(binding collaborationHumanStoreBinding, want bool) {
		t.Helper()
		req := httptest.NewRequest("POST", "/", nil).WithContext(originalContext)
		err := registry.runHumanOperation(req, admission.participant, func(ctx context.Context) error {
			tx, err := rig.db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			return binding.authority(ctx, tx, mooring.BatchAccess{Intent: "outcome", Scope: unit.scope, Participant: admission.participant})
		})
		if (err == nil) != want {
			t.Fatalf("authority allowed=%v want=%v: %v", err == nil, want, err)
		}
	}
	check(old, true)
	forgedNewOwner := owner
	forgedNewOwner.Generation++
	if _, err = registry.replaceRuntime(t.Context(), second, forgedNewOwner); err == nil {
		t.Fatal("replaced a still-live Rust owner")
	}
	_ = first.Close() // waits for actual process death, not a copied ready body
	if _, err = registry.replaceRuntime(t.Context(), second, owner); err == nil {
		t.Fatal("same owner generation reused")
	}
	clock.now += time.Minute + time.Nanosecond
	newOwner, err := unit.owners.Acquire(t.Context(), unit.scope, "replacement-native-owner")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := registry.replaceRuntime(t.Context(), second, newOwner)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.runtime != second || fresh.id == old.id || registry.hostSessionID() != sessionID {
		t.Fatal("incorrect immutable runtime/session binding")
	}
	registry.mu.Lock()
	retained, exists := registry.admissions[admission.participant]
	registry.mu.Unlock()
	if !exists || retained != admission {
		t.Fatal("replacement refreshed original admission")
	}
	check(old, false) // even an all-known outcome lookup must reject the old store
	check(fresh, true)
	var closed bool
	var originalExpiry int64
	if err = rig.db.QueryRow(`SELECT closed,expires_ns FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&closed, &originalExpiry); err != nil {
		t.Fatal(err)
	}
	if !closed || originalExpiry >= int64(clock.now) {
		t.Fatal("old gate not retained closed/expired")
	}
	if _, err = registry.consume(second, newOwner, spareTicket, "stale-ticket"); err == nil {
		t.Fatal("pre-replacement ticket survived")
	}
	freshTicket, err := registry.reconnect(t.Context(), fresh, newOwner, admission.participant)
	if err != nil {
		t.Fatal(err)
	}
	newJoined, err := registry.join(t.Context(), second, newOwner, freshTicket, "after-runtime-death")
	if err != nil {
		t.Fatal(err)
	}
	if newJoined.admission != admission || newJoined.gate.GateID == joined.gate.GateID || newJoined.gate.PermissionVersion != admission.permission {
		t.Fatal("reconnect adopted new authority or old gate")
	}
	if _, err = registry.consume(second, newOwner, freshTicket, "ticket-replay"); err == nil {
		t.Fatal("replacement ticket reused")
	}
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), admission.source.ID)
	if _, err = registry.reconnect(t.Context(), fresh, newOwner, admission.participant); err == nil {
		t.Fatal("revoked original source refreshed during reconnect")
	}
}
