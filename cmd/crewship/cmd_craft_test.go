package main

import (
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCraftCLIConnectPrivateTokenAndOpaqueGeneration(t *testing.T) {
	stub := pageStub(t)
	if findSubcommand(rootCmd.Commands(), "craft") == nil {
		t.Fatal("RED: craft command missing")
	}
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("a", 64)
	stub.OnPost(base+"/sessions", func(r *http.Request, b []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != "" {
			t.Error("session header leaked to admission")
		}
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"public-tab"}`), "application/json"
	})
	stub.OnPost(base+"/lease/renew", func(r *http.Request, b []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret {
			t.Error("missing private closure token")
		}
		if !strings.Contains(string(b), `"generation":"9007199254740993"`) {
			t.Errorf("generation lost: %s", b)
		}
		return 200, []byte(`{"generation":"9007199254740993"}`), "application/json"
	})
	out, err := runPageCLI(t, `{"action":"renew","generation":"9007199254740993"}`+"\n", "craft", "connect", "health")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") {
		t.Fatalf("token disclosed: %s", out)
	}
	if !strings.Contains(out, "public-tab") || !strings.Contains(out, "9007199254740993") {
		t.Fatalf("missing public output: %s", out)
	}
	if len(stub.CallsFor("POST", base+"/lease/renew")) != 1 {
		t.Fatal("missing renew")
	}
}

func TestCraftCLIRejectsWrongScopeAndUnknownFields(t *testing.T) {
	stub := pageStub(t)
	if findSubcommand(rootCmd.Commands(), "craft") == nil {
		t.Fatal("RED: craft command missing")
	}
	stub.OnPost("/api/v1/pages/health/craft/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + strings.Repeat("b", 64) + `","session_id":"tab"}`), "application/json"
	})
	if _, err := runPageCLI(t, `{"action":"renew","generation":"1","page":"other"}`+"\n", "craft", "connect", "health"); err == nil {
		t.Fatal("accepted unbound scope")
	}
}

func TestCraftCLIAllLeaseRoutesAndSSE(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	secret := strings.Repeat("c", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"tab"}`), "application/json"
	})
	suffixes := []string{"/lease", "/lease/acquire", "/lease/renew", "/lease/release", "/lease/request-handoff", "/lease/handoff", "/sessions/reauthorize"}
	for _, suffix := range suffixes {
		stub.OnPost(base+suffix, func(r *http.Request, _ []byte) (int, []byte, string) {
			if r.Header.Get("X-Craft-Session") != secret {
				t.Error("missing session binding")
			}
			return 200, []byte(`{"generation":"9007199254740993","session_token":"` + secret + `","message":"` + secret + `"}`), "application/json"
		})
	}
	stub.OnGet(base+"/lease", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"holder":null}`), "application/json"
	})
	stub.OnGet(base+"/events", func(r *http.Request, _ []byte) (int, []byte, string) {
		if r.Header.Get("X-Craft-Session") != secret {
			t.Error("unbound stream")
		}
		return 200, []byte("event: lease\ndata: {\"generation\":\"9007199254740993\"}\n\nevent: session_retired\ndata: {\"delivery\":\"stopped\",\"draft\":\"retain\"}\n\n"), "text/event-stream"
	})
	input := []string{`{"action":"status"}`, `{"action":"acquire"}`, `{"action":"acquire-explicit"}`, `{"action":"renew","generation":"9007199254740993"}`, `{"action":"release","generation":"1"}`, `{"action":"request-handoff"}`, `{"action":"handoff","generation":"1","request_id":"target-request"}`, `{"action":"reauthorize"}`, `{"action":"watch"}`}
	out, err := runPageCLI(t, strings.Join(input, "\n")+"\n", "craft", "connect", "health")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, secret) || strings.Contains(out, "session_token") {
		t.Fatalf("secret leaked: %s", out)
	}
	for _, suffix := range suffixes {
		if len(stub.CallsFor("POST", base+suffix)) != 1 {
			t.Fatalf("missing route %s", suffix)
		}
	}
	if len(stub.CallsFor("GET", base+"/events")) != 1 {
		t.Fatal("missing actual SSE request")
	}
}

