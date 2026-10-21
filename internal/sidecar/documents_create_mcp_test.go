package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

const documentCreateMCPArguments = `{"slug":"finished-doc","name":"Finished report","mutation_id":"create-one","blocks":[{"type":"paragraph","text":"Completed text"}]}`

func documentCreateMCPCall(t *testing.T, s *Server, token, arguments string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"doc_create","arguments":`+arguments+`}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Host = "127.0.0.1:9119"
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	return w
}

func documentCreateMCPResult(t *testing.T, w *httptest.ResponseRecorder) (bool, string) {
	t.Helper()
	if w.Code != 200 {
		t.Fatalf("MCP HTTP%d", w.Code)
	}
	var response struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Result.Content) != 1 {
		t.Fatalf("MCP envelope: %v", err)
	}
	return response.Result.IsError, response.Result.Content[0].Text
}

func TestDocumentCreateMCPCatalog(t *testing.T) {
	s, _ := newRunIdentityServer(t)
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	var response struct {
		Result struct {
			Tools []memoryMCPToolDescriptor `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, tool := range response.Result.Tools {
		if tool.Name != "doc_create" {
			continue
		}
		found++
		var schema struct {
			Type                 string                     `json:"type"`
			AdditionalProperties bool                       `json:"additionalProperties"`
			Required             []string                   `json:"required"`
			Properties           map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" || schema.AdditionalProperties || len(schema.Required) != 4 || len(schema.Properties) != 4 {
			t.Fatalf("creation schema is not closed: %s", tool.InputSchema)
		}
		for _, field := range []string{"slug", "name", "mutation_id", "blocks"} {
			if _, ok := schema.Properties[field]; !ok {
				t.Fatalf("missing %s", field)
			}
		}
		var blocks struct {
			MinItems, MaxItems int
			Items              struct {
				AdditionalProperties bool `json:"additionalProperties"`
			}
		}
		if err := json.Unmarshal(schema.Properties["blocks"], &blocks); err != nil || blocks.MinItems != 1 || blocks.MaxItems != 128 || blocks.Items.AdditionalProperties {
			t.Fatal("readable authoring bounds missing")
		}
	}
	if found != 1 {
		t.Fatalf("routines MCP must advertise exactly one doc_create; got%d", found)
	}
}

func TestDocumentCreateMCPForwardsOriginalCapability(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls.Add(1)
		if r.Method != "POST" || r.URL.Path != "/api/v1/internal/documents/create" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("original run capability or crew IPC changed")
		}
		raw, _ := io.ReadAll(r.Body)
		var actual, expected any
		if json.Unmarshal(raw, &actual) != nil || json.Unmarshal([]byte(documentCreateMCPArguments), &expected) != nil {
			t.Error("invalid forwarded authoring")
		}
		a, _ := json.Marshal(actual)
		b, _ := json.Marshal(expected)
		if string(a) != string(b) {
			t.Error("creation arguments changed")
		}
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"page_id":"saved-doc","revision":0}`)
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-crew-ipc"
	failed, text := documentCreateMCPResult(t, documentCreateMCPCall(t, s, token, documentCreateMCPArguments))
	if failed || calls.Load() != 1 || !strings.Contains(text, "saved-doc") {
		t.Fatalf("creation tool not forwarded: error%v calls%d result%s", failed, calls.Load(), text)
	}
}

func TestDocumentCreateMCPStrictArgumentsAndRunIdentity(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	for _, test := range []struct{ name, args, contains string }{
		{"null", "null", "object"}, {"array", "[]", "object"}, {"unknown", strings.Replace(documentCreateMCPArguments, `"slug":`, `"owner_user_id":"other","slug":`, 1), "unknown"},
		{"duplicate", strings.Replace(documentCreateMCPArguments, `"slug":`, `"slug":"different","slug":`, 1), "duplicate"},
		{"null value", strings.Replace(documentCreateMCPArguments, `"finished-doc"`, `null`, 1), "null"},
		{"wrong string type", strings.Replace(documentCreateMCPArguments, `"finished-doc"`, `42`, 1), "string"},
		{"wrong blocks type", `{"slug":"s","name":"N","mutation_id":"one","blocks":{}}`, "array"},
		{"missing", `{"slug":"s","name":"N","mutation_id":"one"}`, "blocks"},
	} {
		t.Run(test.name, func(t *testing.T) {
			failed, text := documentCreateMCPResult(t, documentCreateMCPCall(t, s, token, test.args))
			if !failed || !strings.Contains(text, test.contains) || calls.Load() != 0 {
				t.Fatalf("unsafe args reached host: error%v calls%d %s", failed, calls.Load(), text)
			}
		})
	}
	for _, capability := range []string{"", s.ipc.AgentToken, "agtv2.forged", internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(runTestMaster, "ws-1", "other-crew"), "ws-1", "agent-boot", "run-boot")} {
		failed, _ := documentCreateMCPResult(t, documentCreateMCPCall(t, s, capability, documentCreateMCPArguments))
		if !failed || calls.Load() != 0 {
			t.Fatal("non-current run capability reached host")
		}
	}
}

