package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBudgetGateStopsEveryProviderAttempt(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, denyReplay := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "forward", true: "reverse"}[reverse], map[bool]string{false: "initial", true: "replay"}[denyReplay]}, "/"), func(t *testing.T) {
				p, _, up := newGraceProxy(t, []Credential{anthropicCred("c1", "sk-new", "sk-old", time.Now().Add(time.Hour))}, []int{401, 200}, nil, nil)
				gates := 0
				p.onLLMAdmission = func(_ context.Context, actor, credential, provider string) error {
					gates++
					if credential != "c1" || provider != "ANTHROPIC" {
						t.Fatalf("wrong binding %q %q", credential, provider)
					}
					if !denyReplay || gates == 2 {
						return errors.New("denied")
					}
					return nil
				}
				target := "http://api.anthropic.com/v1/messages"
				if reverse {
					target = "http://127.0.0.1:9119/v1/messages"
				}
				rr := httptest.NewRecorder()
				req := httptest.NewRequest("POST", target, strings.NewReader(`{"model":"claude"}`))
				req.RemoteAddr = "127.0.0.1:54321"
				p.ServeHTTP(rr, req)
				wantCalls := 0
				wantCode := 403
				wantGates := 1
				if denyReplay {
					wantCalls = 1
					wantCode = 401
					wantGates = 2
				}
				if len(up.calls) != wantCalls || rr.Code != wantCode || gates != wantGates {
					t.Fatalf("network=%d code=%d gates=%d", len(up.calls), rr.Code, gates)
				}
			})
		}
	}
}

func TestManagedBudgetAdmissionFailClosed(t *testing.T) {
	if (&Server{}).buildLLMAdmission()(context.Background(), "a", "c", "OPENAI") == nil {
		t.Fatal("missing IPC admitted")
	}
	for _, answer := range []string{`{"allowed":true}`, `{"allowed":false}`, `{"allowed":true,"billing_mode":"flat_rate"}`, `{"allowed":true} {}`} {
		t.Run(answer, func(t *testing.T) {
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/internal/cost/admit" || r.Header.Get("X-Internal-Token") != "token" {
					t.Error("wrong IPC binding")
				}
				var body map[string]string
				if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 3 || body["agent_id"] != "actor" || body["credential_id"] != "c" || body["provider"] != "OPENAI" {
					t.Error("request did not bind host-selected identity")
				}
				w.Write([]byte(answer))
			}))
			defer host.Close()
			s := &Server{ipc: &IPCConfig{BaseURL: host.URL, Token: "token", WorkspaceID: "w", AgentID: "boot"}}
			err := s.buildLLMAdmission()(context.Background(), "actor", "c", "OPENAI")
			if (err == nil) != (answer == `{"allowed":true}`) {
				t.Fatalf("answer %s error %v", answer, err)
			}
		})
	}
}

func TestBudgetGateOpaqueTunnelBeforeDial(t *testing.T) {
	p, _, _ := newGraceProxy(t, nil, nil, nil, nil)
	p.onLLMAdmission = func(_ context.Context, _ string, credential, provider string) error {
		if credential != "" || provider != "OPAQUE_TUNNEL" {
			t.Fatal("tunnel attributed")
		}
		return errors.New("denied")
	}
	rr := httptest.NewRecorder()
	r := httptest.NewRequest("CONNECT", "http://api.openai.com:443", nil)
	r.Host = "api.openai.com:443"
	p.ServeHTTP(rr, r)
	if rr.Code != 403 {
		t.Fatalf("code %d", rr.Code)
	}
}
