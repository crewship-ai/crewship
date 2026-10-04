//go:build linux

package restrictedruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type completionAuthority struct {
	*brokerFixtureAuthority
	completed     int
	issued        []json.RawMessage
	completeErr   error
	afterComplete func()
}

func (*completionAuthority) NativeRequest(context.Context, string, string, []byte) ([]byte, string, error) {
	return nil, "", ErrDenied
}
func (a *completionAuthority) NativeComplete(_ context.Context, handle, ticket string, issued []json.RawMessage) error {
	if handle != "h" || ticket != "owned-ticket" {
		return ErrDenied
	}
	a.completed++
	a.issued = issued
	if a.afterComplete != nil {
		a.afterComplete()
	}
	return a.completeErr
}

type failingNativeBody struct{}

func (failingNativeBody) Read([]byte) (int, error) { return 0, errors.New("synthetic read failure") }

func nativeDeliveryPlan(t *testing.T) Plan {
	t.Helper()
	p := streamingPlan()
	p.NativeSandbox = NativeSandboxFingerprint()
	p.Mounts = nil
	p.Credentials = nil
	ceiling, ok := NativeContextCeiling("gpt-5-mini")
	if !ok {
		t.Fatal("configured native fixture model unavailable")
	}
	p.Network.Credentials = []BrokerCredential{{ID: "model-key", Revision: "r1", Provider: "openai", Account: "account-a", Delivery: "broker-bearer-v1"}}
	p.Network.Grants = []HTTPGrant{{ID: "native", Revision: "r1", URL: "https://api.openai.com/v1/responses", Method: "POST", CredentialID: "model-key", MaxRequest: 1 << 20, MaxResponse: 1 << 20, TimeoutMillis: 300000, ResponseMode: "sse", Native: &NativePolicy{Model: "gpt-5-mini", MaxOutputTokens: 128, InputTokenCeiling: ceiling}}}
	if err := p.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestNativeDeliveryRequiresValidatedCompletionAndDurableReceipt(t *testing.T) {
	for _, kind := range []string{"valid", "content type", "upgrade", "encoding", "non-200", "declared oversize", "read failure", "body oversize", "provider secret", "wrong model", "invalid tool", "receipt rejected", "delivery rejected", "revoked before read", "revoked on receipt"} {
		t.Run(kind, func(t *testing.T) {
			p := nativeDeliveryPlan(t)
			s, base := brokerTestSession(t, p)
			authority := &completionAuthority{brokerFixtureAuthority: base}
			s.manager.Authority = authority
			item := map[string]any{"type": "message", "id": "msg_own", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "OWNED RESULT", "annotations": []any{}}}}
			if kind == "provider secret" {
				item["content"] = []any{map[string]any{"type": "output_text", "text": "provider-canary"}}
			}
			if kind == "invalid tool" {
				item = map[string]any{"type": "function_call", "id": "fc_own", "call_id": "call_own", "name": "web_search", "arguments": "{}"}
			}
			raw := nativeStreamFixture(t, item)
			raw = append([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"UNVETTED DELTA\"}\n\n"), raw...)
			if kind == "wrong model" {
				raw = bytes.ReplaceAll(raw, []byte("gpt-5-mini"), []byte("foreign-model"))
			}
			if kind == "body oversize" {
				raw = bytes.Repeat([]byte("x"), (1<<20)+1)
			}
			response := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, ContentLength: -1, Body: io.NopCloser(bytes.NewReader(raw))}
			switch kind {
			case "content type":
				response.Header.Set("Content-Type", "application/json")
			case "upgrade":
				response.Header.Set("Upgrade", "websocket")
			case "encoding":
				response.Header.Set("Content-Encoding", "gzip")
			case "non-200":
				response.StatusCode = 201
			case "declared oversize":
				response.ContentLength = (1 << 20) + 1
			case "read failure":
				response.Body = io.NopCloser(failingNativeBody{})
			case "receipt rejected":
				authority.completeErr = ErrDenied
			case "revoked before read":
				base.revoke("h")
			case "revoked on receipt":
				authority.afterComplete = func() { base.revoke("h") }
			}
			var frames []brokerFrame
			result, usage := s.brokerNativeStream(t.Context(), response, p.Network.Grants[0], BoundSecret{Value: "provider-canary", Expires: time.Now().Add(time.Minute)}, authority, "owned-ticket", func(frame brokerFrame) error {
				if authority.completed != 1 {
					t.Error("output emitted before durable receipt")
				}
				frames = append(frames, frame)
				if kind == "delivery rejected" {
					return ErrDenied
				}
				return nil
			})
			if kind != "valid" {
				if usage.Known || result.Status == 200 {
					t.Fatalf("failed delivery reported success/usage: %+v %+v", result, usage)
				}
				committed := kind == "receipt rejected" || kind == "delivery rejected" || kind == "revoked on receipt"
				if (authority.completed == 1) != committed {
					t.Fatalf("receipt count=%d for %s", authority.completed, kind)
				}
				if kind != "delivery rejected" && len(frames) != 0 {
					t.Fatalf("unchecked provider output emitted: %+v", frames)
				}
				return
			}
			if result.Kind != "stream_end" || result.Status != 200 || !usage.Known || usage.InputTokens != 10 || usage.OutputTokens != 10 {
				t.Fatalf("validated completion lost: %+v %+v", result, usage)
			}
			if authority.completed != 1 || len(authority.issued) != 1 || bytes.Contains(authority.issued[0], []byte(`"status"`)) {
				t.Fatalf("issued receipt not canonical: %s", authority.issued)
			}
			if len(frames) < 2 || frames[0].Kind != "stream_start" {
				t.Fatalf("stream protocol lost: %+v", frames)
			}
			var delivered bytes.Buffer
			for _, frame := range frames {
				delivered.Write(frame.Body)
			}
			if strings.Contains(delivered.String(), "UNVETTED DELTA") || !strings.Contains(delivered.String(), "OWNED RESULT") {
				t.Fatalf("delivery bypassed terminal reconstruction: %s", delivered.String())
			}
		})
	}
}

func TestNativeAdmissionBudgetUsesFullKnownModelContext(t *testing.T) {
	p := nativeDeliveryPlan(t)
	grant := p.Network.Grants[0]
	if !grant.validNative(p.Network.Credentials) {
		t.Fatal("known full-context native grant denied")
	}
	for _, kind := range []string{"missing native", "unknown model", "understated input ceiling", "foreign provider", "oversized output", "nonstreaming"} {
		t.Run(kind, func(t *testing.T) {
			g := grant
			policy := *grant.Native
			g.Native = &policy
			credentials := append([]BrokerCredential(nil), p.Network.Credentials...)
			switch kind {
			case "missing native":
				g.Native = nil
			case "unknown model":
				g.Native.Model = "nonexistent-native-model"
			case "understated input ceiling":
				g.Native.InputTokenCeiling--
			case "foreign provider":
				credentials[0].Provider = "foreign"
			case "oversized output":
				g.Native.MaxOutputTokens = 32769
			case "nonstreaming":
				g.ResponseMode = ""
			}
			if g.validNative(credentials) {
				t.Fatal("unsafe native admission accepted")
			}
		})
	}
}
