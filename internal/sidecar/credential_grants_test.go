package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCredentialGrants_PerAgentLeaseAndRevocation(t *testing.T) {
	now := time.Now()
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "key", Provider: ProviderAnthropic, Token: "synthetic", LeaseExpiresAt: now.Add(-time.Hour).Format(time.RFC3339), AgentGrants: map[string]string{"a": now.Add(-time.Second).Format(time.RFC3339), "b": ""}, GraceToken: "previous", GraceExpiresAt: now.Add(time.Hour).Format(time.RFC3339)}})
	if cs.Select(ProviderAnthropic, "a") != nil || cs.Select(ProviderAnthropic, "") != nil {
		t.Fatal("expired or anonymous grant admitted")
	}
	if cs.Select(ProviderAnthropic, "b") == nil {
		t.Fatal("A's expiry disabled B")
	}
	if cs.ExpireLeases(now) != 0 {
		t.Fatal("credential-wide lease removed standing peer")
	}
	cs.RefreshAgentGrants(map[string]map[string]string{"key": {"a": ""}}, now)
	if cs.Select(ProviderAnthropic, "a") == nil || cs.Select(ProviderAnthropic, "b") != nil {
		t.Fatal("individual revocation was not applied")
	}
	if _, _, ok := cs.GraceFor("key", now, "b"); ok {
		t.Fatal("revoked agent can replay grace token")
	}
	cs.RefreshAgentGrants(map[string]map[string]string{}, now)
	if cs.Select(ProviderAnthropic, "a") != nil {
		t.Fatal("empty snapshot became crew-wide")
	}
}

func TestCredentialGrants_StalenessAndRecovery(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "key", Provider: ProviderAnthropic, Token: "synthetic", AgentGrants: map[string]string{"a": ""}}})
	cs.RefreshAgentGrants(map[string]map[string]string{"key": {"a": ""}}, time.Now().Add(-credentialGrantMaxAge-time.Second))
	if cs.Select(ProviderAnthropic, "a") != nil {
		t.Fatal("stale authority admitted")
	}
	cs.RefreshAgentGrants(map[string]map[string]string{"key": {"a": ""}}, time.Now())
	if cs.Select(ProviderAnthropic, "a") == nil {
		t.Fatal("fresh authority did not recover")
	}
	cs.RefreshAgentGrants(map[string]map[string]string{"key": {"a": "invalid"}}, time.Now())
	if cs.Select(ProviderAnthropic, "a") != nil {
		t.Fatal("invalid deadline admitted")
	}
}

func TestCredentialGrants_PollRejectsFailedAndMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		refreshed  bool
	}{
		{"revoked", `{"version":1,"grants":{"key":{"b":""}}}`, 200, true},
		{"outage", `{}`, 503, false}, {"missing", `{"version":1}`, 200, false},
		{"legacy", `[]`, 200, false}, {"bad version", `{"version":2,"grants":{}}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/internal/credential-grants" || r.URL.Query().Get("crew_id") != "crew" || r.Header.Get("X-Internal-Token") == "" {
					t.Error("incorrect authority request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer backend.Close()
			s := newJournalTestServer(backend.URL)
			s.ipc.CrewID = "crew"
			s.credStore = NewCredStore()
			s.credStore.Load([]Credential{{ID: "key", Provider: ProviderAnthropic, Token: "synthetic", AgentGrants: map[string]string{"a": ""}}})
			s.credStore.RefreshAgentGrants(map[string]map[string]string{"key": {"a": ""}}, time.Now().Add(-credentialGrantMaxAge-time.Second))
			s.refreshCredentialGrants(context.Background())
			if s.credStore.Select(ProviderAnthropic, "a") != nil {
				t.Fatal("old grant admitted")
			}
			if (s.credStore.Select(ProviderAnthropic, "b") != nil) != tc.refreshed {
				t.Fatal("unexpected refreshed grant")
			}
		})
	}
}
