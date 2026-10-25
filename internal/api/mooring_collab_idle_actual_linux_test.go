//go:build linux && mooring_rust_integration

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"github.com/gorilla/websocket"
)

// The original human credential and confirmed agent identity are controlled
// fixtures. The socket, idle transport bound, Rust process, agent tool Save,
// peer delivery and SQL are real; this is not live human/model acceptance.
func testMooringIdleHumanThenAgentSave(t *testing.T) {
	var peer <-chan []byte
	withMooringAgentActualProductionSaved(t, func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, d *collaborationHTTPDocument, a *collaborationLiveAgent) {
		select {
		case raw := <-peer:
			var update protocol.DocumentUpdate
			if json.Unmarshal(raw, &update) != nil || update.Type != "document_update" || update.ToRevision != "1" || update.Scope != d.wireScope() {
				t.Fatalf("idle human lost agent update: %s", raw)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("idle human did not receive agent Save")
		}
	}, mooringAgentSavedFixtureOptions{beforeTask: func(x *collaborationAgentTestFixture, m *collaborationHTTPManager, d *collaborationHTTPDocument) {
		handler := m.fence.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("slug", "@"+x.key.ArtifactID)
			if r.Method == http.MethodPost {
				m.session(w, r)
			} else {
				m.websocket(w, r)
			}
		}))
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(context.WithValue(x.human, ctxWorkspaceID, x.key.WorkspaceID)))
		}))
		t.Cleanup(server.Close)
		response, err := http.Post(server.URL, "application/json", bytes.NewBufferString(`{"write":false}`))
		if err != nil {
			t.Fatal(err)
		}
		var opening struct {
			Revision string            `json:"checkpoint_revision"`
			Session  string            `json:"session_id"`
			Ref      string            `json:"connection_ref"`
			Ticket   string            `json:"ticket"`
			Versions protocol.Versions `json:"versions"`
		}
		if json.NewDecoder(response.Body).Decode(&opening) != nil || response.StatusCode != 200 {
			response.Body.Close()
			t.Fatalf("human session: %d", response.StatusCode)
		}
		response.Body.Close()
		headers := http.Header{"Origin": []string{server.URL}}
		ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"?connection_ref="+opening.Ref, headers)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.Close() })
		join, _ := json.Marshal(protocol.SessionJoin{Type: "session_join", RequestID: "idle-human-join", Ticket: opening.Ticket, Versions: opening.Versions})
		if err := ws.WriteMessage(websocket.TextMessage, join); err != nil {
			t.Fatal(err)
		}
		_, joined, err := ws.ReadMessage()
		if err != nil || !bytes.Contains(joined, []byte(`"session_joined"`)) {
			t.Fatalf("human join: %s %v", joined, err)
		}
		request, _ := json.Marshal(protocol.CheckpointRequest{Type: "checkpoint_request", RequestID: "idle-human-checkpoint", Scope: d.wireScope(), Revision: protocol.Counter(opening.Revision)})
		if err := ws.WriteMessage(websocket.TextMessage, request); err != nil {
			t.Fatal(err)
		}
		_, checkpoint, err := ws.ReadMessage()
		if err != nil || !bytes.Contains(checkpoint, []byte(`"checkpoint_result"`)) {
			t.Fatalf("human checkpoint: %s %v", checkpoint, err)
		}
		updates := make(chan []byte, 1)
		peer = updates
		go func() {
			_, raw, err := ws.ReadMessage()
			if err != nil {
				updates <- nil
				return
			}
			updates <- raw
		}()
		// More than the unchanged production 60s idle deadline. Only RFC6455
		// pongs, handled by the client read, pass during this interval.
		timer := time.NewTimer(65 * time.Second)
		defer timer.Stop()
		select {
		case raw := <-updates:
			t.Fatalf("idle human closed/received premature data: %s", raw)
		case <-timer.C:
		}
	}})
}
