package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeSlashClient implements SlashHTTPClient for testing. Each call
// pulls the next response off the slice so a test can script GET +
// POST behaviour independently. Body is read but ignored; only the
// status code and the body string are inspected by buildSlashHandler.
type fakeSlashClient struct {
	wsID      string
	getCalls  int
	postCalls int
	// getResp is consumed FIFO; nil entry means "return error".
	getResp []*http.Response
	getErr  []error
	// postResp same shape, indexed in call order.
	postResp []*http.Response
	postErr  []error
}

func (f *fakeSlashClient) Get(path string) (*http.Response, error) {
	idx := f.getCalls
	f.getCalls++
	if idx < len(f.getErr) && f.getErr[idx] != nil {
		return nil, f.getErr[idx]
	}
	if idx < len(f.getResp) {
		return f.getResp[idx], nil
	}
	return mkResp(200, `[]`), nil
}

func (f *fakeSlashClient) Post(path string, body interface{}) (*http.Response, error) {
	idx := f.postCalls
	f.postCalls++
	if idx < len(f.postErr) && f.postErr[idx] != nil {
		return nil, f.postErr[idx]
	}
	if idx < len(f.postResp) {
		return f.postResp[idx], nil
	}
	return mkResp(200, `{}`), nil
}

func (f *fakeSlashClient) GetWorkspaceID() string { return f.wsID }

func mkResp(code int, body string) *http.Response {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Status:     fmt.Sprintf("%d", code),
	}
}

func newTestREPL() *REPL {
	return &REPL{
		Slash: map[string]REPLHandler{},
		Err:   io.Discard,
	}
}

// TestLoadServerSlashCommands_NilGuards: nil client or nil repl
// returns 0 without panicking. Defence at the boundary.
func TestLoadServerSlashCommands_NilGuards(t *testing.T) {
	if got := LoadServerSlashCommands(context.Background(), nil, &fakeSlashClient{wsID: "ws"}); got != 0 {
		t.Errorf("nil repl: got %d, want 0", got)
	}
	if got := LoadServerSlashCommands(context.Background(), newTestREPL(), nil); got != 0 {
		t.Errorf("nil client: got %d, want 0", got)
	}
}

// TestLoadServerSlashCommands_EmptyWorkspace: no workspace context
// means there's no catalog to fetch — early return 0, no GET.
func TestLoadServerSlashCommands_EmptyWorkspace(t *testing.T) {
	c := &fakeSlashClient{wsID: ""}
	got := LoadServerSlashCommands(context.Background(), newTestREPL(), c)
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if c.getCalls != 0 {
		t.Errorf("getCalls = %d, want 0 (early-return before HTTP)", c.getCalls)
	}
}

// TestLoadServerSlashCommands_GetError: transport failure logged but
// not fatal; returns 0 and no panic.
func TestLoadServerSlashCommands_GetError(t *testing.T) {
	c := &fakeSlashClient{
		wsID:   "ws",
		getErr: []error{errors.New("connection refused")},
	}
	repl := newTestREPL()
	errBuf := &bytes.Buffer{}
	repl.Err = errBuf
	got := LoadServerSlashCommands(context.Background(), repl, c)
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if !strings.Contains(errBuf.String(), "failed to fetch") {
		t.Errorf("expected 'failed to fetch' in stderr, got %q", errBuf.String())
	}
}

// TestLoadServerSlashCommands_Non200: any non-OK status surfaces an
// error log and returns 0. Body is included for operator debugging.
func TestLoadServerSlashCommands_Non200(t *testing.T) {
	c := &fakeSlashClient{
		wsID:    "ws",
		getResp: []*http.Response{mkResp(500, `{"error":"oops"}`)},
	}
	repl := newTestREPL()
	errBuf := &bytes.Buffer{}
	repl.Err = errBuf
	got := LoadServerSlashCommands(context.Background(), repl, c)
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if !strings.Contains(errBuf.String(), "500") {
		t.Errorf("expected 500 in stderr, got %q", errBuf.String())
	}
}

// TestLoadServerSlashCommands_MalformedJSON: decode failure is
// surfaced but not fatal.
func TestLoadServerSlashCommands_MalformedJSON(t *testing.T) {
	c := &fakeSlashClient{
		wsID:    "ws",
		getResp: []*http.Response{mkResp(200, `not json`)},
	}
	repl := newTestREPL()
	errBuf := &bytes.Buffer{}
	repl.Err = errBuf
	got := LoadServerSlashCommands(context.Background(), repl, c)
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	if !strings.Contains(errBuf.String(), "decode failed") {
		t.Errorf("expected 'decode failed' in stderr, got %q", errBuf.String())
	}
}

