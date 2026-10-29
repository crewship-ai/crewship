package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

// This is a cross-component HTTP unit integration, NOT a genuine provider,
// dispatcher, model or MCP-client acceptance. The confirmed task/runtime comes
// from the existing explicit SQL/unit fixture. Only the RPC transport, sidecar
// identity, crew IPC authentication, Router fence and bound manager are real.
func TestCraftNativeAgentSidecarHTTPAcquireCrossesActualInternalAuth(t *testing.T) {
	for _, held := range []bool{true, false} {
		t.Run(map[bool]string{true: "human holder refuses with recorded409", false: "free document admits agent with recorded200"}[held], func(t *testing.T) {
			f, m, human, _, engineCalls := nativeAgentToolFixture(t)
			master := f.pages.artifactMasterToken
			runKey := internaltoken.DeriveAgentRunKey(master, f.a.WorkspaceID, f.a.CrewID)
			bearer := internaltoken.DeriveAgentRunToken(runKey, f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			ipc := internaltoken.DeriveCrewToken(master, f.a.WorkspaceID, f.a.CrewID)
			if held {
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{}`)).WithContext(human)
				req.SetPathValue("slug", "@native-agent-page")
				w := httptest.NewRecorder()
				m.human.sessions(w, req)
				var admitted struct {
					Token string `json:"session_token"`
				}
				if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &admitted) != nil {
					t.Fatal("human admission", w.Code)
				}
				lease := craftNativeLeaseCall(m.human, human, admitted.Token, "@native-agent-page", "acquire", `{}`)
				if lease.Code != 200 {
					t.Fatal("human acquisition", lease.Code)
				}
			}
			// The same internal route/middleware chain as registerInternalRoutes,
			// on the actual Router.ServeHTTP -> serveInternal path. No IPC scope is
			// inserted into the incoming RPC context: requireInternal must derive it
			// from the actual crew-scoped header delivered by the sidecar.
			internal := NewInternalHandler(f.rig.db, master, newTestLogger())
			router := &Router{db: f.rig.db, logger: newTestLogger(), mux: newRouteMux()}
			router.mux.Handle("POST /api/v1/internal/craft-native/task", internal.requireInternal(withHostChatOrigin(http.HandlerFunc(m.toolCall))))
			var forwarded atomic.Int32
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && r.URL.Path == "/api/v1/internal/craft-native/task" {
					forwarded.Add(1)
					if r.Header.Get("X-Internal-Token") != ipc || r.Header.Get("Authorization") != "Bearer "+bearer {
						t.Error("actual crew IPC/run credentials changed across HTTP")
					}
					if InternalTokenWorkspaceFromContext(r.Context()) != "" || InternalTokenCrewFromContext(r.Context()) != "" {
						t.Error("fixture pre-injected IPC scope")
					}
				}
				router.ServeHTTP(w, r)
			}))
			defer host.Close()
			// Server has no exported Handler accessor. Exercise its production
			// constructor, HTTP listener and buildHandler on an owned ephemeral port.
			reservation, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			addr := reservation.Addr().String()
			if err = reservation.Close(); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CREWSHIP_SIDECAR_STATE_DIR", t.TempDir())
			t.Setenv("CREWSHIP_SIDECAR_RUN_AUTHORITY", "0") // Explicit unit identity fixture, not host runtime qualification.
			s := sidecar.NewServer(sidecar.ServerConfig{Addr: addr, Logger: newTestLogger(), IPC: &sidecar.IPCConfig{
				BaseURL: host.URL, Token: ipc, WorkspaceID: f.a.WorkspaceID, CrewID: f.a.CrewID, AgentID: f.a.AgentID, AgentSlug: "principal-agent", ChatID: f.a.ChatID, RunID: f.a.RunID, RunChatID: f.a.ChatID, AgentRunKey: runKey, ContainerID: "native-http-unit-owned",
			}})
			ctx, cancel := context.WithCancel(t.Context())
			// Closing completion broadcasts shutdown to both the startup select
			// and cleanup. A startup error cannot consume cleanup's only result.
			done := make(chan struct{})
			var serveErr error
			go func() { serveErr = s.Start(ctx); close(done) }()
			defer func() {
				cancel()
				select {
				case <-done:
					if serveErr != nil {
						t.Error("sidecar shutdown", serveErr)
					}
				case <-time.After(7 * time.Second):
					t.Error("owned sidecar did not shut down")
				}
			}()
			select {
			case <-s.Ready():
			case <-done:
				t.Fatal("owned sidecar startup", serveErr)
			case <-time.After(5 * time.Second):
				t.Fatal("owned sidecar never became ready")
			}
			rpc := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"craft_acquire","arguments":{}}}`
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://"+addr+"/mcp/craft-native", strings.NewReader(rpc))
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "Bearer "+bearer)
			request.Header.Set("Content-Type", "application/json")
			client := &http.Client{Timeout: 10 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
			response.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 {
				t.Fatalf("MCP transport denied before host tool: HTTP%d body=%s", response.StatusCode, raw)
			}
			var decoded struct {
				Error  json.RawMessage `json:"error"`
				Result struct {
					IsError bool `json:"isError"`
					Content []struct {
						Type string `json:"type"`
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if json.Unmarshal(raw, &decoded) != nil || len(decoded.Error) > 0 || len(decoded.Result.Content) != 1 {
				t.Fatalf("unexpected MCP envelope %s", raw)
			}
			want := http.StatusOK
			if held {
				want = http.StatusConflict
			}
			e := m.runs[f.a.RunID]
			e.callMu.Lock()
			var events []craftNativeAgentToolEvent
			if e.tool != nil {
				events = append(events, e.tool.events...)
			}
			e.callMu.Unlock()
			if len(events) != 1 || events[0].Operation != "acquire" || events[0].Status != want {
				t.Fatalf("actual bound host acquisition not recorded: want%d events=%+v MCP=%s", want, events, raw)
			}
			if forwarded.Load() != 1 {
				t.Fatal("sidecar did not forward exactly once", forwarded.Load())
			}
			if decoded.Result.IsError != held {
				t.Fatalf("MCP result did not preserve refusal=%v: %s", held, raw)
			}
			if held && (!strings.Contains(decoded.Result.Content[0].Text, `"holder_kind":"human"`) || events[0].HolderKind != "human") {
				t.Fatal("actual human-holder refusal lost", string(raw))
			}
			if *engineCalls != 0 {
				t.Fatal("acquire unexpectedly invoked original engine")
			}
			var head int64
			if err = f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&head); err != nil || head != 1 {
				t.Fatal("acquire changed native bytes/version", head, err)
			}
			t.Logf("Full sidecar HTTP+Router IPC unit fixture: held=%v actual acquire status=%d recorded=%d; not genuine agent acceptance", held, want, len(events))
		})
	}
}
