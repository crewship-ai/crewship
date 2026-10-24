//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Actual signed human HTTP admission, real CLOCK_BOOTTIME/owner and real Rust
// preparation. No listener/live actor, raw-human delta or UI is installed. The
// initial qualified document and typed command are explicit test fixtures.
func TestCollaborationHumanActualHTTPRustStoreAndRevocation(t *testing.T) {
	required := func(name string) string {
		t.Helper()
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("explicit integration requires %s", name)
		}
		return value
	}
	binary := required("MOORING_TEST_RUST_BINARY")
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != required("MOORING_TEST_RUST_SHA256") {
		t.Fatal("actual Rust binary digest mismatch")
	}
	if source, e := hex.DecodeString(required("MOORING_TEST_RUST_SOURCE")); e != nil || len(source) != 20 {
		t.Fatal("source provenance required")
	}
	checkpoint, err := os.ReadFile(required("MOORING_TEST_WORDCRAFT_CHECKPOINT"))
	if err != nil {
		t.Fatal(err)
	}
	sum = sha256.Sum256(checkpoint)
	if hex.EncodeToString(sum[:]) != required("MOORING_TEST_WORDCRAFT_CHECKPOINT_SHA256") {
		t.Fatal("initial checkpoint digest mismatch")
	}
	target := required("MOORING_TEST_WORDCRAFT_TARGET_ID")
	rig, unit, _, _, _ := collaborationAdmissionFixture(t)
	// Remove only this test's synthetic placeholder; bootstrap below uses the
	// actual qualified application and actual authenticated initializer authority.
	mustExec(t, rig.db, `DELETE FROM mooring_collab_documents WHERE page_id='admission-page'`)
	clock, err := mooring.NewBootOwnerClock()
	if err != nil {
		t.Fatal(err)
	}
	owners := mooring.NewOwnerStore(rig.db, true, clock, 30*time.Second)
	owner, err := owners.Acquire(t.Context(), unit.scope, "actual-http-rust-owner")
	if err != nil {
		t.Fatal(err)
	}
	versions := mooring.RuntimeVersions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}
	runtime, err := mooring.StartRuntime(t.Context(), mooring.RuntimeConfig{Enabled: true, Binary: binary, Versions: versions, ControlProfile: mooring.PrivateCausalPrepareProfile})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	fence, err := newCollaborationIngressFence()
	if err != nil {
		t.Fatal(err)
	}
	pages := NewPageHandler(rig.db, nil, newTestLogger())
	if _, err := newCollaborationHumanRegistry(true, pages, owners, runtime, clock, fence, unit.scope, versions, 8); err == nil {
		t.Fatal("causal child gained strict capability")
	}
	registry, err := newCollaborationCausalHumanRegistry(true, pages, owners, runtime, clock, fence, unit.scope, versions, 8)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := auth.NewJWTValidator("mooring-actual-http-test-signing-secret-32chars")
	if err != nil {
		t.Fatal(err)
	}
	sessionsStore := sessions.NewDBStore(rig.db)
	session, err := sessionsStore.Create(t.Context(), rig.userID, "test", "test", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.IssueAccessToken(rig.userID, session.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	middleware := NewAuthMiddleware(signer, sessionsStore, rig.db, newTestLogger())
	call := func(bearer string, action func(*http.Request) error) (int, error) {
		var actionErr error
		handler := fence.Wrap(middleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actionErr = action(r)
			if actionErr != nil {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/mooring/qualification", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code, actionErr
	}
	authority := func(ctx context.Context, tx *sql.Tx, access mooring.BatchAccess) error {
		if access.Intent == "initialize" {
			_, err := pages.authorizeCollaborationHumanPage(ctx, tx, access.Scope, "initialize")
			return err
		}
		return registry.authorizeBatch(ctx, tx, access)
	}
	store := mooring.NewBatchStore(rig.db, mooring.BatchStoreConfig{Enabled: true, Owners: owners, Authority: authority, Limits: mooring.DefaultBatchLimits(), Targets: &mooring.TargetStoreConfig{Enabled: true, ReadPolicy: registry.readPolicy, HistoryBudget: mooring.DefaultBatchHistoryBudget(), Adapter: registry.targetAdapter, Limits: mooring.DefaultBatchTargetLimits()}})
	status, err := call(token, func(r *http.Request) error {
		stamp, err := fence.capture(r.Context())
		if err != nil {
			return err
		}
		release, err := fence.acquire(r.Context(), stamp)
		if err != nil {
			return err
		}
		defer release()
		return store.Initialize(r.Context(), owner, checkpoint)
	})
	if err != nil || status != http.StatusNoContent {
		t.Fatal("actual initializer", status, err)
	}
	var ticket string
	var admission collaborationHumanAdmission
	status, err = call(token, func(r *http.Request) error {
		var err error
		ticket, admission, err = registry.issue(r.Context(), owner, true)
		return err
	})
	if err != nil || status != http.StatusNoContent {
		t.Fatal("actual signed admission", status, err)
	}
	joined, err := registry.join(t.Context(), runtime, owner, ticket, "actual-human-join")
	if err != nil {
		t.Fatal(err)
	}
	if joined.gate.PermissionVersion != admission.permission {
		t.Fatal("gate changed admission epoch")
	}
	if _, err = joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "actual-human-renew"); err != nil {
		t.Fatal(err)
	}
	zero := "0"
	payload, _ := json.Marshal(map[string]any{"kind": "text.insert", "target_id": target, "at": map[string]any{"kind": "end"}, "text": " actual-http Č😀", "properties": map[string]bool{"bold": true, "italic": false}})
	op := mooring.BatchOperation{ID: "actual-http-op", Participant: admission.participant, BaseRevision: "0", Dependencies: []string{}, Reads: []mooring.BatchReadPrecondition{{TargetID: target, Revision: "0", StructureRevision: &zero}}, Payload: payload}
	preimage, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": map[string]string{"application_id": "wordcraft", "artifact_id": unit.scope.ArtifactID, "workspace_id": unit.scope.WorkspaceID}, "change_id": "actual-http-change", "operations_without_fingerprints": []map[string]any{{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": json.RawMessage(payload)}}})
	hashes, err := mooring.FingerprintAtomicJSON(preimage)
	if err != nil {
		t.Fatal(err)
	}
	op.Fingerprint = hashes.Operations[op.ID]
	request := mooring.BatchRequest{Lease: owner, ExpectedRevision: "0", Changes: []mooring.BatchChange{{ID: "actual-http-change", MembershipHash: hashes.MembershipHash, Operations: []mooring.BatchOperation{op}}}}
	var result mooring.BatchResult
	status, err = call(token, func(r *http.Request) error {
		return registry.runHumanOperation(r, admission.participant, func(ctx context.Context) error { var err error; result, err = store.Commit(ctx, request); return err })
	})
	if err != nil || status != http.StatusNoContent || result.Revision != "1" || len(result.Changes) != 1 || result.Changes[0].Status != "saved" || result.Changes[0].Operations[0].Issuer != "crewship" {
		t.Fatalf("actual host Saved status=%d result=%+v error=%v", status, result, err)
	}
	var saved []byte
	status, err = call(token, func(r *http.Request) error {
		return registry.runHumanOperation(r, admission.participant, func(ctx context.Context) error {
			revision, document, err := store.ReadCheckpoint(ctx, unit.scope)
			if err == nil && revision != "1" {
				t.Error("wrong durable revision", revision)
			}
			saved = document
			return err
		})
	})
	if err != nil || status != http.StatusNoContent || bytes.Equal(saved, checkpoint) {
		t.Fatal("actual read", status, err)
	}
	// With the real engine dead, only actual host SQL can answer/replay receipts.
	// Close waits for child exit; the deliberate kill may report signal:killed.
	_ = runtime.Close()
	status, err = call(token, func(r *http.Request) error {
		return registry.runHumanOperation(r, admission.participant, func(ctx context.Context) error {
			receipt, err := store.Status(ctx, unit.scope, op.ID, op.Fingerprint)
			if err != nil {
				return err
			}
			if receipt == nil || receipt.Status != "saved" || receipt.Revision != "1" {
				t.Error("host-only receipt", receipt)
			}
			retry, err := store.Commit(ctx, request)
			if err == nil && (retry.Revision != "1" || retry.Changes[0].Operations[0].Revision != "1") {
				t.Error("immutable retry changed receipt", retry)
			}
			return err
		})
	})
	if err != nil || status != http.StatusNoContent {
		t.Fatal("host recovery after child death", status, err)
	}
	// Canonical source revocation closes the persisted gate in the same writer.
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), session.ID)
	var closed int
	if err := rig.db.QueryRow(`SELECT closed FROM mooring_delivery_gates WHERE id=?`, joined.gate.GateID).Scan(&closed); err != nil || closed != 1 {
		t.Fatal("source revocation left gate open", closed, err)
	}
	if _, err := joined.delivery.Renew(t.Context(), owner, joined.gate.GateID, "after-source-revoke"); err == nil {
		t.Fatal("revoked source renewed gate")
	}
	reached := false
	status, _ = call(token, func(*http.Request) error { reached = true; return nil })
	if status != http.StatusUnauthorized || reached {
		t.Fatal("old actual signed session accepted", status, reached)
	}
	var count int
	if err := rig.db.QueryRow(`SELECT count(*) FROM mooring_collab_operations`).Scan(&count); err != nil || count != 1 {
		t.Fatal("unexpected durable history", count, err)
	}
}
