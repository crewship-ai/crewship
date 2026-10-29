//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"golang.org/x/net/websocket"
)

// Real HTTP authentication, WS, SDK, host-tracked physical Leave write and FULL SQL.
// The retirement proof establishes FD3 write, not a native close ACK.
// Original agent ingress/confirmed execution remain controlled fixtures, not a
// dispatched model/container or a live acceptance by two actual people.
func testMooringAgentActualBoundedRevocation(t *testing.T) {
	for _, mode := range []string{"revoke", "reader"} {
		t.Run(mode, func(t *testing.T) {
			withMooringAgentActualProductionSaved(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, d *collaborationHTTPDocument, a *collaborationLiveAgent) {
				router, ownerToken := mooringBoundedAgentRouter(t, x, m)
				server := httptest.NewServer(router.mux)
				defer server.Close()
				client := server.Client()
				client.Timeout = 5 * time.Second
				var windowDeadline time.Time
				request := func(method, path string, body []byte, agent bool) (int, []byte) {
					t.Helper()
					ctx := t.Context()
					if !windowDeadline.IsZero() {
						var cancel context.CancelFunc
						ctx, cancel = context.WithDeadline(ctx, windowDeadline)
						defer cancel()
					}
					r, err := http.NewRequestWithContext(ctx, method, server.URL+path, bytes.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					r.Header.Set("Content-Type", "application/json")
					if agent {
						r.Header.Set("X-Internal-Token", internaltoken.DeriveCrewToken(x.f.pages.artifactMasterToken, x.key.WorkspaceID, x.f.a.CrewID))
						r.Header.Set("Authorization", x.request().Header.Get("Authorization"))
					} else {
						r.Header.Set("Authorization", "Bearer "+ownerToken)
					}
					response, err := client.Do(r)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					raw, err := io.ReadAll(response.Body)
					if err != nil {
						t.Fatal(err)
					}
					return response.StatusCode, raw
				}
				toolPath := "/api/v1/internal/mooring/task"
				status, raw := request(http.MethodGet, toolPath, nil, true)
				var view struct {
					Revision string                       `json:"revision"`
					BlockIDs []string                     `json:"block_ids"`
					Targets  []mooring.BatchTargetVersion `json:"targets"`
				}
				if status != 200 || json.Unmarshal(raw, &view) != nil || view.Revision != "1" || len(view.BlockIDs) == 0 {
					t.Fatalf("network agent positive read missing: HTTP%d %s", status, raw)
				}
				positive := mooringBoundedAgentOriginal(t, x, d, a, "bounded-agent-positive", view.Revision, view.BlockIDs[0], view.Targets)
				status, raw = request(http.MethodPost, toolPath, positive, true)
				var positiveSaved protocol.CommitResult
				if status != 200 || json.Unmarshal(raw, &positiveSaved) != nil || positiveSaved.Type != "commit_result" || positiveSaved.Revision != "2" || len(positiveSaved.Changes) != 1 || positiveSaved.Changes[0].Status != "saved" {
					t.Fatalf("network agent positive Save missing: HTTP%d %s", status, raw)
				}
				status, raw = request(http.MethodGet, toolPath, nil, true)
				if status != 200 || json.Unmarshal(raw, &view) != nil || view.Revision != "2" || len(view.BlockIDs) == 0 {
					t.Fatal("network agent fresh pre-withdrawal basis missing")
				}
				// Prepare the denied original before withdrawal. Its bytes, base and
				// fingerprints are never rebuilt using a later permission or revision.
				retained := mooringBoundedAgentOriginal(t, x, d, a, "bounded-agent-original", view.Revision, view.BlockIDs[0], view.Targets)
				owner, ownPacket := mooringBoundedOwner(t, server.URL, ownerToken, x, d, request)
				defer owner.Close()
				grantPath := "/api/v1/pages/agent-shared-page/grants?workspace_id=" + x.key.WorkspaceID
				// A healthy, active original must exist immediately before withdrawal;
				// an already retired binding cannot satisfy the timing oracle.
				a.mu.Lock()
				live := a.joined && !a.closed && a.retired == nil && a.binding != nil
				gate := a.gate.GateID
				a.mu.Unlock()
				var closed bool
				var boot string
				var expires int64
				if err := x.f.rig.db.QueryRow(`SELECT closed,boot_id,expires_ns FROM mooring_delivery_gates WHERE id=?`, gate).Scan(&closed, &boot, &expires); err != nil {
					t.Fatal(err)
				}
				current, now, err := m.clock.Now()
				if !live || err != nil || closed || boot != current || expires <= int64(now) {
					t.Fatal("agent not live before timed withdrawal")
				}
				checkGrants := func() {
					t.Helper()
					var readRows, writeRows int
					if err := x.f.rig.db.QueryRow(`SELECT count(*) FILTER(WHERE level='read'),count(*) FILTER(WHERE level='write') FROM page_grants WHERE page_id=? AND subject_type='agent' AND subject_id=?`, x.key.ArtifactID, x.f.a.AgentID).Scan(&readRows, &writeRows); err != nil {
						t.Fatal(err)
					}
					wantRead := 0
					if mode == "reader" {
						wantRead = 1
					}
					if readRows != wantRead || writeRows != 0 {
						t.Fatalf("canonical withdrawal level mismatch: read=%d write=%d", readRows, writeRows)
					}
				}
				var partial int
				var workLease string
				if err := x.f.rig.db.QueryRow(`SELECT t.allow_partial,a.lease_expires_at FROM mooring_collab_tasks t JOIN work_attempts a ON a.work_id=t.task_id WHERE t.page_id=? AND t.task_id=? AND a.run_id=?`, x.key.ArtifactID, x.workID, x.f.a.RunID).Scan(&partial, &workLease); err != nil || partial != 1 {
					t.Fatalf("original declared partial policy missing: %d %v", partial, err)
				}
				workExpiry, err := time.Parse(time.RFC3339Nano, workLease)
				if err != nil {
					t.Fatal(err)
				}
				changedAt := time.Now()
				deadline := changedAt.Add(5 * time.Second)
				if !workExpiry.After(deadline) {
					t.Fatal("original task lease would expire during withdrawal oracle")
				}
				windowDeadline = deadline
				if status, _ := request(http.MethodDelete, grantPath+"&subject_type=agent&subject="+x.f.a.AgentID+"&level=write", nil, false); status != 200 {
					t.Fatalf("ordinary agent revoke failed: %d", status)
				}
				checkGrants()
				if status, raw := request(http.MethodPost, toolPath, retained, true); (status != http.StatusForbidden && status != http.StatusConflict) || bytes.Contains(raw, []byte(`"saved"`)) {
					t.Fatalf("revoked original accepted: HTTP%d %s", status, raw)
				}
				// Send the independent owner's actual SDK original inside the same
				// absolute window. No wait for the revoked agent or lease extension.
				_ = owner.SetWriteDeadline(deadline)
				if err := websocket.Message.Send(owner, string(ownPacket)); err != nil {
					t.Fatal(err)
				}
				if status, raw := request(http.MethodGet, toolPath, nil, true); status != http.StatusForbidden || bytes.Contains(raw, []byte(`"native"`)) || bytes.Contains(raw, []byte("native_json")) {
					t.Fatalf("revoked agent received content: HTTP%d", status)
				}
				saved, updated := false, false
				_ = owner.SetReadDeadline(deadline)
				for !saved || !updated {
					var frame []byte
					if err := websocket.Message.Receive(owner, &frame); err != nil {
						t.Fatalf("owner continuation failed inside five seconds: %v runtime=%v", err, d.cell.runtime.Err())
					}
					var header struct {
						Type string `json:"type"`
					}
					_ = json.Unmarshal(frame, &header)
					switch header.Type {
					case "changes_received":
					case "commit_result":
						var receipt protocol.CommitResult
						if json.Unmarshal(frame, &receipt) != nil || receipt.RequestID != "bounded-owner-request" || receipt.Revision != "3" || len(receipt.Changes) != 1 || receipt.Changes[0].Status != "saved" {
							t.Fatalf("owner not actually Saved: %s", frame)
						}
						saved = true
					case "document_update":
						var update protocol.DocumentUpdate
						if json.Unmarshal(frame, &update) != nil || update.FromRevision != "2" || update.ToRevision != "3" {
							t.Fatalf("owner native cut missing: %s", frame)
						}
						updated = true
					default:
						t.Fatalf("unexpected owner continuation frame: %s", frame)
					}
				}
				ownerSavedAt := time.Now()
				if !ownerSavedAt.Before(deadline) {
					t.Fatal("owner Saved exceeded absolute five-second window")
				}
				for {
					a.mu.Lock()
					retired := a.closed && a.retired != nil && a.binding == nil
					a.mu.Unlock()
					if !time.Now().Before(deadline) {
						t.Fatal("host did not physically write agent Leave within five seconds")
					}
					if retired {
						break
					}
					time.Sleep(time.Millisecond)
				}
				retiredAt := time.Now()
				// Cover the entire original horizon before the definitive SQL oracle.
				if remaining := time.Until(deadline); remaining > 0 {
					timer := time.NewTimer(remaining)
					<-timer.C
				}
				var all, denied int
				if err := x.f.rig.db.QueryRow(`SELECT count(*),count(*) FILTER (WHERE op_id='bounded-agent-original-op') FROM mooring_collab_operations WHERE page_id=?`, x.key.ArtifactID).Scan(&all, &denied); err != nil || all != 3 || denied != 0 {
					t.Fatalf("withdrawn original reached FULL SQL: all=%d denied=%d err=%v", all, denied, err)
				}
				checkGrants()
				windowDeadline = time.Time{}
				if status, raw := request(http.MethodGet, toolPath, nil, true); status != http.StatusForbidden || bytes.Contains(raw, []byte("native_json")) {
					t.Fatal("same original agent run reconnected after retirement")
				}
				if err := d.cell.runtime.Err(); err != nil {
					t.Fatalf("agent-only withdrawal killed owner's process: %v", err)
				}
				t.Logf("actual agent %s: owner Saved=%s, host-tracked physical Leave write=%s, full five-second SQL window clean", mode, ownerSavedAt.Sub(changedAt), retiredAt.Sub(changedAt))
			}, mooringAgentSavedFixtureOptions{allowPartial: true, readerGrant: mode == "reader"})
		})
	}
}

