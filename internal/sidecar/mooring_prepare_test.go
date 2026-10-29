package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
	"github.com/crewship-ai/crewship/internal/mooring"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
)

const mooringTypedToolInput = `{"view":{"scope":{"workspace_id":"ws-1","artifact_id":"native-page","application_id":"wordcraft"},"session_id":"native-session","versions":{"protocol_version":"0.4.0","model_version":"0.2.0","engine_version":"0.10.0"},"task_id":"task-one","participant_ref":"actual-agent-participant","revision":"0"},"mutation_id":"original-task-call","changes":[{"operations":[{"payload":{"kind":"text.insert","target_id":"p_one","at":{"kind":"end"},"text":"A","properties":null},"read_preconditions":[{"target_id":"p_one","revision":"0"}],"dependencies":[]},{"payload":{"kind":"text.format","target_id":"p_one","from":{"kind":"start"},"to":{"kind":"end"},"properties":{"bold":true}},"read_preconditions":[{"target_id":"p_one","revision":"0"}],"dependencies":[]}]},{"operations":[{"payload":{"kind":"text.insert","target_id":"p_two","at":{"kind":"end"},"text":"B","properties":null},"read_preconditions":[{"target_id":"p_two","revision":"0"}],"dependencies":[]}]}]}`

func TestMooringSubmitBuildsExistingImmutableMultiAtomicMembershipWithoutRead(t *testing.T) {
	first, err := prepareMooringToolPacket([]byte(mooringTypedToolInput))
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareMooringToolPacket([]byte(mooringTypedToolInput))
	if err != nil || string(first) != string(second) {
		t.Fatal("same original call rebuilt different packet", err)
	}
	var packet protocol.SubmitChanges
	if json.Unmarshal(first, &packet) != nil || len(packet.Changes) != 2 || len(packet.Changes[0].Operations) != 2 {
		t.Fatal("atomic boundaries were split or restricted")
	}
	raw, _ := json.Marshal(packet.Changes)
	var changes []mooring.BatchChange
	if json.Unmarshal(raw, &changes) != nil {
		t.Fatal("change encoding")
	}
	if _, err = mooring.NormalizeSessionIngress(mooring.SessionKey{WorkspaceID: "ws-1", ArtifactID: "native-page", ApplicationID: "wordcraft"}, changes); err != nil {
		t.Fatal("existing canonicalizer rejects generated membership", err)
	}
	s, key := newRunIdentityServer(t)
	s.ipc.Token = "synthetic-document-ipc"
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := []string{}
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(raw))
		http.Error(w, `{"error":"synthetic uncertainty"}`, 503)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	invoke := func(args string) json.RawMessage {
		r := httptest.NewRequest("POST", "/mcp/mooring", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doc_submit","arguments":`+args+`}}`))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleMooringMCP(w, r)
		failed, body := documentCreateMCPResult(t, w)
		if !failed {
			t.Fatal("uncertain transport reported success")
		}
		var response struct {
			Submission json.RawMessage `json:"submission"`
		}
		if json.Unmarshal([]byte(body), &response) != nil || len(response.Submission) == 0 {
			t.Fatal("uncertainty lost immutable packet")
		}
		return response.Submission
	}
	retained := invoke(mooringTypedToolInput)
	retry, _ := json.Marshal(map[string]any{"submission": retained})
	again := invoke(string(retry))
	if string(retained) != string(again) || len(calls) != 2 || calls[0] != calls[1] || !strings.HasPrefix(calls[0], "POST /api/v1/internal/mooring/task ") {
		t.Fatal("typed submission reread or changed uncertain original")
	}
}