func TestDocumentCreateMCPBudgetsAndUnknownOutcome(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		calls.Add(1)
		_, _ = io.WriteString(w, strings.Repeat("x", (64<<10)+1))
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	w := documentCreateMCPCall(t, s, token, strings.Replace(documentCreateMCPArguments, "Completed text", strings.Repeat("x", 1<<20), 1))
	if w.Code != 400 || calls.Load() != 0 {
		t.Fatal("oversized MCP envelope reached host")
	}
	failed, text := documentCreateMCPResult(t, documentCreateMCPCall(t, s, token, documentCreateMCPArguments))
	if !failed || calls.Load() != 1 || !strings.Contains(text, `"status":503`) || !strings.Contains(text, "same mutation_id") || strings.Contains(text, "xxxx") {
		t.Fatal("oversized sent response did not preserve unknown outcome")
	}
	recorder := &documentCreateMCPRecorder{header: http.Header{}}
	recorder.WriteHeader(201)
	_, _ = recorder.Write([]byte(strings.Repeat("x", (1<<20)+1)))
	_, _ = recorder.Write([]byte("must not append"))
	if !recorder.overflow || recorder.body.Len() != 0 || recorder.status != 201 {
		t.Fatal("MCP recorder retained an oversized response")
	}
}

func TestDocumentCreateMCPLostResponseRetainsExactRetryAndBackendDenial(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	payloads := make(chan string, 3)
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "run-status") {
			w.WriteHeader(503)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("retry lost original run/crew authentication")
		}
		raw, _ := io.ReadAll(r.Body)
		payloads <- string(raw)
		switch calls.Add(1) {
		case 1:
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		case 2:
			_, _ = io.WriteString(w, `{"page_id":"same-saved-doc","revision":0}`)
		default:
			w.WriteHeader(503)
			_, _ = io.WriteString(w, `{"error":"Documents is disabled"}`)
		}
	}))
	defer host.Close()
	s.ipc.BaseURL = host.URL
	s.ipc.Token = "synthetic-crew-ipc"
	failed, text := documentCreateMCPResult(t, documentCreateMCPCall(t, s, token, documentCreateMCPArguments))
	if !failed || !strings.Contains(text, `"status":503`) || !strings.Contains(text, "same mutation_id") {
		t.Fatal("lost acknowledgement was treated as success")
	}
	failed, text = documentCreateMCPResult(t, documentCreateMCPCall(t, s, token, documentCreateMCPArguments))
	if failed || !strings.Contains(text, "same-saved-doc") || calls.Load() != 2 {
		t.Fatal("exact retry could not recover the original result")
	}
	if first, second := <-payloads, <-payloads; first != second || !strings.Contains(first, `"mutation_id":"create-one"`) {
		t.Fatal("uncertain retry changed original mutation or authoring")
	}
	failed, text = documentCreateMCPResult(t, documentCreateMCPCall(t, s, token, documentCreateMCPArguments))
	if !failed || !strings.Contains(text, `"status":503`) || !strings.Contains(text, "disabled") || calls.Load() != 3 {
		t.Fatal("MCP bypassed authoritative backend disabled refusal")
	}
}

func TestDocumentCreateMCPRejectsTrailingArguments(t *testing.T) {
	if _, err := decodeDocumentCreateMCPArgs([]byte(documentCreateMCPArguments + `{}`)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
}