// Bind the production handlers and their original authentication middleware to
// the already bootstrapped manager. No context/user/crew is injected by the test
// server: signed session and crew/run tokens traverse the ordinary middleware.
func mooringBoundedAgentRouter(t *testing.T, x *collaborationAgentTestFixture, m *collaborationHTTPManager) (*Router, string) {
	t.Helper()
	v, err := auth.NewJWTValidator("bounded-agent-revocation-fixture-32characters")
	if err != nil {
		t.Fatal(err)
	}
	token, err := v.IssueAccessToken(x.f.a.HumanUserID, "original-session", "", "")
	if err != nil {
		t.Fatal(err)
	}
	r := &Router{db: x.f.rig.db, pages: m.pages, logger: newTestLogger(), mux: newRouteMux(), authMw: NewAuthMiddleware(v, sessions.NewDBStore(x.f.rig.db), x.f.rig.db, newTestLogger()), mooringHTTP: m}
	r.registerPageRoutes()
	r.authedMut("POST", "/api/v1/pages/{slug}/mooring/session", roleSelf, m.session, m.fence.Wrap)
	r.mux.Handle("GET /api/v1/pages/{slug}/mooring/ws", m.fence.Wrap(r.authMw.RequireAuth(r.authMw.RequireWorkspace(r.providerAccountPolicy(http.HandlerFunc(m.websocket))))))
	internal := NewInternalHandler(x.f.rig.db, x.f.pages.artifactMasterToken, newTestLogger())
	r.registerMooringAgentTaskRoutes(func(next http.Handler) http.Handler {
		return internal.requireInternal(withHostChatOrigin(next))
	})
	return r, token
}

