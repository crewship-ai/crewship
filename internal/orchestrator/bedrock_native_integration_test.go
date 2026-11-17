//go:build integration && (linux || darwin)

package orchestrator

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/bedrock"
)

func nativeBedrockFrame(event, payload string) []byte {
	var headers bytes.Buffer
	for _, pair := range [][2]string{{":message-type", "event"}, {":event-type", event}, {":content-type", "application/json"}} {
		headers.WriteByte(byte(len(pair[0])))
		headers.WriteString(pair[0])
		headers.WriteByte(7)
		_ = binary.Write(&headers, binary.BigEndian, uint16(len(pair[1])))
		headers.WriteString(pair[1])
	}
	frame := make([]byte, 16+headers.Len()+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(headers.Len()))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers.Bytes())
	copy(frame[12+headers.Len():], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

// Runs the actual pinned OpenCode package with Crewship's native Bedrock
// configuration/argv. AWS itself is replaced by a local ConverseStream fixture.
func TestBedrockNativeOpenCodeProtocol(t *testing.T) {
	dir := os.Getenv("CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	if dir == "" {
		t.Fatal("set CREWSHIP_CLI_QUALIFICATION_BIN_DIR")
	}
	req := bedrockRunFixture(t)
	req.UserMessage = "Reply with the fixture response"
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/llm/amazon-bedrock/model/eu.anthropic.claude-opus-5-5/converse-stream" || r.Header.Get(bedrock.AccountHeader) != "selected-account" || !strings.Contains(r.Header.Get("Authorization"), routedProviderDummyKey) {
			t.Errorf("unexpected native request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected protocol", 400)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		for _, e := range [][2]string{{"messageStart", `{"role":"assistant"}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"Bedrock fixture response"}}`}, {"contentBlockStop", `{"contentBlockIndex":0}`}, {"messageStop", `{"stopReason":"end_turn"}`}, {"metadata", `{"usage":{"inputTokens":10,"outputTokens":3,"totalTokens":13},"metrics":{"latencyMs":1}}`}} {
			_, _ = w.Write(nativeBedrockFrame(e[0], e[1]))
		}
	}))
	defer server.Close()
	generated, ok := localModelConfigEnv(req, true)
	if !ok {
		t.Fatal("no native Bedrock config")
	}
	var cfg map[string]any
	_ = json.Unmarshal([]byte(strings.TrimPrefix(generated, "OPENCODE_CONFIG_CONTENT=")), &cfg)
	providers := cfg["providers"].(map[string]any)
	p := providers[bedrock.NativeProvider].(map[string]any)
	p["settings"].(map[string]any)["baseURL"] = server.URL + "/llm/amazon-bedrock"
	raw, _ := json.Marshal(cfg)
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	argv := (opencodeAdapter{}).BuildCommand(req)
	cmd := qualificationCommand(ctx, argv[0], argv[1:]...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "PATH=" + dir + ":/usr/local/bin:/usr/bin:/bin", "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_CACHE_HOME=" + home + "/cache", "OPENCODE_CONFIG_CONTENT=" + string(raw), "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_CONFIG_PROJECT_DISABLE=1", "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_CLAUDE_CODE=1", "CI=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native Bedrock run: %v\n%s", err, out)
	}
	var text strings.Builder
	failed := false
	parse := (opencodeAdapter{}).NewStreamLineParser()
	for _, line := range bytes.Split(out, []byte("\n")) {
		parse(line, func(e AgentEvent) {
			if e.Type == "text" {
				text.WriteString(e.Content)
			}
			if e.Type == "error" {
				failed = true
			}
		})
	}
	if failed || calls.Load() == 0 || !strings.Contains(text.String(), "Bedrock fixture response") {
		t.Fatalf("calls=%d text=%s failed=%v\n%s", calls.Load(), text.String(), failed, out)
	}
}
