package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Reap drops exactly the credentials whose IDs aren't in the keep set, and a
// dropped credential stops being served by Select.
func TestCredStore_Reap(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a", Provider: ProviderAnthropic, Token: "t1"},
		{ID: "b", Provider: ProviderOpenAI, Token: "t2"},
	})
	if removed := cs.Reap(map[string]struct{}{"a": {}}); removed != 1 {
		t.Fatalf("Reap removed = %d, want 1", removed)
	}
	if cs.Select(ProviderOpenAI) != nil {
		t.Error("reaped (revoked) openai credential is still served by Select")
	}
	if cs.Select(ProviderAnthropic) == nil {
		t.Error("kept anthropic credential was wrongly dropped")
	}
}

// The reaper drops a credential that crewshipd no longer lists (revoked →
// excluded from the metadata response), and keeps the ones still listed. This
// is the fix: a key revoked after boot stops being served within one interval.
func TestSidecar_ReapRevokedCredentials_DropsMissing(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") == "" {
			t.Error("reaper did not send X-Internal-Token")
		}
		if r.URL.Query().Get("workspace_id") != "wks_1" {
			t.Errorf("workspace_id = %q, want wks_1", r.URL.Query().Get("workspace_id"))
		}
		// "b" was revoked → excluded from the metadata list; only "a" is live.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"a","provider":"anthropic","status":"ACTIVE"}]`))
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.credStore = NewCredStore()
	s.credStore.Load([]Credential{
		{ID: "a", Provider: ProviderAnthropic, Token: "t1"},
		{ID: "b", Provider: ProviderOpenAI, Token: "t2"},
	})

	s.reapRevokedCredentials(context.Background())

	if s.credStore.Select(ProviderOpenAI) != nil {
		t.Error("revoked credential b is still served after reap")
	}
	if s.credStore.Select(ProviderAnthropic) == nil {
		t.Error("live credential a was wrongly reaped")
	}
}

// A fetch error (or non-200) must NOT reap — availability first: a transient
// crewshipd blip can't nuke valid keys with no way to re-fetch them.
func TestSidecar_ReapRevokedCredentials_FetchError_KeepsCreds(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer backend.Close()

	s := newJournalTestServer(backend.URL)
	s.credStore = NewCredStore()
	s.credStore.Load([]Credential{
		{ID: "a", Provider: ProviderAnthropic, Token: "t1"},
		{ID: "b", Provider: ProviderOpenAI, Token: "t2"},
	})

	s.reapRevokedCredentials(context.Background())

	if s.credStore.Select(ProviderAnthropic) == nil || s.credStore.Select(ProviderOpenAI) == nil {
		t.Error("a fetch error must not reap any credential")
	}
}
