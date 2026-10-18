package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

const draftPrepareFixture = `{"view":{"page_id":"own-page","schema":3,"rev":0,"generation":1,"epoch":"opaque-epoch","targets":[{"id":"p1","type":"paragraph","parent":null,"text":"A","hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},"target":"p1","text":"B","mutation_id":"draft-one"}`

func runDocDraft(t *testing.T, op, raw string, transport http.RoundTripper) (string, error) {
	t.Helper()
	cmd := newDocDraftCommandWithTransport(transport)
	cmd.SetArgs([]string{op, "--file", "-"})
	cmd.SetIn(strings.NewReader(raw))
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	var compact bytes.Buffer
	if json.Compact(&compact, out.Bytes()) == nil {
		return compact.String(), err
	}
	return out.String(), err
}
func TestProtectedDocDraftPrepareApplyAndExactRetry(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	const token = "agtv2.synthetic-draft-capability"
	t.Setenv("CREWSHIP_AGENT_TOKEN", token)
	t.Setenv("CREWSHIP_SERVER", "https://untrusted.invalid")
	t.Setenv("CREWSHIP_PROFILE", "human-profile")
	prepared, err := runDocDraft(t, "prepare", draftPrepareFixture, docCreateTransport(func(*http.Request) (*http.Response, error) {
		t.Fatal("prepare must not send a mutation")
		return nil, nil
	}))
	if err != nil || !strings.Contains(prepared, "prepared_unsaved") {
		t.Fatalf("prepare: %v %s", err, prepared)
	}
	calls := []string{}
	transport := docCreateTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "http://127.0.0.1:9119/api/v1/documents/draft" || r.Header.Get("Authorization") != "Bearer "+token || r.Method != "POST" {
			t.Fatalf("scope or credential changed: %s", r.URL)
		}
		raw, _ := io.ReadAll(r.Body)
		calls = append(calls, string(raw))
		var in struct {
			Operation string `json:"operation"`
		}
		_ = json.Unmarshal(raw, &in)
		id := "draft-one"
		pinField := ""
		if in.Operation == "checkpoint" {
			id += "-checkpoint"
			pinField = `,"checkpoint":"` + id + `"`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"` + id + `","rev":1,"epoch":"opaque-epoch"` + pinField + `}`))}, nil
	})
	for range 2 {
		out, err := runDocDraft(t, "apply", prepared, transport)
		if err != nil || !strings.Contains(out, `"status":"checkpointed"`) {
			t.Fatalf("apply: %v %s", err, out)
		}
	}
	if len(calls) != 4 || calls[0] != calls[2] || calls[1] != calls[3] {
		t.Fatalf("retry regenerated original requests: %v", calls)
	}
	out, err := runDocDraft(t, "checkpoint", prepared, transport)
	if err != nil || !strings.Contains(out, "checkpointed") || len(calls) != 5 || calls[4] != calls[1] {
		t.Fatalf("pin retry changed packet: %v %s", err, out)
	}
}
func TestProtectedDocDraftReportsCommittedPatchWhenPinFails(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.aaaa")
	prepared, err := runDocDraft(t, "prepare", draftPrepareFixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	out, err := runDocDraft(t, "apply", prepared, docCreateTransport(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 2 {
			return nil, errors.New("synthetic connection lost")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"draft-one","rev":1,"epoch":"opaque-epoch"}`))}, nil
	}))
	if err == nil || calls != 2 || !strings.Contains(out, "committed_checkpoint_pending") || !strings.Contains(out, `"patch"`) || strings.Contains(out, "unsaved") {
		t.Fatalf("committed patch misrepresented: %v %s", err, out)
	}
}
func TestProtectedDocDraftRefusesUnsafePacketsAndHumanFallback(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.aaaa")
	prepared, err := runDocDraft(t, "prepare", draftPrepareFixture, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{strings.Replace(prepared, `"generation":1`, `"generation":null`, 1), strings.Replace(prepared, `"ifRev":1`, `"ifRev":2`, 1), strings.Replace(prepared, `"page_id":"own-page"`, `"page_id":"other"`, 1), strings.Replace(prepared, `"text":"B"`, `"text":"B","text":"C"`, 1), strings.Replace(prepared, `"packet":{`, `"owner":"other","packet":{`, 1), strings.Repeat("x", (1<<20)+1)} {
		calls := 0
		_, err := runDocDraft(t, "apply", raw, docCreateTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, nil }))
		if err == nil || calls != 0 {
			t.Fatal("unsafe packet reached transport")
		}
	}
	t.Setenv("CREWSHIP_AGENT_TOKEN", "")
	_, err = runDocDraft(t, "apply", prepared, nil)
	if err == nil || !strings.Contains(err.Error(), "no human profile fallback") {
		t.Fatalf("human fallback: %v", err)
	}
}
func TestProtectedDocDraftViewAndTransportGuards(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.aaaa")
	for _, test := range []struct {
		name, body string
		status     int
	}{{"redirect", `{}`, 302}, {"invalid response", `not JSON`, 200}, {"oversized", strings.Repeat("x", (64<<10)+1), 200}} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			cmd := newDocDraftCommandWithTransport(docCreateTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" {
					t.Fatal("view mutated")
				}
				return &http.Response{StatusCode: test.status, Header: http.Header{"Location": []string{"https://untrusted.invalid"}}, Body: io.NopCloser(strings.NewReader(test.body)), Request: r}, nil
			}))
			cmd.SetArgs([]string{"view"})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			if err := cmd.Execute(); err == nil || calls != 1 {
				t.Fatalf("unsafe response: %d %v", calls, err)
			}
		})
	}
	cmd := newDocDraftCommandWithTransport(nil)
	cmd.PersistentFlags().String("server", "", "fixture override")
	cmd.SetArgs([]string{"view", "--server", "https://untrusted.invalid"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "does not accept") {
		t.Fatalf("override accepted: %v", err)
	}
}

func TestProtectedDocDraftViewPreservesHTTPStatus(t *testing.T) {
	guardCLIState(t)
	flagFormat = "json"
	t.Setenv("CREWSHIP_AGENT_TOKEN", "agtv2.aaaa")
	command := newDocDraftCommandWithTransport(docCreateTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 409, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"private-body-not-an-error-message"}`))}, nil
	}))
	command.SetArgs([]string{"view"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "HTTP409") || strings.Contains(err.Error(), "private-body-not-an-error-message") {
		t.Fatalf("view status lost or body exposed: %v", err)
	}
}
