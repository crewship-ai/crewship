package sidecar

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

const draftPrepareInput = `{"view":{"page_id":"own-page","schema":3,"rev":0,"generation":1,"epoch":"opaque-epoch","targets":[{"id":"p1","type":"paragraph","parent":null,"text":"A","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},"target":"p1","text":"B","mutation_id":"draft-one"}`

func TestDocumentDraftTransportIdentityAndBounds(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	response := `{"page_id":"own-page"}`
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls++
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/api/v1/internal/documents/draft" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Fatalf("wrong protected host transport: %s", r.URL)
		}
		if r.Method == "POST" && string(body) != `{"operation":"checkpoint","generation":1,"request":{}}` {
			t.Fatalf("body changed %s", body)
		}
		_, _ = io.WriteString(w, response)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-crew-ipc"
	invoke := func(method, path, capability, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+capability)
		w := httptest.NewRecorder()
		s.handleDocumentDraft(w, r)
		return w
	}
	for _, method := range []string{"GET", "POST"} {
		body := ""
		if method == "POST" {
			body = `{"operation":"checkpoint","generation":1,"request":{}}`
		}
		w := invoke(method, "/api/v1/documents/draft", token, body)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("transport%d %s", w.Code, w.Body.String())
		}
	}
	before := calls
	for _, test := range []struct {
		method, path, token, body string
		status                    int
	}{{"GET", "/api/v1/documents/draft?workspace_id=ws-1", token, "", 400}, {"GET", "/api/v1/documents/draft?page_id=foreign", token, "", 400}, {"GET", "/api/v1/documents/draft", token, `{}`, 400}, {"DELETE", "/api/v1/documents/draft", token, "", 405}, {"GET", "/api/v1/documents/draft", "", "", 403}, {"GET", "/api/v1/documents/draft", s.ipc.AgentToken, "", 403}, {"GET", "/api/v1/documents/draft", internaltoken.DeriveAgentRunToken(key, "wrong-ws", "agent-boot", "run-boot"), "", 403}, {"POST", "/api/v1/documents/draft", token, strings.Repeat("x", (1<<20)+1), 413}} {
		w := invoke(test.method, test.path, test.token, test.body)
		if w.Code != test.status || calls != before {
			t.Fatalf("scope/bound guard%d want%d calls%d %s", w.Code, test.status, calls, w.Body.String())
		}
	}
	response = strings.Repeat("x", (64<<10)+1)
	w := invoke("GET", "/api/v1/documents/draft", token, "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "xxxx") {
		t.Fatal("partial oversized response exposed")
	}
}
func TestDocumentDraftPrepareRejectsScopeAndAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{`null`, draftPrepareInput + `{}`, strings.Replace(draftPrepareInput, `"target":"p1"`, `"target":"missing"`, 1), strings.Replace(draftPrepareInput, `"schema":3`, `"schema":4`, 1), strings.Replace(draftPrepareInput, `"rev":0`, `"rev":null`, 1), strings.Replace(draftPrepareInput, `"type":"paragraph"`, `"type":"table"`, 1), strings.Replace(draftPrepareInput, `"text":"B"`, `"text":"B","text":"C"`, 1), strings.Replace(draftPrepareInput, `"mutation_id":"draft-one"`, `"mutation_id":"draft-one","user_id":"other"`, 1), strings.Replace(draftPrepareInput, `"text":"B"`, `"text":"`+strings.Repeat("b", 64<<10)+`"`, 1)} {
		if _, err := PrepareDocumentDraft([]byte(raw)); err == nil {
			t.Fatalf("unsafe prepare accepted: %.100s", raw)
		}
	}
}
func TestDocumentDraftApplyRequiresOriginalConfirmedReceipt(t *testing.T) {
	p, err := PrepareDocumentDraft([]byte(draftPrepareInput))
	if err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{`{}`, `{"id":"other","rev":1,"epoch":"opaque-epoch"}`, `{"id":"draft-one","rev":2,"epoch":"opaque-epoch"}`, `{"id":"draft-one","rev":1,"epoch":"new-epoch"}`} {
		calls := 0
		out, err := ApplyDocumentDraft(p, false, func(string, []byte) (int, []byte, error) { calls++; return 200, []byte(response), nil })
		if err == nil || calls != 1 || out.Status != "outcome_unknown" || len(out.Checkpoint) != 0 {
			t.Fatalf("unconfirmed patch pinned: %v %+v", err, out)
		}
	}
	p.Checkpoint.IfRev++
	calls := 0
	if _, err := ApplyDocumentDraft(p, false, func(string, []byte) (int, []byte, error) { calls++; return 200, nil, nil }); err == nil || calls != 0 {
		t.Fatal("inconsistent packet reached transport")
	}
}
func TestDocumentDraftMCPCatalogIsClosedAndDeterministic(t *testing.T) {
	first, _ := json.Marshal(documentDraftMCPCatalog())
	second, _ := json.Marshal(documentDraftMCPCatalog())
	if string(first) != string(second) {
		t.Fatal("catalog order changed")
	}
	tools := documentDraftMCPCatalog()
	if len(tools) != 4 {
		t.Fatal("missing bounded tools")
	}
	for _, tool := range tools {
		var schema struct {
			AdditionalProperties bool
			Type                 string
			Properties           map[string]json.RawMessage
		}
		if json.Unmarshal(tool.InputSchema, &schema) != nil || schema.AdditionalProperties || schema.Type != "object" {
			t.Fatalf("open schema %s", tool.Name)
		}
	}
}

func TestDocumentDraftCheckpointRequiresPinEvidence(t *testing.T) {
	p, err := PrepareDocumentDraft([]byte(draftPrepareInput))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"id":"draft-one-checkpoint","rev":1,"epoch":"opaque-epoch"}`, `{"id":"draft-one-checkpoint","rev":1,"epoch":"opaque-epoch","checkpoint":"other-pin"}`} {
		out, err := ApplyDocumentDraft(p, true, func(string, []byte) (int, []byte, error) { return 200, []byte(body), nil })
		if err == nil || out.Status == "checkpointed" {
			t.Fatalf("missing or mismatched checkpoint falsely confirmed: %s", body)
		}
	}
}
func TestDocumentDraftPreservesDenialHTTPStatus(t *testing.T) {
	p, err := PrepareDocumentDraft([]byte(draftPrepareInput))
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{403, 409, 503} {
		_, err := ApplyDocumentDraft(p, false, func(string, []byte) (int, []byte, error) {
			return status, []byte(`{"error":"do-not-echo-private-body"}`), nil
		})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("HTTP%d", status)) || strings.Contains(err.Error(), "do-not-echo-private-body") {
			t.Fatalf("denial status lost: %d %v", status, err)
		}
	}
}
func TestDocumentDraftDerivedOperationIDBounds(t *testing.T) {
	for _, size := range []int{117, 118, 128} {
		raw := strings.Replace(draftPrepareInput, "draft-one", strings.Repeat("x", size), 1)
		_, err := PrepareDocumentDraft([]byte(raw))
		if (err == nil) != (size == 117) {
			t.Fatalf("derived checkpoint ID boundary%d: %v", size, err)
		}
	}
	p, err := PrepareDocumentDraft([]byte(draftPrepareInput))
	if err != nil {
		t.Fatal(err)
	}
	p.Patch.ID = strings.Repeat("x", 118)
	p.Checkpoint.ID = p.Patch.ID + "-checkpoint"
	raw, _ := json.Marshal(p)
	if _, err := ParseDocumentDraftPacket(raw); err == nil {
		t.Fatal("packet accepted a checkpoint ID over the deployed Node limit")
	}
}
