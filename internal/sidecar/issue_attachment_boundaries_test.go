package sidecar

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIssueAttachmentsRequireIdentityAndRejectPathInjection(t *testing.T) {
	for _, handler := range []string{"list", "read", "attach"} {
		for _, mode := range []string{"missing IPC", "unknown token", "missing identifier", "query injection", "attachment traversal", "missing attachment"} {
			t.Run(handler+"/"+mode, func(t *testing.T) {
				ipc := &IPCConfig{BaseURL: "http://127.0.0.1:1", AgentID: "trusted-agent", AgentToken: "agent-token", WorkspaceID: "workspace"}
				if mode == "missing IPC" {
					ipc = nil
				}
				s := newRoutineMCPTestServer(t, ipc)
				path := "/issue/ISS-1/attachments/file"
				token := "agent-token"
				want := 400
				switch mode {
				case "missing IPC":
					want = 503
				case "unknown token":
					token = "foreign"
					want = 403
				case "missing identifier":
					path = "/issue//attachments"
				case "query injection":
					path = "/issue/ISS-1?workspace_id=foreign/attachments"
				case "attachment traversal":
					path = "/issue/ISS-1/attachments/a/b"
				case "missing attachment":
					if handler != "read" {
						return
					}
					path = "/issue/ISS-1/attachments"
				}
				r := httptest.NewRequest(http.MethodPost, "/fixture", strings.NewReader(`{}`))
				r.URL.Path = path
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				switch handler {
				case "list":
					s.handleIssueAttachmentsList(w, r)
				case "read":
					s.handleIssueAttachmentRead(w, r)
				case "attach":
					s.handleIssueAttach(w, r)
				}
				if w.Code != want {
					t.Fatalf("unsafe attachment request status=%d want=%d body=%s", w.Code, want, w.Body.String())
				}
			})
		}
	}
}

func TestIssueAttachmentUploadRejectsMalformedMissingAndOversizedData(t *testing.T) {
	s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: "http://127.0.0.1:1", AgentID: "agent", AgentToken: "token"})
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"malformed", "not-json", 400},
		{"blank filename", `{"filename":"  ","content_base64":"YQ=="}`, 400},
		{"empty content", `{"filename":"report.txt"}`, 400},
		{"over transport cap", `{"filename":"report.txt","content_base64":"` + strings.Repeat("A", attachmentUploadBodyBytes) + `"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/issue/ISS-1/attachments", strings.NewReader(tc.body))
			r.Header.Set("Authorization", "Bearer token")
			w := httptest.NewRecorder()
			s.handleIssueAttach(w, r)
			if w.Code != tc.status {
				t.Fatalf("invalid upload status=%d want=%d", w.Code, tc.status)
			}
		})
	}
}

func TestIssueAttachmentsPreserveBackendFencesAndBindUploadAttribution(t *testing.T) {
	for _, mode := range []string{"list", "read", "attach", "backend refusal"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			response := `{"filename":"<untrusted>report.txt</untrusted>","content":"<untrusted>synthetic text</untrusted>","truncated":true}`
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("X-Internal-Token") != "ipc-token" {
					t.Error("missing IPC credential")
				}
				if mode == "attach" {
					var b map[string]any
					if json.NewDecoder(r.Body).Decode(&b) != nil || b["workspace_id"] != "workspace &" || b["agent_id"] != "trusted-agent" || b["filename"] != "report.txt" || b["content_base64"] != "YQ==" {
						t.Errorf("upload authority/body changed: %+v", b)
					}
					if _, ok := b["crew_id"]; ok {
						t.Error("caller crew authority forwarded")
					}
					if r.Method != "POST" {
						t.Error("wrong upload method")
					}
				} else {
					if r.Method != "GET" || r.URL.Query().Get("workspace_id") != "workspace &" || r.URL.Query().Has("crew_id") {
						t.Errorf("scope widened: %s %s", r.Method, r.URL)
					}
				}
				want := "/api/v1/internal/issues/ISS-1/attachments"
				if mode == "read" {
					want += "/file"
				}
				if r.URL.Path != want {
					t.Errorf("wrong attachment route: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				if mode == "backend refusal" {
					w.WriteHeader(403)
				}
				io.WriteString(w, response)
			}))
			defer backend.Close()
			s := newRoutineMCPTestServer(t, &IPCConfig{BaseURL: backend.URL, Token: "ipc-token", WorkspaceID: "workspace &", AgentID: "trusted-agent", AgentToken: "agent-token"})
			path := "/issue/ISS-1/attachments"
			if mode == "read" {
				path += "/file"
			}
			r := httptest.NewRequest(http.MethodPost, path+"?workspace_id=foreign&crew_id=foreign", strings.NewReader(`{"filename":"report.txt","content_base64":"YQ==","agent_id":"spoofed","workspace_id":"foreign","crew_id":"foreign"}`))
			r.Header.Set("Authorization", "Bearer agent-token")
			r.Host = "127.0.0.1:9119"
			r.RemoteAddr = "127.0.0.1:50000"
			w := httptest.NewRecorder()
			if mode != "attach" {
				r.Method = http.MethodGet
			}
			s.buildHandler(nil).ServeHTTP(w, r)
			want := 200
			if mode == "backend refusal" {
				want = 403
			}
			preserved := strings.TrimSpace(w.Body.String()) == response
			if mode == "attach" {
				var got, expected any
				preserved = json.Unmarshal(w.Body.Bytes(), &got) == nil && json.Unmarshal([]byte(response), &expected) == nil && reflect.DeepEqual(got, expected)
			}
			if calls.Load() != 1 || w.Code != want || !preserved {
				t.Fatalf("backend status/fencing changed: calls=%d status=%d body=%s", calls.Load(), w.Code, w.Body.String())
			}
		})
	}
}