func mooringBoundedAgentOriginal(t *testing.T, x *collaborationAgentTestFixture, d *collaborationHTTPDocument, a *collaborationLiveAgent, id, revision, target string, targets []mooring.BatchTargetVersion) []byte {
	t.Helper()
	change := submissionTestChange(t, x.key, id, x.workID, a.participant.id)
	op := &change.Operations[0]
	op.ID = id + "-op"
	op.BaseRevision = revision
	for _, v := range targets {
		if v.TargetID == target {
			op.Reads = []mooring.BatchReadPrecondition{{TargetID: target, Revision: v.Revision}}
		}
	}
	op.Payload, _ = json.Marshal(map[string]any{"kind": "text.insert", "target_id": target, "at": map[string]string{"kind": "end"}, "text": " " + id})
	preimage, _ := json.Marshal(map[string]any{"domain": "mooring.atomic.v0.4", "scope": d.wireScope(), "change_id": change.ID, "task_id": change.TaskID, "operations_without_fingerprints": []any{map[string]any{"op_id": op.ID, "participant_ref": op.Participant, "base_revision": op.BaseRevision, "dependencies": op.Dependencies, "read_preconditions": op.Reads, "payload": op.Payload}}})
	hashes, err := mooring.FingerprintAtomicJSON(preimage)
	if err != nil {
		t.Fatal(err)
	}
	change.MembershipHash = hashes.MembershipHash
	op.Fingerprint = hashes.Operations[op.ID]
	encoded, _ := json.Marshal([]mooring.BatchChange{change})
	var changes []protocol.AtomicChange
	if err := json.Unmarshal(encoded, &changes); err != nil {
		t.Fatal(err)
	}
	task := protocol.Identifier(x.workID)
	packet, err := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: protocol.Identifier(id + "-request"), Scope: d.wireScope(), SessionID: protocol.Identifier(d.activation.SessionID()), Versions: d.wireVersions(), TaskID: &task, Changes: changes})
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

