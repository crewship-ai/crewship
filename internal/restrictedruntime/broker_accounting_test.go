//go:build linux

package restrictedruntime

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The synthetic authority models accounting callbacks; production Authority
// binds these callbacks to persistent shared paymaster budgets.
func (a *brokerFixtureAuthority) BrokerReserve(context.Context, string, string, string, int64, int64) (string, error) {
	return "fixture-reservation", nil
}
func (a *brokerFixtureAuthority) BrokerSettle(context.Context, string, string, BrokerUsage) error {
	return nil
}

func TestTerminalResponsesUsage(t *testing.T) {
	valid := `data: {"type":"response.completed","response":{"model":"fixture-model","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":2}}}}` + "\n\n"
	if got := terminalResponsesUsage([]byte(valid), "fixture-model"); !got.Known || got.InputTokens != 10 || got.OutputTokens != 5 || got.CachedInputTokens != 2 {
		t.Fatalf("usage not parsed: %+v", got)
	}
	for _, bad := range []string{valid + valid, strings.Replace(valid, `"input_tokens":10`, `"input_tokens":10,"input_tokens":1`, 1), strings.Replace(valid, `"total_tokens":15`, `"total_tokens":16`, 1), strings.Replace(valid, `"cached_tokens":2`, `"cached_tokens":20`, 1), strings.Replace(valid, `"status":"completed"`, `"status":"failed"`, 1), strings.Replace(valid, `"output_tokens":5`, `"output_tokens":-1`, 1), strings.Replace(valid, "fixture-model", "other", 1), `data: {"type":"response.output_text.delta","delta":"hello"}` + "\n\n"} {
		if terminalResponsesUsage([]byte(bad), "fixture-model").Known {
			t.Fatal("invalid terminal usage accepted", bad)
		}
	}
}

type measuredAccounting struct {
	*brokerFixtureAuthority
	denied            bool
	reserved, settled atomic.Int32
	usage             BrokerUsage
}

func (a *measuredAccounting) BrokerReserve(_ context.Context, _ string, _ string, model string, input, output int64) (string, error) {
	a.reserved.Add(1)
	if a.denied || model != "fixture-model" || input < 16384 || output != 128 {
		return "", ErrDenied
	}
	return "measured", nil
}
func (a *measuredAccounting) BrokerSettle(_ context.Context, _ string, id string, usage BrokerUsage) error {
	if id != "measured" {
		return ErrDenied
	}
	a.settled.Add(1)
	a.usage = usage
	return nil
}

func TestBrokerAccountingBeforeProviderAndTerminalSettlement(t *testing.T) {
	for _, mode := range []string{"known", "no usage", "canceled delivery", "denied reservation", "missing accounting"} {
		t.Run(mode, func(t *testing.T) {
			s, base := brokerTestSession(t, responsesPlan())
			base.material = BoundSecret{ID: "model-key", Revision: "r1", Provider: "openai", Account: "account-a", Value: "synthetic-secret", Expires: time.Now().Add(time.Minute)}
			measured := &measuredAccounting{brokerFixtureAuthority: base, denied: mode == "denied reservation"}
			s.manager.Authority = measured
			if mode == "missing accounting" {
				s.manager.Authority = &noAccountingAuthority{base}
			}
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if measured.reserved.Load() != 1 {
					t.Error("provider dispatched before reservation")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if mode == "known" || mode == "canceled delivery" {
					io.WriteString(w, `data: {"type":"response.completed","response":{"model":"fixture-model","status":"completed","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`+"\n\n")
				} else {
					io.WriteString(w, "data: {}\n\n")
				}
			}))
			defer server.Close()
			tr := syntheticBrokerTransport(t, server)
			tr.tls.ServerName = "example.com"
			result := s.brokerExchange(t.Context(), "token", brokerFrame{Token: "token", Operation: "echo", Body: []byte(textResponseRequest)}, *tr, func(f brokerFrame) error {
				if mode == "canceled delivery" {
					return ErrDenied
				}
				return nil
			})
			if mode == "denied reservation" || mode == "missing accounting" {
				if calls != 0 || result.Status != 403 || measured.settled.Load() != 0 {
					t.Fatalf("unaccounted request escaped: %+v calls=%d settled=%d", result, calls, measured.settled.Load())
				}
				return
			}
			if calls != 1 || measured.settled.Load() != 1 || measured.usage.Known != (mode == "known") {
				t.Fatalf("wrong settlement calls=%d settled=%d usage=%+v", calls, measured.settled.Load(), measured.usage)
			}
		})
	}
}

// Do not promote accounting methods from the normal test fixture.
type noAccountingAuthority struct{ delegate *brokerFixtureAuthority }

func (a *noAccountingAuthority) Resolve(ctx context.Context, h string) (Plan, error) {
	return a.delegate.Resolve(ctx, h)
}
func (a *noAccountingAuthority) Secrets(ctx context.Context, h string) (map[string]string, error) {
	return a.delegate.Secrets(ctx, h)
}
func (a *noAccountingAuthority) BrokerSecret(ctx context.Context, h, id string) (BoundSecret, error) {
	return a.delegate.BrokerSecret(ctx, h, id)
}
