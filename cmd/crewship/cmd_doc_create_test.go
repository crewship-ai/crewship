package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

type docCreateTransport func(*http.Request) (*http.Response, error)

func (f docCreateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProtectedDocCreatePreservesOriginalBodyAndFixedOrigin(t *testing.T) {
	// Synthetic fixture capability only; the actual host validates its HMAC.
	const token = "agtv2.synthetic-workspace.synthetic-agent.synthetic-run.synthetic-signature"
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	t.Setenv("CREWSHIP_SERVER", "https://untrusted.invalid")
	t.Setenv("CREWSHIP_PROFILE", "untrusted-human-profile")
	const body = `{"slug":"created","name":"Created report","mutation_id":"exact-one","blocks":[{"type":"paragraph","text":"Finished content"}]}`
	calls := 0
	execute := func(input string) error {
		command := newDocCreateCommandWithTransport(docCreateTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			raw, err := io.ReadAll(r.Body)
			if err != nil || string(raw) != body || r.URL.String() != "http://127.0.0.1:9119/api/v1/documents/create" || r.Header.Get("Authorization") != "Bearer "+token {
				t.Fatalf("protected request changed: %s %s %v", r.URL, raw, err)
			}
			return &http.Response{StatusCode: 201, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"page_id":"server-page","revision":0,"url":"/pages?ref=server-page&workspace_id=workspace"}`))}, nil
		}))
		command.SetArgs([]string{"--file", "-"})
		command.SetIn(strings.NewReader(input))
		output := &bytes.Buffer{}
		command.SetOut(output)
		command.SetErr(io.Discard)
		err := command.Execute()
		if err == nil && (!strings.Contains(output.String(), "server-page") || strings.Contains(output.String(), token)) {
			t.Fatalf("invalid protected output %s", output.String())
		}
		return err
	}
	if err := execute(body); err != nil {
		t.Fatal(err)
	}
	if err := execute(body); err != nil || calls != 2 {
		t.Fatalf("exact body retry: calls%d err%v", calls, err)
	}
	if err := execute(strings.Replace(body, `"blocks":[`, `"owner_user_id":"other","blocks":[`, 1)); err == nil || calls != 2 {
		t.Fatal("unknown authoring field reached transport")
	}
	t.Setenv("CREWSHIP_AGENT_TOKEN", "")
	if err := execute(body); err == nil || calls != 2 {
		t.Fatal("missing run token fell back to a human profile")
	}
}

func TestProtectedDocCreateRejectsRedirectsAndOverrides(t *testing.T) {
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.test")
	calls := 0
	command := newDocCreateCommandWithTransport(docCreateTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://untrusted.invalid/receiver"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
	}))
	command.SetArgs([]string{"--file", "-"})
	command.SetIn(strings.NewReader(`{"slug":"s","name":"N","mutation_id":"one","blocks":[{"type":"paragraph","text":"body"}]}`))
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err == nil || calls != 1 {
		t.Fatalf("redirect followed or accepted: %d %v", calls, err)
	}
	command = newDocCreateCommandWithTransport(nil)
	command.Flags().String("server", "", "fixture override flag")
	command.SetArgs([]string{"--server", "https://untrusted.invalid", "--file", "-"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("server override accepted: %v", err)
	}
}
