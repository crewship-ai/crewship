package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/artifacts"
)

func TestArtifactDataCLIOriginalRunPreservesExactValues(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.test")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	calls := 0
	transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if !bytes.Contains(raw, []byte(`"name":"artifact_data_batch"`)) || !bytes.Contains(raw, []byte(`9007199254740993`)) || r.Header.Get("Authorization") != "Bearer agtv2.test" || bytes.Contains(raw, []byte(`\u003c`)) || len(raw) > 300<<10 {
			t.Fatalf("run or exact JSON lost: %s", raw)
		}
		result, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"isError": false, "content": []map[string]string{{"type": "text", "text": `{"documents":[{"value":{"n":9007199254740993}}]}`}}}})
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(bytes.NewReader(result))}, nil
	})
	body := artifacts.DataBatch{BaseVersion: 1, Documents: []artifacts.DataWrite{{ID: "row", Value: json.RawMessage(`{"n":9007199254740993,"label":"` + strings.Repeat("<", 200<<10) + `"}`)}}}
	out, err := captureStdoutCov(t, func() error {
		return artifactCLIRequestWithTransport(cmd, "POST", "/api/v1/pages/demo/artifact/data/sales", body, transport)
	})
	if err != nil || calls != 1 {
		t.Fatalf("data CLI: %v calls%d", err, calls)
	}
	if !strings.Contains(out, "9007199254740993") {
		t.Fatalf("output value rounded: %s", out)
	}
}

func TestArtifactDataCLIOriginalRunPreservesUnicodeBudget(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.test")
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	value := json.RawMessage(`{"n":9007199254740993,"label":"` + strings.Repeat(string([]rune{0x2028, 0x2029}), 25000) + `"}`)
	raw := []byte(`{"base_version":1,"documents":[{"id":"row","ifVersion":0,"value":` + string(value) + `}]}`)
	body, err := artifacts.DecodeDataBatch(raw)
	if err != nil {
		t.Fatalf("invalid source fixture (%d bytes): %v", len(raw), err)
	}
	encoded, err := artifacts.MarshalDataJSON(body)
	if err != nil || len(encoded) > len(raw)+1024 {
		t.Fatalf("initial typed data encoding expanded source: %d -> %d: %v", len(raw), len(encoded), err)
	}
	calls := 0
	transport := artifactAgentRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		packet, _ := io.ReadAll(r.Body)
		if !bytes.Contains(packet, []byte(string(rune(0x2028)))) || !bytes.Contains(packet, []byte(string(rune(0x2029)))) || len(packet) > len(raw)+1024 {
			t.Fatalf("valid Unicode value expanded in protected CLI: source=%d packet=%d", len(raw), len(packet))
		}
		if !bytes.Contains(packet, []byte("9007199254740993")) {
			t.Fatal("exact number lost")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{}"}]}}`))}, nil
	})
	_, err = captureStdoutCov(t, func() error {
		return artifactCLIRequestWithTransport(cmd, "POST", "/api/v1/pages/demo/artifact/data/sales", body, transport)
	})
	if err != nil || calls != 1 {
		t.Fatalf("protected CLI: %v calls%d", err, calls)
	}
}
