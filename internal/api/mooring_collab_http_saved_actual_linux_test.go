//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
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
	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/testutil"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"golang.org/x/net/websocket"
)

// Actual HTTP RequireAuth (real signed access token/session rows), two canonical
// human subjects, native process, FULL SQL, one-use joins and gated WS snapshot.
// Uses actual pinned SDK emissions and native transport; the assertions below
// distinguish original public receipts and per-atomic SQL-backed peer cuts.
func TestMooringHTTPActualSDKWholeGroupSavedAndPeer(t *testing.T) {
	runMooringHTTPActualSDK(t, false)
}
func TestMooringHTTPActualSDKUnobservedPeerDeletePreservesOriginal(t *testing.T) {
	runMooringHTTPActualSDK(t, true)
}
func runMooringHTTPActualSDK(t *testing.T, peerDelete bool) {
	copy := testutil.MigratedDB(t)
	path := copy.Path()
	if e := copy.Close(); e != nil {
		t.Fatal(e)
	}
	full, e := database.Open("file:" + path)
	if e != nil {
		t.Fatal(e)
	}
	defer full.Close()
	rig := pagesTransferSetupWithDB(t, full)
	rig.seedPage(t, "http-native-page", "http-native-page", "Native", rig.adminID, "")
	mustExec(t, rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('http-native-page','user',?,'write',?)`, rig.userID, rig.adminID)
	v, e := auth.NewJWTValidator("http-native-actual-fixture-signing-32characters")
	if e != nil {
		t.Fatal(e)
	}
	sessionsDB := sessions.NewDBStore(rig.db)
	tokens := map[string]string{}
	sessionIDs := map[string]string{}
	for _, user := range []string{rig.adminID, rig.userID} {
		s, e := sessionsDB.Create(t.Context(), user, "", "", time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		sessionIDs[user] = s.ID
		tokens[user], e = v.IssueAccessToken(user, s.ID, "", "")
		if e != nil {
			t.Fatal(e)
		}
	}
	r := &Router{db: rig.db, pages: NewPageHandler(rig.db, nil, newTestLogger()), logger: newTestLogger(), mux: newRouteMux(), authMw: NewAuthMiddleware(v, sessionsDB, rig.db, newTestLogger()), mooringHTTPConfig: MooringHTTPConfig{Enabled: true, Binary: os.Getenv("MOORING_SESSION_BINARY"), BinarySHA256: os.Getenv("MOORING_SESSION_BINARY_SHA256"), DeliveryTTL: 5 * time.Second, SDKPrefix: "/mooring-sdk/generated/" + strings.Repeat("a", 64), EditorBundle: func(req *http.Request) (string, string) {
		if u := UserFromContext(req.Context()); u != nil && u.ID == rig.adminID {
			return strings.Repeat("c", 64), "https://editor.invalid/exact-release/index.html"
		}
		return "partial-metadata-must-not-appear", ""
	}}}
	r.registerMooringRoutes()
	if r.mooringHTTP == nil {
		t.Fatal("actual controller missing pinned runtime")
	}
	defer r.mooringHTTP.Close()
	server := httptest.NewServer(r.mux)
	defer server.Close()
	cp, e := os.ReadFile(os.Getenv("MOORING_SESSION_CHECKPOINT"))
	if e != nil || fmt.Sprintf("%x", sha256.Sum256(cp)) != "208213ce8ceb06a735fb0b1bf5cd7915f070227aee11d5a0955df3d86ffd0170" {
		t.Fatal("checkpoint pin unavailable", e)
	}
	type opening struct {
		Session     string            `json:"session_id"`
		Participant string            `json:"participant_ref"`
		Ticket      string            `json:"ticket"`
		Ref         string            `json:"connection_ref"`
		Origin      string            `json:"origin"`
		Revision    string            `json:"checkpoint_revision"`
		WSPath      string            `json:"websocket_path"`
		Versions    protocol.Versions `json:"versions"`
		SDKPrefix   string            `json:"sdk_prefix"`
		Editor      *struct {
			URL    string `json:"url"`
			Digest string `json:"bundle_digest"`
		} `json:"editor"`
	}
	start := func(user string, initial bool) opening {
		t.Helper()
		body := map[string]any{"write": true}
		if initial {
			body["checkpoint_base64"] = base64.StdEncoding.EncodeToString(cp)
		}
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", server.URL+"/api/v1/pages/http-native-page/mooring/session?workspace_id="+rig.wsID, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tokens[user])
		resp, e := server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("start %s HTTP%d %s", user, resp.StatusCode, data)
		}
		var result opening
		if json.Unmarshal(data, &result) != nil || result.Ref == "" || result.Revision == "" || result.SDKPrefix != r.mooringHTTPConfig.SDKPrefix {
			t.Fatal("missing bootstrap metadata")
		}
		return result
	}
	first, second := start(rig.adminID, true), start(rig.userID, false)
	if first.Editor == nil || first.Editor.Digest != strings.Repeat("c", 64) || first.Editor.URL != "https://editor.invalid/exact-release/index.html" || second.Editor != nil {
		t.Fatal("trusted editor metadata missing or partial callback reflected")
	}
	if first.Session != second.Session || first.Origin == second.Origin || first.Participant == second.Participant {
		t.Fatal("connections not independently bound")
	}
	// The complete baseline comes from the actual activated native inventory,
	// never a caller list or the assigned numeric writer label.
	key := mooring.SessionKey{WorkspaceID: rig.wsID, ArtifactID: "http-native-page", ApplicationID: "wordcraft"}
	r.mooringHTTP.mu.Lock()
	document := r.mooringHTTP.docs[key]
	r.mooringHTTP.mu.Unlock()
	if document == nil || document.activation.RequireHumanNativePrepare() != nil {
		t.Fatal("actual human capability not activated")
	}
	document.mu.Lock()
	original := document.connections[first.Ref]
	document.mu.Unlock()
	allocated := 0
	e = document.humans.runHumanOperation(original.request, original.admission.participant, func(ctx context.Context) error {
		snapshot, err := document.cell.store.ReadFullCheckpoint(ctx, document.owner)
		if err != nil {
			return err
		}
		basis := mooring.FullCheckpointBasis{Revision: string(snapshot.Checkpoint.Revision), Digest: string(snapshot.Checkpoint.Digest)}
		return document.cell.store.WithSessionOrigins(ctx, document.activation, basis, func(scoped context.Context, tx *sql.Tx, proof *mooring.SessionOriginInventory) error {
			return proof.Visit(scoped, tx, document.cell.store, document.owner, basis, func(origin uint64, _ uint32) error {
				allocated++
				r.mooringHTTP.mu.Lock()
				_, found := r.mooringHTTP.usedOrigins[key][origin]
				r.mooringHTTP.mu.Unlock()
				if !found || first.Origin == jsonNumber(origin) || second.Origin == jsonNumber(origin) {
					t.Fatal("source allocation ignored actual baseline namespace")
				}
				return nil
			})
		})
	})
	if e != nil || allocated == 0 {
		t.Fatal("complete native baseline was not qualified", allocated, e)
	}
	dial := func(o opening, user string) (*websocket.Conn, error) {
		cfg, e := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+o.WSPath+"&connection_ref="+o.Ref, server.URL)
		if e != nil {
			return nil, e
		}
		cfg.Header.Set("Authorization", "Bearer "+tokens[user])
		return websocket.DialConfig(cfg)
	}
	if stolen, e := dial(first, rig.userID); e == nil {
		stolen.Close()
		t.Fatal("foreign credential used original connection")
	}
	joinedBytes := map[string]json.RawMessage{}
	join := func(o opening, user string) *websocket.Conn {
		t.Helper()
		ws, e := dial(o, user)
		if e != nil {
			t.Fatal("WS upgrade", e)
		}
		t.Cleanup(func() { ws.Close() })
		ws.SetDeadline(time.Now().Add(8 * time.Second))
		raw, _ := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: protocol.Identifier("join-" + o.Participant), Ticket: o.Ticket, Versions: o.Versions})
		if e = websocket.Message.Send(ws, string(raw)); e != nil {
			t.Fatal(e)
		}
		var received []byte
		if e = websocket.Message.Receive(ws, &received); e != nil {
			t.Fatal("native joined", e)
		}
		var j protocol.SessionJoined
		if json.Unmarshal(received, &j) != nil || j.Type != "session_joined" || string(j.ParticipantRef) != o.Participant || string(j.SessionID) != o.Session || j.GateID == "" {
			t.Fatal("uncorrelated joined")
		}
		joinedBytes[o.Ref] = append([]byte(nil), received...)
		return ws
	}
	a, b := join(first, rig.adminID), join(second, rig.userID)

	read := func(ws *websocket.Conn, o opening) protocol.Checkpoint {
		t.Helper()
		raw, _ := json.Marshal(protocol.CheckpointRequest{Type: "checkpoint_request", RequestID: protocol.Identifier("read-" + o.Participant), Scope: document.wireScope(), Revision: protocol.Counter(o.Revision)})
		if err := websocket.Message.Send(ws, string(raw)); err != nil {
			t.Fatal(err)
		}
		var data []byte
		if err := websocket.Message.Receive(ws, &data); err != nil {
			t.Fatal(err)
		}
		var result protocol.CheckpointResult
		if json.Unmarshal(data, &result) != nil || result.Type != "checkpoint_result" {
			t.Fatal("checkpoint missing")
		}
		return result.Checkpoint
	}
	firstCP, secondCP := read(a, first), read(b, second)
	sdkDirs := map[string]string{}
	generate := func(o opening, checkpoint protocol.Checkpoint, count int, prefix string) []protocol.AtomicChange {
		t.Helper()
		dir := t.TempDir()
		sdkDirs[o.Ref] = dir
		inputs := []struct{ env, name, hash string }{
			{"MOORING_SDK_JS", "sdk.mjs", "4a55f64bc2f174b4dd2c06b503a910c74207041bfa41bf1ed3f91d245bfaa269"},
			{"MOORING_SDK_WASM", "sdk.wasm", "ed2cbc58105a0d7f157b4ec9513d4005ffb30ec98c85c8a0cfb5b375de510315"},
		}
		for _, input := range inputs {
			raw, err := os.ReadFile(os.Getenv(input.env))
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != input.hash {
				t.Fatal("SDK pin unavailable", input.env, err)
			}
			if err = os.WriteFile(filepath.Join(dir, input.name), raw, 0600); err != nil {
				t.Fatal(err)
			}
		}
		request, _ := json.Marshal(map[string]any{"joined": json.RawMessage(joinedBytes[o.Ref]), "checkpoint": checkpoint, "origin": o.Origin, "count": count, "prefix": prefix, "peer_delete": peerDelete && prefix == "peer-http"})
		if err := os.WriteFile(filepath.Join(dir, "input.json"), request, 0600); err != nil {
			t.Fatal(err)
		}
		const script = `import fs from 'node:fs'; import * as sdk from './sdk.mjs';
await sdk.default({module_or_path:fs.readFileSync(new URL('./sdk.wasm',import.meta.url))});
const i=JSON.parse(fs.readFileSync(new URL('./input.json',import.meta.url)));
const cp=sdk.validate_host_bootstrap(JSON.stringify(i.joined),JSON.stringify(i.checkpoint));
const draft=new sdk.WordCraftLocalDraft(cp,i.origin,'writer'); const changes=[];
for(let n=0;n<i.count;n++){
 const id=i.prefix+'-op-'+n;
 const command=i.peer_delete ? {kind:'text.delete',target_id:'qualified-d1-0',start:{kind:'start'},end:{kind:'end'}} : {kind:'text.insert',target_id:'qualified-d1-0',at:{kind:'start'},text:i.prefix+n,properties:{bold:false,italic:true}};
 const proposal=draft.edit(id,JSON.stringify(command));
 const payload=JSON.parse(proposal.human_payload_json(draft.optimistic_checkpoint()));
 const atomic={change_id:i.prefix+'-change-'+n,membership_hash:'sha256:'+'0'.repeat(64),operations:[{op_id:id,fingerprint:'sha256:'+'0'.repeat(64),participant_ref:i.joined.participant_ref,base_revision:i.checkpoint.revision,dependencies:[],read_preconditions:[],payload}]};
 changes.push(JSON.parse(sdk.bind_agent_change(JSON.stringify(i.joined.scope),JSON.stringify(atomic))));
}
process.stdout.write(JSON.stringify(changes));`
		if err := os.WriteFile(filepath.Join(dir, "generate.mjs"), []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		output, err := exec.CommandContext(t.Context(), "node", filepath.Join(dir, "generate.mjs")).CombinedOutput()
		if err != nil {
			t.Fatalf("actual SDK: %v %s", err, output)
		}
		var result []protocol.AtomicChange
		if json.Unmarshal(output, &result) != nil || len(result) != count {
			t.Fatal("SDK output malformed", string(output))
		}
		return result
	}
	own := generate(first, firstCP, 2, "own-http")
	peer := generate(second, secondCP, 1, "peer-http")
	send := func(ws *websocket.Conn, o opening, id string, changes []protocol.AtomicChange) {
		t.Helper()
		raw, _ := json.Marshal(protocol.SubmitChanges{Type: "submit_changes", RequestID: protocol.Identifier(id), Scope: document.wireScope(), SessionID: protocol.Identifier(o.Session), Versions: o.Versions, Changes: changes})
		if err := websocket.Message.Send(ws, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	framesByRef := map[string][]json.RawMessage{}
	peerAlreadySaved := false
	if peerDelete {
		// A/B were emitted by their actual SDK BEFORE this unobserved peer delete.
		// Wait for C's real SQL receipt, without applying C to that original draft.
		send(b, second, "original-peer-request", peer)
		for !peerAlreadySaved || len(framesByRef[second.Ref]) < 1 {
			_ = b.SetReadDeadline(time.Now().Add(15 * time.Second))
			var raw []byte
			if err := websocket.Message.Receive(b, &raw); err != nil {
				t.Fatal("peer delete SQL confirmation", err)
			}
			var header struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &header)
			switch header.Type {
			case "changes_received":
				var pending protocol.ChangesReceived
				_ = json.Unmarshal(raw, &pending)
				if pending.RequestID != "original-peer-request" || pending.State != "pending" {
					t.Fatal("peer delete pending relabelled")
				}
			case "document_update":
				var update protocol.DocumentUpdate
				_ = json.Unmarshal(raw, &update)
				if update.FromRevision != "0" || update.ToRevision != "1" {
					t.Fatal("peer delete cut not first")
				}
				framesByRef[second.Ref] = append(framesByRef[second.Ref], append(json.RawMessage(nil), raw...))
			case "commit_result":
				var receipt protocol.CommitResult
				_ = json.Unmarshal(raw, &receipt)
				if receipt.RequestID != "original-peer-request" || receipt.Revision != "1" || len(receipt.Changes) != 1 || receipt.Changes[0].Status != "saved" {
					t.Fatal("peer delete not actually Saved")
				}
				peerAlreadySaved = true
			default:
				t.Fatal("unexpected peer delete output", string(raw))
			}
		}
		send(a, first, "original-own-request", own)
	} else {
		send(a, first, "original-own-request", own)
		send(b, second, "original-peer-request", peer)
	}
	for _, recipient := range []struct {
		ws    *websocket.Conn
		id    string
		count int
		ref   string
	}{{a, "original-own-request", 2, first.Ref}, {b, "original-peer-request", 1, second.Ref}} {
		receivedReceipt, updates := recipient.ref == second.Ref && peerAlreadySaved, len(framesByRef[recipient.ref])
		for !receivedReceipt || updates < 3 {
			_ = recipient.ws.SetReadDeadline(time.Now().Add(15 * time.Second))
			var raw []byte
			if err := websocket.Message.Receive(recipient.ws, &raw); err != nil {
				document.originals.mu.Lock()
				for id, slot := range document.originals.changes {
					t.Logf("slot %s captured=%v captureError=%v group=%v saved=%v retired=%v", id, slot.captured, slot.captureErr, slot.group != nil, slot.saved != nil, slot.source.retired)
				}
				for _, group := range document.originals.groups {
					t.Logf("group captured=%v sent=%v pending=%v published=%v", group.captured, group.sent, group.pendingPrivate != nil, group.receipt != nil)
				}
				document.originals.mu.Unlock()

				t.Fatalf("actual receipt/peer %s updates=%d queued=%d: %v", recipient.id, updates, document.pump.queuedMutations.Load(), err)
			}
			var header struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(raw, &header)
			switch header.Type {
			case "changes_received":
				var pending protocol.ChangesReceived
				_ = json.Unmarshal(raw, &pending)
				if string(pending.RequestID) != recipient.id || pending.State != "pending" || len(pending.ChangeIds) != recipient.count {
					t.Fatal("pending group relabelled")
				}
			case "commit_result":
				var receipt protocol.CommitResult
				_ = json.Unmarshal(raw, &receipt)
				if string(receipt.RequestID) != recipient.id || len(receipt.Changes) != recipient.count {
					t.Fatal("original whole group relabelled", string(raw))
				}
				for _, change := range receipt.Changes {
					if change.Status != "saved" {
						t.Fatal("actual SDK not Saved", string(raw))
					}
				}
				receivedReceipt = true
			case "document_update":
				var update protocol.DocumentUpdate
				_ = json.Unmarshal(raw, &update)
				if update.RequestID != protocol.Identifier("actor-update:"+string(update.ToRevision)) || update.Encoding != "json" {
					t.Fatal("peer cut uncorrelated")
				}
				framesByRef[recipient.ref] = append(framesByRef[recipient.ref], append(json.RawMessage(nil), raw...))
				updates++
			default:
				t.Fatal("unexpected output", string(raw))
			}
		}
	}

	currentOpening := first
	currentOpening.Revision = "3"
	actualCurrent := read(a, currentOpening)
	for _, o := range []opening{first, second} {
		dir := sdkDirs[o.Ref]
		input, _ := json.Marshal(map[string]any{"frames": framesByRef[o.Ref], "expected": actualCurrent.State, "peer_delete": peerDelete})
		if err := os.WriteFile(filepath.Join(dir, "replay.json"), input, 0600); err != nil {
			t.Fatal(err)
		}
		const replayScript = `import fs from 'node:fs';import assert from 'node:assert/strict';import * as sdk from './sdk.mjs';
await sdk.default({module_or_path:fs.readFileSync(new URL('./sdk.wasm',import.meta.url))});
const i=JSON.parse(fs.readFileSync(new URL('./input.json',import.meta.url)));const replay=JSON.parse(fs.readFileSync(new URL('./replay.json',import.meta.url)));
const bytes=sdk.validate_host_bootstrap(JSON.stringify(i.joined),JSON.stringify(i.checkpoint));
const reader=new sdk.WordCraftLocalDraft(bytes,i.origin,'reader');let expectedFrom=i.checkpoint.revision;
for(const frame of replay.frames){const data=sdk.validate_host_peer_document_update(JSON.stringify(i.joined),expectedFrom,JSON.stringify(frame));reader.apply_verified_document_update(data);expectedFrom=frame.to_revision;}
assert.equal(expectedFrom,'3');const actual=JSON.parse(reader.native_json());const expected=JSON.parse(sdk.WordCraftModel.restore(Buffer.from(replay.expected,'base64')).native_json());
assert.deepEqual(actual,expected);const text=JSON.stringify(actual);for(const marker of (replay.peer_delete ? ['own-http0','own-http1'] : ['own-http0','own-http1','peer-http0']))assert.ok(text.includes(marker),marker);
if(replay.peer_delete)assert.ok(!actual.body[0].text.includes('Český zkušební dokument'));`
		if err := os.WriteFile(filepath.Join(dir, "replay.mjs"), []byte(replayScript), 0600); err != nil {
			t.Fatal(err)
		}
		if output, err := exec.CommandContext(t.Context(), "node", filepath.Join(dir, "replay.mjs")).CombinedOutput(); err != nil {
			t.Fatalf("actual peer SDK vs SQL native: %v %s", err, output)
		}
	}
	var operations int
	if err := rig.db.QueryRow(`SELECT COUNT(*) FROM mooring_collab_operations WHERE page_id='http-native-page'`).Scan(&operations); err != nil || operations != 3 {
		t.Fatal("actual SQL missing changes", operations, err)
	}
	t.Log("actual SDK original two-atomic group + independent human Saved and three correlated native peer cuts per recipient")
}