// TestLoadServerSlashCommands_RegistersAll: happy path — three
// catalog entries land as three slash handlers on the REPL.
func TestLoadServerSlashCommands_RegistersAll(t *testing.T) {
	catalog := `[
		{"id":"routine","label":"R","capability":"routine.create"},
		{"id":"issue","label":"I","capability":"issue.create"},
		{"id":"remember","label":"M","capability":"memory.write"}
	]`
	c := &fakeSlashClient{
		wsID:    "ws",
		getResp: []*http.Response{mkResp(200, catalog)},
	}
	repl := newTestREPL()
	got := LoadServerSlashCommands(context.Background(), repl, c)
	if got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	for _, id := range []string{"routine", "issue", "remember"} {
		if _, ok := repl.Slash[id]; !ok {
			t.Errorf("handler for %q not registered", id)
		}
	}
}

// TestBuildSlashHandler_RequiredFieldMissing: client-side validation
// catches missing required field before any HTTP call.
func TestBuildSlashHandler_RequiredFieldMissing(t *testing.T) {
	cmd := ServerSlashCommand{
		ID: "routine",
		FormSchema: []ServerSlashField{
			{Name: "name", Type: "text", Required: true},
		},
	}
	c := &fakeSlashClient{wsID: "ws"}
	h := buildSlashHandler(cmd, c, nil)
	_, err := h(context.Background(), []string{}) // no args
	if err == nil || !strings.Contains(err.Error(), "required field") {
		t.Errorf("expected 'required field' error, got %v", err)
	}
	if c.postCalls != 0 {
		t.Errorf("postCalls = %d, want 0 (client-side reject)", c.postCalls)
	}
}

// TestBuildSlashHandler_RequiredFieldDefault: a missing required
// field that has a Default value falls through using the default —
// no error, POST proceeds.
func TestBuildSlashHandler_RequiredFieldDefault(t *testing.T) {
	cmd := ServerSlashCommand{
		ID: "routine",
		FormSchema: []ServerSlashField{
			{Name: "timezone", Type: "timezone", Required: true, Default: "UTC"},
		},
	}
	c := &fakeSlashClient{wsID: "ws"}
	h := buildSlashHandler(cmd, c, nil)
	_, err := h(context.Background(), []string{})
	if err != nil {
		t.Errorf("default should satisfy required: %v", err)
	}
	if c.postCalls != 1 {
		t.Errorf("postCalls = %d, want 1", c.postCalls)
	}
}

