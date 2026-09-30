//go:build linux && restrictedruntime_live

package restrictedruntime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveResponsesBroker(t *testing.T) {
	f := live(t)
	p := responsesPlan()
	p.Credentials = []Credential{{ID: "direct", Env: "DIRECT_TOKEN", File: "direct"}}
	p.Command = []string{"sh", "-c", `printf '%s' "$CREWSHIP_BROKER_TOKEN" > /home/agent/broker-token; exec sleep 3600`}
	f.m.Authority = &brokerFixtureAuthority{fixtureAuthority: f.a, material: BoundSecret{ID: "model-key", Revision: "r1", Provider: "openai", Account: "account-a", Value: "synthetic-provider-secret", Expires: time.Now().Add(time.Minute)}}
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "fixture-model" || body["max_output_tokens"] != float64(128) || body["store"] != false || body["stream"] != true || r.Header.Get("Authorization") != "Bearer synthetic-provider-secret" || r.URL.Path != "/v1/responses" {
			t.Error("provider identity or closed request policy lost")
		}
		if r.Header.Get("ChatGPT-Account-ID") != "" || r.Header.Get("OpenAI-Project") != "" {
			t.Error("caller account headers escaped the broker")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"SYNTHETIC_MODEL_OK\"}\n\n")
	}))
	t.Cleanup(server.Close)
	f.m.brokerTransport = syntheticBrokerTransport(t, server)
	f.m.brokerTransport.tls.ServerName = "example.com"
	a := f.start("text-a", p, "synthetic-direct-a")
	other := responsesPlan()
	other.Credentials = p.Credentials
	other.Attempt, other.Principal, other.Scope, other.OriginID = "text-b", "h2", "h2-scope", "chat2"
	other.Command = p.Command
	b := f.start("text-b", other, "synthetic-direct-b")
	call := `wget -q -T 3 -O - --post-data='` + textResponseRequest + `' --header="Authorization: Bearer $(cat /home/agent/broker-token)" --header='ChatGPT-Account-ID: foreign' --header='OpenAI-Project: foreign' http://127.0.0.1:9121/v1/responses`
	for _, s := range []*Session{a, b} {
		if got := f.shell(s, call); !strings.Contains(got, "SYNTHETIC_MODEL_OK") {
			t.Fatal("SDK route did not stream synthetic model text")
		}
		f.shell(s, `set -eu; test "$(id -u)" = 1001; test "$(stat -c %a /home/agent)" = 700; test "$(stat -c %u /broker)" = 1002; test ! -e /var/run/docker.sock; if ls /broker >/dev/null 2>&1; then exit 91; fi; needle=$(printf 'synthetic-provider-%s' secret); if grep -Raql "$needle" /home/agent /secrets /tmp 2>/dev/null; then exit 92; fi; for p in /proc/[0-9]*; do if cat "$p/environ" "$p/cmdline" 2>/dev/null | grep -q "$needle"; then exit 93; fi; done; echo uid-and-secret-boundaries-ok`)
	}
	if calls.Load() != 2 {
		t.Fatal("positive controls did not reach synthetic provider")
	}
	for _, bad := range []string{
		strings.Replace(call, "fixture-model", "foreign-model", 1),
		strings.Replace(call, `"stream":true`, `"stream":true,"previous_response_id":"resp_foreign"`, 1),
		strings.Replace(call, `"stream":true`, `"stream":true,"conversation":"conv_foreign"`, 1),
		strings.Replace(call, `"Reply OK"`, `[{"role":"user","content":[{"type":"input_file","file_id":"file_foreign"}]}]`, 1),
		strings.Replace(call, "/v1/responses", "/v1/responses/foreign", 1),
		strings.Replace(call, "/v1/responses", "/v1/responses?", 1),
		strings.Replace(call, "/v1/responses", "/v1/models", 1),
		strings.Replace(call, "/v1/responses", "/v1/responses/compact", 1),
		strings.Replace(call, "--header='ChatGPT-Account-ID: foreign'", "--header='Content-Encoding: gzip'", 1),
	} {
		f.shell(a, "if "+bad+"; then exit 94; fi")
	}
	foreign := strings.TrimSpace(f.shell(a, "cat /home/agent/broker-token"))
	f.shell(b, "if "+strings.Replace(call, "$(cat /home/agent/broker-token)", foreign, 1)+"; then exit 95; fi")
	if calls.Load() != 2 {
		t.Fatal("forbidden request reached provider")
	}
	f.a.revoke("text-a")
	select {
	case <-a.Done():
	case <-time.After(7 * time.Second):
		t.Fatal("revoked client runtime did not terminate")
	}
	if got := f.shell(b, call); !strings.Contains(got, "SYNTHETIC_MODEL_OK") || calls.Load() != 3 {
		t.Fatal("revocation of A affected independent B")
	}
	t.Log("two UID-1001 clients of one agent streamed bounded text via /v1/responses; foreign model/resource/token/path denied before upstream; provider credential absent from agent files/proc; revoked A terminated while B continued")
}
