package api

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

// A WordCraft command that no longer fits the aggregate artifact budget fails
// the SQL commit as storage capacity: the candidate is rejected, nothing is
// persisted or acknowledged, and the HTTP mapping answers 413
// artifact_storage_quota instead of the unconfirmed-outcome 409.
func TestCommandSQLAggregateStorageRefusalIsExplicit413(t *testing.T) {
	r := newCommandSQLRig(t, true)
	before := append([]byte(nil), r.p.p...)
	mustExec(t, r.f.r.db, `UPDATE artifact_storage_global SET max_bytes=used_bytes WHERE id=1`)
	_, err := r.s.Submit(r.request(), r.a, r.actor(), r.input("one", 0, "hello"))
	if err == nil || !errors.Is(artifacts.StorageBudgetError(err), artifacts.ErrStorageQuota) {
		t.Fatalf("storage refusal lost on the way out: %v", err)
	}
	rev, _, n := r.state(t)
	if rev != "0" || n != 0 || r.p.acks != 0 || r.p.rejects != 1 || r.p.k || !bytes.Equal(r.p.p, before) {
		t.Fatal("refused command polluted state", rev, n, r.p.acks, r.p.rejects)
	}
	w := httptest.NewRecorder()
	if !artifactStorageRefusal(w, err) || w.Code != 413 || !strings.Contains(w.Body.String(), `"artifact_storage_quota"`) {
		t.Fatalf("mapping %d %s", w.Code, w.Body)
	}
}
