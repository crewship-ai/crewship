package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestDocumentDraftMCPPreparesImmutableUnsavedPacket(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	args := `{"view":{"page_id":"own-page","schema":3,"rev":0,"generation":1,"epoch":"opaque-epoch","targets":[{"id":"p1","type":"paragraph","parent":null,"text":"A","hash":"` + strings.Repeat("a", 64) + `"}]},"target":"p1","text":"B","mutation_id":"draft-one"}`
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doc_draft_prepare","arguments":`+args+`}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	failed, body := documentCreateMCPResult(t, w)
	if failed {
		t.Fatalf("admitted drafting prepare unavailable: %s", body)
	}
	var result struct {
		Status string `json:"status"`
		Packet struct {
			Generation int64 `json:"generation"`
			Patch      struct {
				ID, Target, Text, IfHash string
				IfRev                    int64 `json:"ifRev"`
			} `json:"patch"`
			Checkpoint struct {
				ID    string
				IfRev int64 `json:"ifRev"`
			} `json:"checkpoint"`
		} `json:"packet"`
	}
	if err := json.Unmarshal([]byte(body), &result); err != nil || result.Status != "prepared_unsaved" || result.Packet.Generation != 1 || result.Packet.Patch.ID != "draft-one" || result.Packet.Patch.Target != "p1" || result.Packet.Patch.Text != "B" || result.Packet.Patch.IfHash != strings.Repeat("a", 64) || result.Packet.Patch.IfRev != 0 || result.Packet.Checkpoint.ID != "draft-one-checkpoint" || result.Packet.Checkpoint.IfRev != 1 {
		t.Fatalf("not an immutable original-view packet: %v %s", err, body)
	}
}

func TestDocumentDraftMCPExactApplyRetryAndCheckpointFailure(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	packet, err := PrepareDocumentDraft([]byte(draftPrepareInput))
	if err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(map[string]any{"packet": packet})
	calls := []string{}
	failPin := true
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, string(raw))
		var body struct {
			Operation string `json:"operation"`
		}
		_ = json.Unmarshal(raw, &body)
		id := "draft-one"
		pinField := ""
		if body.Operation == "checkpoint" {
			if failPin {
				http.Error(w, "synthetic pin outage", 503)
				return
			}
			id += "-checkpoint"
			pinField = `,"checkpoint":"` + id + `"`
		}
		_, _ = io.WriteString(w, `{"id":"`+id+`","rev":1,"epoch":"opaque-epoch"`+pinField+`}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	call := func(name string) (bool, string) {
		r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+string(arguments)+`}}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Host = "127.0.0.1:9119"
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, r)
		return documentCreateMCPResult(t, w)
	}
	failed, body := call("doc_draft_apply")
	if !failed || !strings.Contains(body, "committed_checkpoint_pending") || len(calls) != 2 {
		t.Fatalf("durable patch hidden: %s", body)
	}
	failPin = false
	failed, body = call("doc_draft_apply")
	if failed || !strings.Contains(body, "checkpointed") || len(calls) != 4 || calls[0] != calls[2] || calls[1] != calls[3] {
		t.Fatalf("exact retry changed %v %s", calls, body)
	}
	failed, body = call("doc_draft_checkpoint")
	if failed || len(calls) != 5 || calls[4] != calls[1] {
		t.Fatalf("checkpoint retry %v %s", calls, body)
	}
}
