package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactDataMCPPacketKeepsExactCASAndRejectsIdentity(t *testing.T) {
	valid := `{"slug":"demo","collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"n":9007199254740993}}]}`
	packet, handled, err := artifactProjectMCPPacket("artifact_data_batch", []byte(valid))
	if err != nil || !handled || !bytes.Contains(packet, []byte(`9007199254740993`)) {
		t.Fatalf("packet changed value: %s %v", packet, err)
	}
	for _, raw := range []string{
		strings.Replace(valid, `"ifVersion":0,`, "", 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":0,"IfVersion":1`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":1,"BASE_VERSION":2`, 1),
		strings.Replace(valid, `"ifVersion":0`, `"ifVersion":9007199254740993`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":0`, 1),
		strings.Replace(valid, `"base_version":1`, `"base_version":1,"updated_by":"spoof"`, 1),
		strings.Replace(valid, `"collection":"sales"`, `"collection":"../other"`, 1),
	} {
		if _, _, err := artifactProjectMCPPacket("artifact_data_batch", []byte(raw)); err == nil {
			t.Errorf("invalid packet accepted: %s", raw)
		}
	}
}

func TestArtifactDataMCPEnvelopeDoesNotExpandHTMLLikeValues(t *testing.T) {
	for _, value := range []string{strings.Repeat("<", 1<<20), strings.Repeat(string([]rune{0x2028, 0x2029}), 25000)} {
		w := httptest.NewRecorder()
		s := &Server{}
		body := []byte(`{"value":"` + value + `"}`)
		s.writeArtifactDataMCPResult(w, memoryMCPRequest{ID: json.RawMessage(`1`)}, 200, body)
		if w.Code != 200 || w.Body.Len() > len(body)+1024 {
			t.Fatalf("data envelope expanded: %d -> %d bytes", len(body), w.Body.Len())
		}
	}

}

func TestArtifactDataMCPBoundaryValueKeepsWireBudget(t *testing.T) {
	value := strings.Repeat("<", 200<<10)
	raw := []byte(`{"slug":"demo","collection":"sales","base_version":1,"documents":[{"id":"row","ifVersion":0,"value":{"DisplayName":"` + value + `","optional":null,"huge":1e400}}]}`)
	packet, handled, err := artifactProjectMCPPacket("artifact_data_batch", raw)
	if err != nil || !handled || len(packet) > len(raw)+1024 || bytes.Contains(packet, []byte(`\u003c`)) {
		t.Fatalf("valid data expanded or rejected: %d %v", len(packet), err)
	}
}

func TestArtifactDataMCPBatchMetadataHeadroomRetainsProjectLimit(t *testing.T) {
	body := artifacts.DataBatch{BaseVersion: 1}
	for i := 0; i < 4; i++ {
		body.Documents = append(body.Documents, artifacts.DataWrite{ID: fmt.Sprintf("row%d", i), Value: json.RawMessage(`{"x":""}`)})
	}
	empty, _ := artifacts.MarshalDataJSON(body)
	remaining := artifacts.MaxDataBatchBytes - 8 - len(empty)
	for i := range body.Documents {
		n := remaining / (len(body.Documents) - i)
		remaining -= n
		body.Documents[i].Value = json.RawMessage(`{"x":"` + strings.Repeat("a", n) + `"}`)
	}
	raw, _ := artifacts.MarshalDataJSON(body)
	if _, err := artifacts.DecodeDataBatch(raw); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"slug": "demo", "collection": "sales", "base_version": body.BaseVersion, "documents": body.Documents}
	arguments, _ := artifacts.MarshalDataJSON(args)
	if len(arguments) <= artifacts.MaxDataBatchBytes {
		t.Fatal("fixture missed arguments overhead")
	}
	if _, _, err := artifactProjectMCPPacket("artifact_data_batch", arguments); err != nil {
		t.Errorf("valid batch arguments rejected: %v", err)
	}
	s, key := newRunIdentityServer(t)
	token := internaltoken.DeriveAgentRunToken(key, "ws-1", "agent-boot", "run-boot")
	var calls atomic.Int32
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("original run lost")
		}
		var packet struct {
			Operation string `json:"operation"`
			Payload   struct {
				Collection  string                `json:"collection"`
				BaseVersion int64                 `json:"base_version"`
				Documents   []artifacts.DataWrite `json:"documents"`
			} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&packet); err != nil {
			t.Error(err)
		}
		bounded, _ := artifacts.MarshalDataJSON(artifacts.DataBatch{BaseVersion: packet.Payload.BaseVersion, Documents: packet.Payload.Documents})
		if _, err := artifacts.DecodeDataBatch(bounded); err != nil || packet.Operation != "artifact_data_batch" {
			t.Errorf("batch changed in transit: %v", err)
		}
		_, _ = w.Write([]byte(`{"collection":"sales","code_version":1,"documents":[]}`))
	}))
	defer host.Close()
	s.ipc.BaseURL, s.ipc.Token = host.URL, "synthetic-crew-ipc"
	frame, _ := artifacts.MarshalDataJSON(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "artifact_data_batch", "arguments": args}})
	r := httptest.NewRequest("POST", "/mcp/routines", bytes.NewReader(frame))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("valid data RPC rejected: status%d calls%d body%s", w.Code, calls.Load(), w.Body)
	}
	project := bytes.Replace(frame, []byte("artifact_data_batch"), []byte("artifact_publish"), 1)
	r = httptest.NewRequest("POST", "/mcp/routines", bytes.NewReader(project))
	r.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	s.handleRoutinesMCP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "oversized MCP request") || calls.Load() != 1 {
		t.Fatalf("Project outer1MiB bound widened: status%d calls%d", w.Code, calls.Load())
	}
	// Metadata allowance must never become extra dataset body capacity.
	body.Documents[0].Value = json.RawMessage(`{"x":"` + strings.Repeat("b", len(body.Documents[0].Value)-8+32) + `"}`)
	oversized, _ := artifacts.MarshalDataJSON(args)
	if _, _, err := artifactProjectMCPPacket("artifact_data_batch", oversized); err == nil {
		t.Fatal("data body limit widened with metadata envelope")
	}
}
