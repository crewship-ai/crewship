//go:build craft_native_engine_qualification

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/work"
)

// This is local HTTP/dispatch/real-engine qualification, NOT genuine model,
// provider, MCP process, browser or public acceptance. The provider stand-in
// consumes the actual dispatched request; it does not seed any run/attempt or
// publish a mandate. Original H1 identity and chat messages are local fixtures.
type nativeCLIHTTPQualificationRunner struct {
	manager  *craftNativeAgentManager
	endpoint string
	commands json.RawMessage
	busy     bool
	run      string
	receipt  mooring.NativeSaveReceipt
}

func (r *nativeCLIHTTPQualificationRunner) RunAgent(ctx context.Context, req orchestrator.AgentRunRequest, handler orchestrator.EventHandler) error {
	if handler != nil {
		return fmt.Errorf("private runtime installed content handler")
	}
	if err := req.ExecGate(ctx); err != nil {
		return err
	}
	r.manager.mu.Lock()
	premature := r.manager.runs[req.RunID] != nil
	r.manager.mu.Unlock()
	if premature {
		return fmt.Errorf("mandate published before provider confirmation")
	}
	r.run = req.RunID
	req.OnExecStarted("qualification-provider-stand-in-exec")
	r.manager.mu.Lock()
	entry := r.manager.runs[req.RunID]
	r.manager.mu.Unlock()
	if entry == nil {
		return fmt.Errorf("confirmed dispatcher attempt absent")
	}
	g := entry.grant
	// The normal runtime fixture transports the server's actual run-bound token
	// inside this explicitly fake provider; no identity is accepted from commands.
	token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(r.manager.human.pages.artifactMasterToken, g.Scope.WorkspaceID, g.CrewID), g.Scope.WorkspaceID, g.AgentID, req.RunID)
	call := func(packet any) (int, []byte, error) {
		return nativeCLIQualificationHTTP(ctx, r.endpoint, "POST", "/api/v1/internal/craft-native/task", token, "", packet)
	}
	code, raw, err := call(map[string]any{"operation": "acquire"})
	if err != nil {
		return err
	}
	if r.busy {
		if code != 409 || !bytes.Contains(raw, []byte(`"holder_kind":"human"`)) {
			return fmt.Errorf("human refusal: status%d", code)
		}
		return nil
	}
	if code != 200 {
		return fmt.Errorf("agent acquire: status%d %s", code, raw)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	if err = json.Unmarshal(raw, &lease); err != nil {
		return err
	}
	code, raw, err = call(map[string]any{"operation": "read"})
	if err != nil || code != 200 {
		return fmt.Errorf("agent read: status%d: %v", code, err)
	}
	var head struct {
		Version string `json:"version"`
	}
	if err = json.Unmarshal(raw, &head); err != nil || head.Version != "2" {
		return fmt.Errorf("agent base: %s %v", raw, err)
	}
	code, raw, err = call(map[string]any{"operation": "apply", "generation": lease.Generation, "base_version": head.Version, "request_id": "qualification-original-cli-edit", "commands": r.commands})
	if err != nil || code != 200 {
		return fmt.Errorf("original CLI apply: status%d %s %v", code, raw, err)
	}
	if err = json.Unmarshal(raw, &r.receipt); err != nil {
		return err
	}
	code, _, err = call(map[string]any{"operation": "release", "generation": lease.Generation})
	if err != nil || code != 200 {
		return fmt.Errorf("agent release: status%d %v", code, err)
	}
	return nil
}
func (*nativeCLIHTTPQualificationRunner) StopRun(context.Context, string) (bool, error) {
	return false, nil
}
func (*nativeCLIHTTPQualificationRunner) RunIsAlive(context.Context, string) (bool, error) {
	return false, nil
}
func nativeCLIQualificationHTTP(ctx context.Context, base, method, path, bearer, session string, packet any) (int, []byte, error) {
	var body io.Reader
	if packet != nil {
		raw, err := json.Marshal(packet)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if session != "" {
		req.Header.Set("X-Craft-Session", session)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 40<<20))
	return response.StatusCode, raw, err
}

