//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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
// No synthetic role/source context, raw SDK edit, peer broadcast or Saved claim.
func TestMooringHTTPActualTwoHumansJoinReadAndOriginalRevocation(t *testing.T) {
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
	r := &Router{db: rig.db, pages: NewPageHandler(rig.db, nil, newTestLogger()), logger: newTestLogger(), mux: newRouteMux(), authMw: NewAuthMiddleware(v, sessionsDB, rig.db, newTestLogger()), mooringHTTPConfig: MooringHTTPConfig{Enabled: true, Binary: os.Getenv("MOORING_SESSION_BINARY"), BinarySHA256: os.Getenv("MOORING_SESSION_BINARY_SHA256"), DeliveryTTL: 5 * time.Second}}
	r.registerMooringRoutes()
	if r.mooringHTTP == nil {
		t.Fatal("actual controller missing pinned runtime")
	}
	defer r.mooringHTTP.Close()
	server := httptest.NewServer(r.mux)
	defer server.Close()
	cp, e := os.ReadFile(os.Getenv("MOORING_SESSION_CHECKPOINT"))
	if e != nil {
		t.Fatal(e)
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
		if json.Unmarshal(data, &result) != nil || result.Ref == "" || result.Revision == "" {
			t.Fatal("missing bootstrap metadata")
		}
		return result
	}
	first, second := start(rig.adminID, true), start(rig.userID, false)
	if first.Session != second.Session || first.Origin == second.Origin || first.Participant == second.Participant {
		t.Fatal("connections not independently bound")
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
		return ws
	}
	a, b := join(first, rig.adminID), join(second, rig.userID)
	read := func(ws *websocket.Conn, o opening) {
		t.Helper()
		raw, _ := json.Marshal(protocol.CheckpointRequest{Type: "checkpoint_request", RequestID: protocol.Identifier("read-" + o.Participant), Scope: protocol.Scope{WorkspaceID: protocol.Identifier(rig.wsID), ArtifactID: "http-native-page", ApplicationID: "wordcraft"}, Revision: protocol.Counter(o.Revision)})
		if e = websocket.Message.Send(ws, string(raw)); e != nil {
			t.Fatal(e)
		}
		var received []byte
		if e = websocket.Message.Receive(ws, &received); e != nil {
			t.Fatal("gated checkpoint", e)
		}
		var result protocol.CheckpointResult
		if json.Unmarshal(received, &result) != nil || result.Type != "checkpoint_result" || result.Checkpoint.Scope.ArtifactID != "http-native-page" || string(result.Checkpoint.Revision) != o.Revision || result.Checkpoint.State == "" {
			t.Fatal("wrong checkpoint")
		}
	}
	read(a, first)
	read(b, second)
	if replay, e := dial(first, rig.adminID); e == nil {
		replay.Close()
		t.Fatal("used WS connection replayed")
	}
	r.mooringHTTP.mu.Lock()
	originalDocument := r.mooringHTTP.docs[mooring.SessionKey{WorkspaceID: rig.wsID, ArtifactID: "http-native-page", ApplicationID: "wordcraft"}]
	r.mooringHTTP.mu.Unlock()
	mustExec(t, rig.db, `UPDATE user_sessions SET revoked_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), sessionIDs[rig.userID])
	raw, _ := json.Marshal(protocol.CheckpointRequest{Type: "checkpoint_request", RequestID: "read-revoked", Scope: protocol.Scope{WorkspaceID: protocol.Identifier(rig.wsID), ArtifactID: "http-native-page", ApplicationID: "wordcraft"}, Revision: protocol.Counter(second.Revision)})
	_ = b.SetDeadline(time.Now().Add(3 * time.Second))
	_ = websocket.Message.Send(b, string(raw))
	var denied []byte
	if websocket.Message.Receive(b, &denied) == nil {
		t.Fatal("revoked original source received content")
	}
	// Actual child death retires the old process/capabilities before a new
	// authenticated bootstrap can restore the retained creator checkpoint.
	originalDocument.close()
	reopened := start(rig.adminID, false)
	if reopened.Session == first.Session || reopened.Origin == first.Origin || reopened.Ref == first.Ref {
		t.Fatal("child restart reused connection binding")
	}
	fresh := join(reopened, rig.adminID)
	read(fresh, reopened)
}
