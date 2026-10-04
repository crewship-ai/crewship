//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The same tests run as the host identity and as the real 1001/1002 identities
// in isolated acceptance containers. No identity lookup is mocked.
func TestRunnerIdentityAndStdinProtocol(t *testing.T) {
	oldArgs, oldInput := os.Args, os.Stdin
	defer func() { os.Args, os.Stdin = oldArgs, oldInput }()
	invoke := func(mode, body string) error {
		t.Helper()
		file, err := os.CreateTemp(t.TempDir(), "stdin")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if _, err := file.WriteString(body); err != nil {
			t.Fatal(err)
		}
		if _, err := file.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		os.Stdin = file
		os.Args = []string{"runner", mode}
		return run()
	}
	for _, mode := range []string{"hold", "lease", "broker", "launch", "responses"} {
		uid := 1002
		if mode == "launch" || mode == "responses" {
			uid = 1001
		}
		if os.Getuid() == uid {
			continue
		}
		os.Args = []string{"runner", mode}
		if mode == "responses" {
			os.Args = append(os.Args, "{}")
		}
		if err := run(); err == nil || err.Error() != "identity" {
			t.Fatalf("uid %d admitted %s: %v", os.Getuid(), mode, err)
		}
	}
	if os.Getuid() == 1002 {
		if err := invoke("lease", "not-json"); err == nil {
			t.Fatal("invalid lease decoded")
		}
		expired, _ := json.Marshal(time.Now().Add(-time.Hour))
		if err := invoke("lease", string(expired)); err == nil {
			t.Fatal("expired lease admitted")
		}
		if err := invoke("broker", ""); err == nil {
			t.Fatal("broker accepted missing configuration")
		}
	}
	if os.Getuid() != 1001 {
		return
	}
	if _, err := os.Stat("/opt/crewship-runner"); !os.IsNotExist(err) {
		t.Fatal("identity fixture requires an absent /opt/crewship-runner; run in its isolated acceptance image")
	}
	for _, body := range []string{"not-json", `{"unknown":true}`, `{}`, `{"command":["/bin/sh","-c","true"],"env":{"a":"b","c":"d"}}`} {
		if err := invoke("launch", body); err == nil {
			t.Fatalf("invalid bootstrap admitted: %s", body)
		}
	}
	for _, tc := range []struct{ url, token string }{
		{"http://127.0.0.1:9999", strings.Repeat("a", 64)},
		{"http://127.0.0.1:9121", "short"},
		{"http://127.0.0.1:9121", strings.Repeat("a", 64)},
	} {
		raw, err := json.Marshal(map[string]any{"command": []string{"/opt/crewship-runner", "responses", "{}"}, "env": map[string]string{"CREWSHIP_BROKER_URL": tc.url, "CREWSHIP_BROKER_TOKEN": tc.token}})
		if err != nil {
			t.Fatal(err)
		}
		// Acceptance image deliberately has no executable at /opt/crewship-runner.
		// A valid bootstrap must report that missing executable, never run a fallback.
		if err := invoke("launch", string(raw)); err == nil {
			t.Fatal("invalid or missing launcher accepted")
		}
	}
	os.Args = []string{"runner", "responses", "not-json"}
	if err := run(); err == nil || err.Error() != "request" {
		t.Fatalf("worker request validation: %v", err)
	}
}

func TestAgentResponsesUseOwnedBrokerAndRefuseRedirects(t *testing.T) {
	if os.Getuid() != 1001 {
		old := os.Args
		defer func() { os.Args = old }()
		os.Args = []string{"runner", "responses", "{}"}
		if err := run(); err == nil || err.Error() != "identity" {
			t.Fatalf("non-agent identity bypass: %v", err)
		}
		return
	}
	// Bind before making any request: an occupied port fails this fixture rather
	// than talking to an unrelated broker. Docker acceptance uses --network none.
	listener, err := net.Listen("tcp4", "127.0.0.1:9121")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_BROKER_TOKEN", "owned-fixture-token")
	var status atomic.Int32
	status.Store(200)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer owned-fixture-token" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("request escaped the fixed broker contract")
		}
		code := int(status.Load())
		if code == 302 {
			w.Header().Set("Location", "/forbidden-redirect")
		}
		w.WriteHeader(code)
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"))
	})}
	done := make(chan struct{})
	go func() { defer close(done); _ = server.Serve(listener) }()
	defer func() { _ = server.Close(); <-done }()
	for _, code := range []int{200, 403, 302} {
		status.Store(int32(code))
		var out bytes.Buffer
		err := responses(context.Background(), "{}", &out)
		if code == 200 {
			if err != nil || out.String() != "{\"type\":\"done\"}\n" {
				t.Fatalf("success protocol: %q %v", out.String(), err)
			}
		} else if err == nil || out.Len() != 0 {
			t.Fatalf("refusal %d produced success: %q %v", code, out.String(), err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := responses(ctx, "{}", &bytes.Buffer{}); err == nil {
		t.Fatal("cancelled request continued")
	}
}
