package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

type proxyAuditFixtureTransport struct {
	upstream *url.URL
}

func (p proxyAuditFixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme, cloned.URL.Host, cloned.Host = p.upstream.Scheme, p.upstream.Host, p.upstream.Host
	return http.DefaultTransport.RoundTrip(cloned)
}

// Cross-component HTTP regression, not model/CLI authentication acceptance.
// The caller presents a real HMAC route token. Production sidecar identity,
// IPC middleware, admission SQL and audit persistence are all exercised.
func TestProxyAdmissionSignedRouteHTTPAudit(t *testing.T) {
	for _, scenario := range []string{"valid", "audit failure", "ended run", "tampered token"} {
		t.Run(scenario, func(t *testing.T) {
			r, _ := costRouter(t)
			db := r.db
			user := seedTestUser(t, db)
			ws := seedTestWorkspace(t, db, user)
			crew := seedCrewRow(t, db, "http-proxy-crew", ws, "Proxy", "proxy")
			agent := seedAgentRow(t, db, "http-proxy-agent", ws, crew, "A", "a", "AGENT")
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`INSERT INTO credentials(id,workspace_id,name,encrypted_value,type,provider,status,scope,created_by) VALUES('http-key',?,'http-key','synthetic','API_KEY','OPENAI','ACTIVE','CREW',?)`, ws, user)
			exec(`INSERT INTO agent_credentials(id,agent_id,credential_id,env_var_name) VALUES('http-grant',?,'http-key','OPENAI_API_KEY')`, agent)
			exec(`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('http-start',?,?,?,'run.started','orchestrator','fixture','{}','current-run')`, ws, crew, agent)
			if scenario == "ended run" {
				exec(`INSERT INTO journal_entries(id,workspace_id,crew_id,agent_id,entry_type,actor_type,summary,payload,trace_id) VALUES('http-ended',?,?,?,'run.completed','orchestrator','fixture','{}','current-run')`, ws, crew, agent)
			}
			if scenario == "audit failure" {
				exec(`CREATE TRIGGER http_audit_failure BEFORE INSERT ON credential_audit BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`)
			}
			internal := NewInternalHandler(db, bindTestMaster, testLogger())
			mux := http.NewServeMux()
			mux.Handle("POST /api/v1/internal/cost/admit", internal.requireInternal(http.HandlerFunc(r.handleSidecarCostAdmit)))
			host := httptest.NewServer(mux)
			defer host.Close()
			var upstreamCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				upstreamCalls.Add(1)
				if req.Header.Get("Authorization") != "Bearer synthetic-upstream-token" {
					t.Error("native proxy did not replace the route token")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"fixture","choices":[]}`)
			}))
			defer upstream.Close()
			upstreamURL, err := url.Parse(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			reservation, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := reservation.Addr().String()
			if err := reservation.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CREWSHIP_SIDECAR_STATE_DIR", t.TempDir())
			// Journal scope is checked by the actual admission endpoint below;
			// the separate registry-authority poller has its own qualification.
			t.Setenv("CREWSHIP_SIDECAR_RUN_AUTHORITY", "0")
			const fingerprint = "fixture-fingerprint"
			routeKey := internaltoken.DeriveLLMRouteKey(bindTestMaster, ws, crew)
			s := sidecar.NewServer(sidecar.ServerConfig{Addr: addr, Logger: testLogger(),
				ConfigFingerprint: fingerprint, RouteAuth: &sidecar.RouteAuth{Key: routeKey},
				ProxyTransport: proxyAuditFixtureTransport{upstreamURL},
				Credentials:    []sidecar.Credential{{ID: "http-key", Provider: sidecar.ProviderOpenAI, Token: "synthetic-upstream-token", AgentIDs: []string{agent}}},
				IPC:            &sidecar.IPCConfig{BaseURL: host.URL, Token: internaltoken.DeriveCrewToken(bindTestMaster, ws, crew), WorkspaceID: ws, CrewID: crew, AgentID: agent, RunID: "different-boot-run"},
			})
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			var serveErr error
			go func() { serveErr = s.Start(ctx); close(done) }()
			defer func() {
				cancel()
				select {
				case <-done:
					if serveErr != nil {
						t.Error(serveErr)
					}
				case <-time.After(7 * time.Second):
					t.Error("owned sidecar did not stop")
				}
			}()
			select {
			case <-s.Ready():
			case <-done:
				t.Fatal("sidecar startup", serveErr)
			case <-time.After(5 * time.Second):
				t.Fatal("sidecar never ready")
			}
			token := internaltoken.DeriveLLMRunRouteToken(routeKey, agent, "current-run")
			if scenario == "tampered token" {
				token += "x"
			}
			req, err := http.NewRequestWithContext(t.Context(), "POST", "http://"+addr+"/openai/v1/chat/completions", strings.NewReader(`{"model":"fixture","messages":[]}`))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer sk-dummy."+token+internaltoken.RouteFingerprintDelimiter+fingerprint)
			client := &http.Client{Timeout: 5 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			wantCode, wantCalls, wantAudit := http.StatusForbidden, int32(0), 0
			if scenario == "valid" {
				wantCode, wantCalls, wantAudit = http.StatusOK, 1, 1
			} else if scenario == "audit failure" {
				wantCode = http.StatusServiceUnavailable
			}
			var audits int
			if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE event_type='PROXY_ADMITTED' AND agent_id=? AND json_extract(metadata_json,'$.run_id')='current-run'`, agent).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != wantCode || upstreamCalls.Load() != wantCalls || audits != wantAudit {
				t.Fatalf("status=%d calls=%d audits=%d; want %d/%d/%d", resp.StatusCode, upstreamCalls.Load(), audits, wantCode, wantCalls, wantAudit)
			}
		})
	}
}