type mooringBoundedHTTPRequest func(string, string, []byte, bool) (int, []byte)

func mooringBoundedOwner(t *testing.T, base, token string, x *collaborationAgentTestFixture, d *collaborationHTTPDocument, call mooringBoundedHTTPRequest) (*websocket.Conn, []byte) {
	t.Helper()
	status, raw := call(http.MethodPost, "/api/v1/pages/agent-shared-page/mooring/session?workspace_id="+x.key.WorkspaceID, []byte(`{"write":true}`), false)
	var opening struct {
		Session     string            `json:"session_id"`
		Participant string            `json:"participant_ref"`
		Ticket      string            `json:"ticket"`
		Ref         string            `json:"connection_ref"`
		Origin      string            `json:"origin"`
		Revision    string            `json:"checkpoint_revision"`
		WSPath      string            `json:"websocket_path"`
		Versions    protocol.Versions `json:"versions"`
	}
	if status != 200 || json.Unmarshal(raw, &opening) != nil || opening.Revision != "2" {
		t.Fatalf("owner bootstrap at saved revision failed: HTTP%d", status)
	}
	config, err := websocket.NewConfig("ws"+strings.TrimPrefix(base, "http")+opening.WSPath+"&connection_ref="+opening.Ref, base)
	if err != nil {
		t.Fatal(err)
	}
	config.Header.Set("Authorization", "Bearer "+token)
	ws, err := websocket.DialConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	_ = ws.SetDeadline(time.Now().Add(8 * time.Second))
	join, _ := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: "bounded-owner-join", Ticket: opening.Ticket, Versions: opening.Versions})
	if err := websocket.Message.Send(ws, string(join)); err != nil {
		t.Fatal(err)
	}
	var joined []byte
	if err := websocket.Message.Receive(ws, &joined); err != nil {
		t.Fatal(err)
	}
	var j protocol.SessionJoined
	if json.Unmarshal(joined, &j) != nil || j.Type != "session_joined" || string(j.ParticipantRef) != opening.Participant {
		t.Fatal("owner native Joined missing")
	}
	read, _ := json.Marshal(protocol.CheckpointRequest{Type: "checkpoint_request", RequestID: "bounded-owner-read", Scope: d.wireScope(), Revision: protocol.Counter(opening.Revision)})
	if err := websocket.Message.Send(ws, string(read)); err != nil {
		t.Fatal(err)
	}
	var cpRaw []byte
	if err := websocket.Message.Receive(ws, &cpRaw); err != nil {
		t.Fatal(err)
	}
	var cp protocol.CheckpointResult
	if json.Unmarshal(cpRaw, &cp) != nil || cp.Type != "checkpoint_result" {
		t.Fatal("owner native checkpoint missing")
	}
	dir := t.TempDir()
	for _, input := range []struct{ env, name, pin string }{{"MOORING_SDK_JS", "sdk.mjs", "MOORING_SDK_JS_SHA256"}, {"MOORING_SDK_WASM", "sdk.wasm", "MOORING_SDK_WASM_SHA256"}} {
		data, err := os.ReadFile(os.Getenv(input.env))
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != mooringActualFixtureSHA(t, input.pin) {
			t.Fatal("owner SDK pin mismatch", err)
		}
		if err := os.WriteFile(filepath.Join(dir, input.name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	input, _ := json.Marshal(map[string]any{"joined": json.RawMessage(joined), "checkpoint": cp.Checkpoint, "origin": opening.Origin})
	if err := os.WriteFile(filepath.Join(dir, "input.json"), input, 0600); err != nil {
		t.Fatal(err)
	}
	const script = `import fs from 'node:fs';import * as sdk from './sdk.mjs';
await sdk.default({module_or_path:fs.readFileSync(new URL('./sdk.wasm',import.meta.url))});
const i=JSON.parse(fs.readFileSync(new URL('./input.json',import.meta.url)));
const cp=sdk.validate_host_bootstrap(JSON.stringify(i.joined),JSON.stringify(i.checkpoint));
const draft=new sdk.WordCraftLocalDraft(cp,i.origin,'writer');
const proposal=draft.edit('bounded-owner-op',JSON.stringify({kind:'text.insert',target_id:'qualified-d1-0',at:{kind:'start'},text:' bounded owner still Saves ',properties:{bold:false,italic:true}}));
const payload=JSON.parse(proposal.human_payload_json(draft.optimistic_checkpoint()));
const a={change_id:'bounded-owner-change',membership_hash:'sha256:'+'0'.repeat(64),operations:[{op_id:'bounded-owner-op',fingerprint:'sha256:'+'0'.repeat(64),participant_ref:i.joined.participant_ref,base_revision:i.checkpoint.revision,dependencies:[],read_preconditions:[],payload}]};
process.stdout.write(JSON.stringify(JSON.parse(sdk.bind_agent_change(JSON.stringify(i.joined.scope),JSON.stringify(a)))));`
	if err := os.WriteFile(filepath.Join(dir, "generate.mjs"), []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.CommandContext(t.Context(), "node", filepath.Join(dir, "generate.mjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("owner actual SDK: %v %s", err, output)
	}
	var atomic protocol.AtomicChange
	if json.Unmarshal(output, &atomic) != nil {
		t.Fatal("owner SDK output invalid")
	}
	packet, err := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: "bounded-owner-request", Scope: d.wireScope(), SessionID: protocol.Identifier(opening.Session), Versions: opening.Versions, Changes: []protocol.AtomicChange{atomic}})
	if err != nil {
		t.Fatal(err)
	}
	return ws, packet
}