// TestBuildSlashHandler_OptionalDefaultApplied: missing optional
// field with a Default value gets the default applied before POST.
func TestBuildSlashHandler_OptionalDefaultApplied(t *testing.T) {
	cmd := ServerSlashCommand{
		ID: "routine",
		FormSchema: []ServerSlashField{
			{Name: "timezone", Type: "timezone", Default: "UTC"},
		},
	}
	c := &fakeSlashClient{wsID: "ws"}
	h := buildSlashHandler(cmd, c, nil)
	if _, err := h(context.Background(), []string{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if c.postCalls != 1 {
		t.Errorf("postCalls = %d, want 1", c.postCalls)
	}
}

// TestBuildSlashHandler_UnknownEndpoint: a slash entry with an id
// that slashCommandEndpoint doesn't recognise surfaces the error
// without crashing.
func TestBuildSlashHandler_UnknownEndpoint(t *testing.T) {
	cmd := ServerSlashCommand{ID: "future-cmd"}
	c := &fakeSlashClient{wsID: "ws"}
	h := buildSlashHandler(cmd, c, nil)
	_, err := h(context.Background(), []string{})
	if err == nil || !strings.Contains(err.Error(), "unknown slash command") {
		t.Errorf("expected unknown-id error, got %v", err)
	}
}

// TestBuildSlashHandler_PostTransportError: HTTP POST failure is
// surfaced verbatim.
func TestBuildSlashHandler_PostTransportError(t *testing.T) {
	cmd := ServerSlashCommand{ID: "routine"}
	c := &fakeSlashClient{
		wsID:    "ws",
		postErr: []error{errors.New("conn reset")},
	}
	h := buildSlashHandler(cmd, c, nil)
	_, err := h(context.Background(), []string{})
	if err == nil || !strings.Contains(err.Error(), "conn reset") {
		t.Errorf("expected transport error, got %v", err)
	}
}

// TestBuildSlashHandler_PostNon2xx: server-side rejection (e.g. 403
// from capability re-check) wraps the response body in the error.
func TestBuildSlashHandler_PostNon2xx(t *testing.T) {
	cmd := ServerSlashCommand{ID: "routine"}
	c := &fakeSlashClient{
		wsID:     "ws",
		postResp: []*http.Response{mkResp(403, `{"error":"Forbidden"}`)},
	}
	h := buildSlashHandler(cmd, c, nil)
	_, err := h(context.Background(), []string{})
	if err == nil || !strings.Contains(err.Error(), "/routine failed") {
		t.Errorf("expected /routine failed wrapper, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("expected body included in error, got %v", err)
	}
}

// TestBuildSlashHandler_HappyPath: 2xx response → no error, POST
// fired exactly once.
func TestBuildSlashHandler_HappyPath(t *testing.T) {
	cmd := ServerSlashCommand{ID: "routine"}
	c := &fakeSlashClient{
		wsID:     "ws",
		postResp: []*http.Response{mkResp(201, `{"id":"sched_1"}`)},
	}
	h := buildSlashHandler(cmd, c, nil)
	cont, err := h(context.Background(), []string{})
	if err != nil {
		t.Errorf("unexpected err: %v", err)
	}
	if !cont {
		t.Error("expected REPL to continue after slash command")
	}
	if c.postCalls != 1 {
		t.Errorf("postCalls = %d, want 1", c.postCalls)
	}
}

// TestBuildSlashHandler_WritesSuccessToOut is the CodeRabbit CR-7
// regression: success confirmation must go via the passed-in writer
// (repl.Out) instead of being hardcoded to os.Stdout. We pass a
// bytes.Buffer and assert it captures the confirmation line.
func TestBuildSlashHandler_WritesSuccessToOut(t *testing.T) {
	cmd := ServerSlashCommand{ID: "routine"}
	c := &fakeSlashClient{
		wsID:     "ws",
		postResp: []*http.Response{mkResp(201, `{}`)},
	}
	out := &bytes.Buffer{}
	h := buildSlashHandler(cmd, c, out)
	if _, err := h(context.Background(), []string{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(out.String(), "[/routine] ✓") {
		t.Errorf("success confirmation missing from out buffer; got %q", out.String())
	}
}

// TestLoadServerSlashCommands_RegistersHandlerWithReplOut wires the
// full LoadServerSlashCommands path against a REPL whose Out is a
// captured buffer; asserts the resulting handler writes its success
// line through that buffer (end-to-end CR-7 coverage).
func TestLoadServerSlashCommands_RegistersHandlerWithReplOut(t *testing.T) {
	catalog := `[{"id":"routine","label":"R","capability":"routine.create"}]`
	c := &fakeSlashClient{
		wsID:     "ws",
		getResp:  []*http.Response{mkResp(200, catalog)},
		postResp: []*http.Response{mkResp(201, `{}`)},
	}
	repl := newTestREPL()
	out := &bytes.Buffer{}
	repl.Out = out
	got := LoadServerSlashCommands(context.Background(), repl, c)
	if got != 1 {
		t.Fatalf("registered %d, want 1", got)
	}
	handler, ok := repl.Slash["routine"]
	if !ok {
		t.Fatal("routine handler not registered")
	}
	if _, err := handler(context.Background(), []string{}); err != nil {
		t.Fatalf("handler err: %v", err)
	}
	if !strings.Contains(out.String(), "[/routine] ✓") {
		t.Errorf("repl.Out did not capture success line: %q", out.String())
	}
}

// TestSlashCommandPayload_AllIDs: every catalog id has its own
// reshape branch; cover the three that the per-id smoke test didn't
// hit (credential, issue, remember).
func TestSlashCommandPayload_AllIDs(t *testing.T) {
	t.Run("credential", func(t *testing.T) {
		got := slashCommandPayload("credential", map[string]string{
			"name": "GH_PAT", "type": "SECRET", "value": "ghp_x",
		}).(map[string]any)
		if got["name"] != "GH_PAT" || got["type"] != "SECRET" || got["value"] != "ghp_x" {
			t.Errorf("credential reshape: %v", got)
		}
	})
	t.Run("issue", func(t *testing.T) {
		got := slashCommandPayload("issue", map[string]string{
			"title": "Bug", "description": "It broke", "priority": "high",
		}).(map[string]any)
		if got["title"] != "Bug" || got["priority"] != "high" {
			t.Errorf("issue reshape: %v", got)
		}
	})
	t.Run("remember", func(t *testing.T) {
		got := slashCommandPayload("remember", map[string]string{
			"content": "X is Y", "scope": "crew",
		}).(map[string]any)
		if got["content"] != "X is Y" || got["scope"] != "crew" {
			t.Errorf("remember reshape: %v", got)
		}
	})
}