func TestCraftNativeAgentHTTPOriginalCLIQualification(t *testing.T) {
	// These four CRAFT_QUALIFICATION_* inputs are test-only paths, never
	// startup configuration. Both immutable source/CLI and fixture hashes are
	// pinned below and checked before execution; missing inputs fail, not skip.
	paths := map[string]string{
		"wordCLI":     os.Getenv("CRAFT_QUALIFICATION_WORD_ENGINE"),
		"gridCLI":     os.Getenv("CRAFT_QUALIFICATION_GRID_ENGINE"),
		"wordFixture": os.Getenv("CRAFT_QUALIFICATION_WORD_FIRST"),
		"gridFixture": os.Getenv("CRAFT_QUALIFICATION_GRID_FIRST"),
	}
	for key, value := range paths {
		if value == "" {
			t.Fatalf("required qualification path %s missing", key)
		}
	}
	for _, pin := range []struct {
		profile    CraftNativeDocumentProfile
		fixture    string
		fixtureSHA string
	}{
		{CraftNativeDocumentProfile{Application: "wordcraft", EngineRevision: "91936b119a2b4d94025620ba1194cdee6a4adea3", CLIPath: paths["wordCLI"], CLISHA256: "b235d5251c166642d42d7fa17c604891215392200331b091e8605e8b8b42b975", EditorBundle: "87029ed59fa070b149df1d8f778450f7b388d353cba006f46686c31ddf31cf91"}, paths["wordFixture"], "155d73a6de7c738f6e353dfeaeac0361d3194f9182378abe3af1096eca89b30b"},
		{CraftNativeDocumentProfile{Application: "gridcraft", EngineRevision: "e55c948e2c2053c566e4c0c0429d7dd54a89c0e0", CLIPath: paths["gridCLI"], CLISHA256: "9445fae026a29838fbe66d2c9c20340aeef58a8e2ef13a40bd9c017569663a52", EditorBundle: "72390f6e22e479405e5045652fb68e87ca5c489d3baecea0743fc2d38b019a93"}, paths["gridFixture"], "3b0aa1b8780418c92d0e00a1302425674b9a7a62d58a1241ecaf6ec4cff411fd"},
	} {
		t.Run(pin.profile.Application, func(t *testing.T) {
			p := pin.profile
			initial, err := os.ReadFile(pin.fixture)
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprintf("%x", sha256.Sum256(initial)) != pin.fixtureSHA {
				t.Fatal("frozen qualification fixture hash mismatch")
			}
			adapter := NewCraftNativeAgentCLIAdapter()
			validator := mooring.NativeCLIValidator{Application: p.Application, BinaryPath: p.CLIPath, SHA256: p.CLISHA256}
			if err = validator.Validate(t.Context(), initial); err != nil {
				t.Fatal("pinned original input", err)
			}
			f, source := originalSessionFixture(t)
			f.rig.seedPage(t, "native-agent-page", "native-agent-page", "Native local qualification", source.UserID, "")
			// Create this owned Page's immutable profile once, at its real pin.
			mustExec(t, f.rig.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES('native-agent-page',?,?,?,?,?)`, f.rig.wsID, p.Application, p.EngineRevision, p.CLISHA256, p.EditorBundle)
			u := &AuthUser{ID: source.UserID, SessionID: source.ID, OriginalCredential: source}
			human := auth.WithOriginalCredential(context.WithValue(context.WithValue(context.WithValue(t.Context(), ctxUser, u), ctxAuthKind, AuthKindSession), ctxWorkspaceID, f.rig.wsID), *source)
			h := newCraftNativeHTTP(f.pages, true, &collaborationAdmissionTestClock{boot: "local-cli-qualification", now: time.Second})
			documents := newCraftNativeDocumentsHTTP(h, nil)
			m := newCraftNativeAgentManager(h, documents, CraftNativeHTTPConfig{AgentsEnabled: true})
			f.rig.db.SetMaxOpenConns(1)
			mustExec(t, f.rig.db, `PRAGMA synchronous=FULL`)
			cfg := mooring.NativeStoreConfig{Application: p.Application, EngineRevision: p.EngineRevision, EngineSHA256: p.CLISHA256, EditorBundle: p.EditorBundle, Validate: validator.Validate}
			m.documents.applications = map[string]craftNativeApplication{p.Application: {Store: cfg}}
			m.config.Profiles = map[string]CraftNativeDocumentProfile{p.Application: p}
			m.config.AgentApply = adapter.Apply
			m.config.AgentInspect = adapter.Inspect
			m.human.agents = m
			f.pages.artifactMasterToken = "local-http-qualification-master-not-a-provider-credential"
			versions := artifacts.NewVersionStore(f.rig.db, artifacts.VersionOptions{Enabled: true, ContentType: p.Application})
			if _, err = versions.Apply(human, artifacts.VersionKey{WorkspaceID: f.rig.wsID, PageID: "native-agent-page"}, artifacts.VersionRequest{EntryPoint: "document.json", Files: map[string]artifacts.VersionFile{"document.json": {Content: initial, MediaType: "application/json"}}}, func(context.Context, *sql.Tx) (string, error) { return "user:" + f.a.HumanUserID, nil }); err != nil {
				t.Fatal(err)
			}
			internal := NewInternalHandler(f.rig.db, f.pages.artifactMasterToken, newTestLogger())
			wireTestJournalForHandler(t, f.rig.db, internal)
			resolver := &nativeAgentRuntimeResolver{&collaborationRuntimeResolver{handler: internal, message: f.a.MessageID, info: &chatbridge.ChatInfo{AgentID: f.a.AgentID, AgentSlug: "principal-agent", AgentRole: "AGENT", CrewID: f.a.CrewID, CrewSlug: "principal-crew", WorkspaceID: f.a.WorkspaceID, CLIAdapter: "CLAUDE_CODE", TimeoutSecs: 15, ToolProfile: "MINIMAL"}}}
			runner := &nativeCLIHTTPQualificationRunner{manager: m, busy: true}
			wh := &WebhookHandler{db: f.rig.db, resolver: resolver, orch: runner, container: &nativeAgentRuntimeProvider{}, logger: newTestLogger()}
			runtime := &craftNativeAgentRuntime{WebhookRuntime: NewWebhookRuntime(wh), manager: m, environment: collaborationRuntimeEnvironment{}}
			dispatcher := dispatch.New(work.NewStore(f.rig.db), runtime, m, dispatch.Config{Owner: "local-real-cli-qualification", Kinds: []work.Kind{{Source: work.SourceChat, DomainKind: craftNativeAgentDomain}}, Limits: work.SerialAgentLimits()}, newTestLogger())
			m.runtime, m.dispatcher = runtime, dispatcher
			mux := http.NewServeMux()
			// HTTP handlers are real; the local original-human auth context is a fixture,
			// not a login/Router-authentication or genuine-provider qualification.
			humanRoute := func(pattern string, handler http.HandlerFunc) {
				mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { handler(w, r.WithContext(human)) })
			}
			humanRoute("POST /api/v1/pages/{slug}/craft/tasks", m.task)
			humanRoute("GET /api/v1/pages/{slug}/craft/tasks/{workID}", m.taskStatus)
			humanRoute("POST /api/v1/pages/{slug}/craft/sessions", m.human.sessions)
			humanRoute("POST /api/v1/pages/{slug}/craft/lease/acquire", m.human.acquire)
			humanRoute("POST /api/v1/pages/{slug}/craft/lease/release", m.human.release)
			humanRoute("POST /api/v1/pages/{slug}/craft/document/save", m.documents.saveSnapshot)
			humanRoute("GET /api/v1/pages/{slug}/craft/document", m.documents.readSnapshot)
			mux.HandleFunc("POST /api/v1/internal/craft-native/task", func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(context.WithValue(r.Context(), ctxInternalTokenWS, f.rig.wsID), ctxInternalTokenCrew, f.a.CrewID)
				m.toolCall(w, r.WithContext(ctx))
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			runner.endpoint = server.URL
			call := func(method, op, session string, packet any, want int) []byte {
				t.Helper()
				code, raw, e := nativeCLIQualificationHTTP(t.Context(), server.URL, method, "/api/v1/pages/@native-agent-page/craft/"+op, "", session, packet)
				if e != nil || code != want {
					t.Fatalf("HTTP%s %s: status%d %s %v", method, op, code, raw, e)
				}
				return raw
			}
			var session struct {
				Token string `json:"session_token"`
				ID    string `json:"session_id"`
			}
			if json.Unmarshal(call("POST", "sessions", "", map[string]any{}, 201), &session) != nil || session.Token == "" {
				t.Fatal("H1 admission missing")
			}
			var humanLease struct {
				Generation string `json:"generation"`
			}
			if json.Unmarshal(call("POST", "lease/acquire", session.Token, map[string]any{}, 200), &humanLease) != nil {
				t.Fatal("H1 lease missing")
			}
			var humanCommands json.RawMessage
			if p.Application == "wordcraft" {
				humanCommands = json.RawMessage(`[{"command":"caret.docEnd","params":{}},{"command":"text.insert","params":{"text":" HumanHTTPQualification"}}]`)
				runner.commands = json.RawMessage(`[{"command":"caret.docEnd","params":{}},{"command":"text.insert","params":{"text":" AgentHTTPQualification"}}]`)
			} else {
				humanCommands = json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":"HumanHTTPQualification"}}]`)
				runner.commands = json.RawMessage(`[{"command":"cell.set","params":{"cell":"B1","input":"AgentHTTPQualification"}}]`)
			}
			humanBytes, err := adapter.Apply(t.Context(), p, initial, humanCommands)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(humanBytes)
			enginePin := mooring.NewNativeStore(f.rig.db, cfg).EnginePin()
			humanPacket := craftNativeSnapshotBody{WorkspaceID: f.rig.wsID, PageID: "native-agent-page", ApplicationID: p.Application, EnginePin: enginePin, SessionID: session.ID, Generation: humanLease.Generation, BaseVersion: "1", EditorRevision: "h1-original-cli", ContentHash: fmt.Sprintf("sha256:%x", sum), Bytes: humanBytes}
			var humanReceipt mooring.NativeSaveReceipt
			if json.Unmarshal(call("POST", "document/save", session.Token, humanPacket, 200), &humanReceipt) != nil || humanReceipt.Version != "2" {
				t.Fatal("actual human receipt missing")
			}
			var previousRun string
			for index, message := range []string{f.a.MessageID, "principal-next-message"} {
				var accepted struct {
					WorkID string `json:"work_id"`
				}
				if json.Unmarshal(call("POST", "tasks", "", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: message, WholeDocument: true}, 202), &accepted) != nil || accepted.WorkID == "" {
					t.Fatal("actual HTTP task absent")
				}
				resolver.original = m.entries[accepted.WorkID].grant.Original
				resolver.message = message
				worked, e := dispatcher.RunOne(t.Context())
				if e != nil || !worked {
					t.Fatal("normal dispatcher", worked, e)
				}
				if runner.run == "" || runner.run == previousRun {
					t.Fatal("fresh dispatcher run missing")
				}
				previousRun = runner.run
				item, e := work.NewStore(f.rig.db).Get(t.Context(), accepted.WorkID)
				want := work.StateFailed
				if index == 1 {
					want = work.StateSucceeded
				}
				if e != nil || item.State != want {
					t.Fatalf("actual work outcome=%v error=%v", item, e)
				}
				var status struct {
					State   string                      `json:"state"`
					Events  []craftNativeAgentToolEvent `json:"events"`
					Receipt *mooring.NativeSaveReceipt  `json:"last_committed_receipt"`
				}
				if json.Unmarshal(call("GET", "tasks/"+accepted.WorkID, "", nil, 200), &status) != nil {
					t.Fatal("task status absent")
				}
				if index == 0 {
					if status.Receipt != nil || len(status.Events) != 1 || status.Events[0].Operation != "acquire" || status.Events[0].Status != 409 || status.Events[0].HolderKind != "human" {
						t.Fatal("actual refusal not proven", status)
					}
					var unchanged craftNativeDocumentResponse
					if json.Unmarshal(call("GET", "document", session.Token, nil, 200), &unchanged) != nil || unchanged.Version != "2" || !bytes.Equal(unchanged.Bytes, humanBytes) {
						t.Fatal("busy task changed native bytes")
					}
					call("POST", "lease/release", session.Token, map[string]string{"generation": humanLease.Generation}, 200)
					runner.busy = false
				} else {
					receipt := runner.receipt
					if status.Receipt == nil || *status.Receipt != receipt || receipt.Version != "3" || receipt.BaseVersion != "2" || receipt.Phase != "committed" || receipt.Durability != "node" || receipt.IssuedBy != "crewship" || receipt.SessionID != "native-agent:"+runner.run || receipt.EnginePin != enginePin || receipt.WorkspaceID != f.rig.wsID || receipt.PageID != "native-agent-page" || receipt.ApplicationID != p.Application {
						t.Fatal("exact actual Node receipt absent", receipt, status)
					}
					var fresh craftNativeDocumentResponse
					if json.Unmarshal(call("GET", "document", session.Token, nil, 200), &fresh) != nil || fresh.Version != receipt.Version {
						t.Fatal("fresh H1 version absent")
					}
					generation, generationErr := strconv.ParseUint(receipt.Generation, 10, 64)
					humanGeneration, humanGenerationErr := strconv.ParseUint(humanReceipt.Generation, 10, 64)
					if generationErr != nil || humanGenerationErr != nil || generation <= humanGeneration {
						t.Fatal("actual agent lease did not advance the human generation")
					}
					if p.Application == "wordcraft" {
						before, beforeErr := adapter.Inspect(t.Context(), p, initial, json.RawMessage(`{"command":"text","params":{}}`))
						after, afterErr := adapter.Inspect(t.Context(), p, fresh.Bytes, json.RawMessage(`{"command":"text","params":{}}`))
						var original, current struct{ Text string }
						if beforeErr != nil || afterErr != nil || json.Unmarshal(before, &original) != nil || json.Unmarshal(after, &current) != nil || current.Text != original.Text+" HumanHTTPQualification AgentHTTPQualification" {
							t.Fatal("original engine text read did not preserve the entire prior Word body and both exact edits")
						}
					}
					var actor string
					if err = f.rig.db.QueryRow(`SELECT actor FROM artifact_versions WHERE page_id='native-agent-page' AND version=3`).Scan(&actor); err != nil || actor != "agent:"+f.a.AgentID {
						t.Fatal("actual version lost original agent attribution", actor, err)
					}
					projection, owned, projectionErr := work.NewStore(f.rig.db).RunProjection(t.Context(), runner.run)
					if projectionErr != nil || !owned || !projection.Ready || projection.Status != "COMPLETED" || projection.Result.Metadata["native_saved"] != true || projection.Result.Metadata["native_version"] != "3" {
						t.Fatal("normal dispatcher did not project only the actual qualified saved version", projectionErr)
					}
					hash := fmt.Sprintf("sha256:%x", sha256.Sum256(fresh.Bytes))
					if hash != receipt.CandidateHash || hash != receipt.ContentHash {
						t.Fatal("fresh H1 bytes differ from receipt")
					}
					if err = validator.Validate(t.Context(), fresh.Bytes); err != nil {
						t.Fatal("original native validator", err)
					}
					for _, check := range []struct {
						query  json.RawMessage
						marker string
					}{{json.RawMessage(`{"command":"text","params":{}}`), "HumanHTTPQualification"}, {json.RawMessage(`{"command":"text","params":{}}`), "AgentHTTPQualification"}} {
						if p.Application == "gridcraft" {
							cell := "A1"
							if strings.HasPrefix(check.marker, "Agent") {
								cell = "B1"
							}
							check.query = json.RawMessage(fmt.Sprintf(`{"command":"cell.get","params":{"cell":%q}}`, cell))
						}
						inspected, e := adapter.Inspect(t.Context(), p, fresh.Bytes, check.query)
						if e != nil || !bytes.Contains(inspected, []byte(check.marker)) {
							t.Fatal("original engine read lost human/agent marker", e)
						}
					}
					t.Logf("LOCAL HTTP qualification provider=TEST-STAND-IN app=%s originalCLI=%s run=%s version=%s hash=%s; no genuine model/MCP-process/browser claim", p.Application, p.CLISHA256, runner.run, receipt.Version, hash)
				}
			}
		})
	}
}
