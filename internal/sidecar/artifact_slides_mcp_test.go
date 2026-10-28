package sidecar

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactSlidesMCPClosedArgumentsAndRequiredPin(t *testing.T) {
	for _, name := range []string{"artifact_slides_read", "artifact_export_pdf"} {
		t.Run(name, func(t *testing.T) {
			packet, handled, err := artifactProjectMCPPacket(name, []byte(`{"slug":"@page-id","version":2}`))
			if err != nil || !handled || !bytes.Contains(packet, []byte(`"version":2`)) {
				t.Fatalf("tool unavailable: handled%v %v %s", handled, err, packet)
			}
			for _, raw := range []string{`{"slug":"demo"}`, `{"slug":"demo","version":0}`, `{"slug":"demo","version":9007199254740992}`, `{"slug":"demo","version":2,"version":3}`, `{"slug":"demo","Version":2}`, `{"slug":"demo","version":2,"out":"/host/out.pdf"}`, `{"slug":"demo","version":2,"user_id":"spoof"}`, `{"slug":"../victim","version":2}`} {
				if _, handled, err := artifactProjectMCPPacket(name, []byte(raw)); !handled || err == nil {
					t.Errorf("accepted %s", raw)
				}
			}
		})
	}
}
func TestArtifactSlidesMCPForwardsOriginalRunToExactHostGateway(t *testing.T) {
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	calls := 0
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/internal/pages/artifact-tool" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("original identity/gateway changed")
		}
		var got struct {
			Operation string                     `json:"operation"`
			Slug      string                     `json:"slug"`
			Payload   map[string]json.RawMessage `json:"payload"`
		}
		if json.NewDecoder(r.Body).Decode(&got) != nil || got.Operation != "artifact_slides_read" || got.Slug != "@page-id" || len(got.Payload) != 1 || string(got.Payload["version"]) != "2" {
			t.Errorf("mandate changed: %+v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"deck":{},"anchors":{}}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"artifact_slides_read","arguments":{"slug":"@page-id","version":2}}}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	isError, text := documentCreateMCPResult(t, w)
	if isError || calls != 1 || !strings.Contains(text, `"anchors"`) {
		t.Fatalf("forward failure calls%d error%v %s", calls, isError, text)
	}
}

func TestArtifactSlidesMCPKeepsRequestAndResponseBounds(t *testing.T) {
	for _, name := range []string{"artifact_slides_read", "artifact_export_pdf"} {
		t.Run(name, func(t *testing.T) {
			s, key := newRunIdentityServer(t)
			token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
			var hostCalls atomic.Int32
			body := `{"deck":{"padding":"` + strings.Repeat("a", 3<<20) + `"},"anchors":{}}`
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hostCalls.Add(1); _, _ = w.Write([]byte(body)) }))
			defer host.Close()
			s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
			call := func(arguments string) *httptest.ResponseRecorder {
				raw := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":` + arguments + `}}`
				r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(raw))
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				s.handleRoutinesMCP(w, r)
				return w
			}
			w := call(`{"slug":"demo","version":2}`)
			isError, text := documentCreateMCPResult(t, w)
			if isError || len(text) != len(body) || hostCalls.Load() != 1 {
				t.Fatalf("new read response inherited Project's smaller bound: error%v length%d calls%d", isError, len(text), hostCalls.Load())
			}
			w = call(`{"slug":"demo","version":2,"extra":"` + strings.Repeat("x", 1<<20) + `"}`)
			if w.Code != 400 || hostCalls.Load() != 1 {
				t.Fatalf("Slides request bound widened: status%d calls%d", w.Code, hostCalls.Load())
			}
		})
	}
}

// Synthetic PDF-shaped bytes exercise transport bounds only, not PDF rendering.
func TestArtifactSlidesMCPNearEightMiBPDFAndOldProjectLimit(t *testing.T) {
	pdf := append([]byte("%PDF-1.7\n"), bytes.Repeat([]byte("x"), (8<<20)-9)...)
	if len(pdf) != 8<<20 {
		t.Fatal("exact PDF limit fixture required")
	}
	sum := sha256.Sum256(pdf)
	dto, _ := json.Marshal(map[string]any{"media_type": "application/pdf", "encoding": "base64", "code_version": 2, "byte_length": len(pdf), "sha256": hex.EncodeToString(sum[:]), "data": base64.StdEncoding.EncodeToString(pdf)})
	if len(dto) <= 8<<20 || len(dto) >= 16<<20 {
		t.Fatal("DTO boundary fixture invalid")
	}
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/internal/pages/artifact-tool" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Internal-Token") != "synthetic-crew-ipc" {
			t.Error("immutable host gateway transport changed")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(dto)
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	call := func(name string) *httptest.ResponseRecorder {
		raw := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + name + `","arguments":{"slug":"@page-id","version":2}}}`
		r := httptest.NewRequest("POST", "/mcp/routines", strings.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.handleRoutinesMCP(w, r)
		return w
	}
	w := call("artifact_export_pdf")
	isError, text := documentCreateMCPResult(t, w)
	if isError || len(text) != len(dto) || w.Body.Len() >= 32<<20 || calls.Load() != 1 {
		t.Fatalf("legal PDF host response rejected: error%v DTO%d RPC%d calls%d", isError, len(text), w.Body.Len(), calls.Load())
	}
	var received struct {
		Data       string `json:"data"`
		SHA256     string `json:"sha256"`
		ByteLength int    `json:"byte_length"`
	}
	if json.Unmarshal([]byte(text), &received) != nil {
		t.Fatal("invalid tool DTO")
	}
	got, err := base64.StdEncoding.Strict().DecodeString(received.Data)
	if err != nil || len(got) != 8<<20 || !bytes.Equal(got, pdf) || received.SHA256 != hex.EncodeToString(sum[:]) || received.ByteLength != len(pdf) {
		t.Fatal("large PDF changed through actual sidecar/host transport")
	}
	w = call("artifact_versions")
	isError, text = documentCreateMCPResult(t, w)
	if !isError || !strings.Contains(text, "outcome unavailable") || len(text) > 1024 || calls.Load() != 2 {
		t.Fatalf("old Project host limit widened or retried: error%v text%d calls%d", isError, len(text), calls.Load())
	}
}