func TestCraftCLIAvailabilityAndHTTPFailureNoRetry(t *testing.T) {
	stub := pageStub(t)
	base := "/api/v1/pages/health/craft"
	stub.OnGet(base+"/availability", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"enabled":true}`), "application/json"
	})
	if out, err := runPageCLI(t, "", "craft", "availability", "health"); err != nil || !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("availability %s %v", out, err)
	}
	secret := strings.Repeat("d", 64)
	stub.OnPost(base+"/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"tab"}`), "application/json"
	})
	stub.OnPost(base+"/lease/renew", func(*http.Request, []byte) (int, []byte, string) {
		return 403, []byte(`{"error":"` + secret + `"}`), "application/json"
	})
	out, err := runPageCLI(t, `{"action":"renew","generation":"1"}`+"\n"+`{"action":"acquire"}`+"\n", "craft", "connect", "health")
	if err == nil || strings.Contains(out, secret) {
		t.Fatalf("unsafe error %s %v", out, err)
	}
	if len(stub.CallsFor("POST", base+"/lease/renew")) != 1 || len(stub.CallsFor("POST", base+"/lease")) != 0 {
		t.Fatal("mutation retry or continued after denial")
	}
}

func TestCraftCLIAdmissionDoesNotEchoTokenAsPublicID(t *testing.T) {
	stub := pageStub(t)
	secret := strings.Repeat("e", 64)
	stub.OnPost("/api/v1/pages/health/craft/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + secret + `","session_id":"` + secret + `"}`), "application/json"
	})
	out, _ := runPageCLI(t, "", "craft", "connect", "health")
	if strings.Contains(out, secret) {
		t.Fatal("token echoed as malformed public ID")
	}
}

func TestCraftCLIRejectsNonHexSessionToken(t *testing.T) {
	stub := pageStub(t)
	stub.OnPost("/api/v1/pages/health/craft/sessions", func(*http.Request, []byte) (int, []byte, string) {
		return 201, []byte(`{"session_token":"` + strings.Repeat("Z", 64) + `","session_id":"tab"}`), "application/json"
	})
	if _, err := runPageCLI(t, "", "craft", "connect", "health"); err == nil {
		t.Fatal("accepted nonhex bearer")
	}
}

func TestCraftCLIWatchUnexpectedEOFIsUnconfirmed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: lease\ndata: {}\n\n")
	}))
	defer server.Close()
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	if err := craftWatch(cmd, cli.NewClient(server.URL, "", ""), "health", ""); err == nil {
		t.Fatal("unexpected EOF reported success")
	}
}

func TestCraftCLIWatchIgnoresOrdinaryRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond)
		io.WriteString(w, "event: session_retired\ndata: {\"delivery\":\"stopped\",\"draft\":\"retain\"}\n\n")
	}))
	defer server.Close()
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	client := cli.NewClient(server.URL, "", "").WithTimeout(30 * time.Millisecond)
	if err := craftWatch(cmd, client, "health", ""); err != nil {
		t.Fatalf("watch inherited ordinary timeout: %v", err)
	}
	if client.HTTPClient.Timeout != 30*time.Millisecond {
		t.Fatal("mutated shared client timeout")
	}
}

func TestCraftCLIPrivateTokenNeverFollowsCrossOriginRedirect(t *testing.T) {
	var calls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-Craft-Session") != "" {
			t.Error("private session crossed origin")
		}
	}))
	defer other.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, http.StatusFound) }))
	defer source.Close()
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	c := cli.NewClient(source.URL, "", "").WithHeader("X-Craft-Session", strings.Repeat("f", 64))
	if err := craftWatch(cmd, c, "health", strings.Repeat("f", 64)); err == nil {
		t.Fatal("followed redirect")
	}
	if calls.Load() != 0 {
		t.Fatal("destination contacted")
	}
}
