package kinds

// Coverage-focused tests for hook.go: the ListHooks shape tolerance,
// expectHookSuccess branches, hookDescription matrix, and the nil
// tolerance of the snippet/readAll helpers.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

func TestHookCov_ListHooks(t *testing.T) {
	t.Parallel()
	path := "/api/v1/hooks"

	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {err: errors.New("down")}})
		if _, err := ListHooks(context.Background(), c); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("nil response", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {nilResp: true}})
		if _, err := ListHooks(context.Background(), c); err == nil || !strings.Contains(err.Error(), "nil response") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("non-2xx with body", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "blew up"}})
		_, err := ListHooks(context.Background(), c)
		if err == nil || !strings.Contains(err.Error(), "HTTP 500") || !strings.Contains(err.Error(), "blew up") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad body read", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {badBody: true}})
		if _, err := ListHooks(context.Background(), c); err == nil || !strings.Contains(err.Error(), "read /api/v1/hooks body") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty body → nil", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: ""}})
		out, err := ListHooks(context.Background(), c)
		if err != nil || out != nil {
			t.Fatalf("out=%v err=%v", out, err)
		}
	})
	t.Run("wrapped envelope", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"rows":[{"id":"h1","event":"pre_run","handler_kind":"shell","enabled":true}],"count":1}`}})
		out, err := ListHooks(context.Background(), c)
		if err != nil || len(out) != 1 {
			t.Fatalf("out=%v err=%v", out, err)
		}
		if out[0].ID != "h1" || out[0].Slug != "h1" || !out[0].Enabled {
			t.Errorf("row = %+v", out[0])
		}
		if out[0].Description != "pre_run shell hook" {
			t.Errorf("description = %q", out[0].Description)
		}
	})
	t.Run("flat array", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `[{"id":"h1","event":"post_run","handler_kind":"http","enabled":false}]`}})
		out, err := ListHooks(context.Background(), c)
		if err != nil || len(out) != 1 || out[0].Description != "post_run http hook" {
			t.Fatalf("out=%+v err=%v", out, err)
		}
	})
	t.Run("unrecognized shape", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"GET " + path: {body: `{"count":3}`}})
		if _, err := ListHooks(context.Background(), c); err == nil || !strings.Contains(err.Error(), "unrecognized response shape") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestHookCov_FindHookBySlug(t *testing.T) {
	t.Parallel()
	path := "/api/v1/hooks"
	body := `{"rows":[{"id":"h1","event":"pre_run","handler_kind":"shell","enabled":true}],"count":1}`

	c := newCovClient(map[string]covRoute{"GET " + path: {body: body}})
	got, err := FindHookBySlug(context.Background(), c, "h1")
	if err != nil || got == nil || got.ID != "h1" {
		t.Fatalf("found: got=%+v err=%v", got, err)
	}
	got, err = FindHookBySlug(context.Background(), c, "ghost")
	if err != nil || got != nil {
		t.Fatalf("missing: got=%+v err=%v", got, err)
	}
	c = newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
	if _, err := FindHookBySlug(context.Background(), c, "h1"); err == nil {
		t.Fatal("list error: want error")
	}
}

func TestHookCov_ExportHooks(t *testing.T) {
	t.Parallel()
	path := "/api/v1/hooks"

	c := newCovClient(map[string]covRoute{"GET " + path: {status: 500, body: "x"}})
	if _, err := ExportHooks(context.Background(), c); err == nil {
		t.Fatal("list error: want error")
	}

	c = newCovClient(map[string]covRoute{"GET " + path: {body: `{"rows":[{"id":"h1","event":"pre_run","handler_kind":"shell","enabled":true}],"count":1}`}})
	docs, err := ExportHooks(context.Background(), c)
	if err != nil || len(docs) != 1 {
		t.Fatalf("docs=%v err=%v", docs, err)
	}
	d := docs[0]
	if d.Metadata.Slug != "h1" || d.Metadata.Name != "h1" || !d.Spec.Enabled {
		t.Errorf("doc = %+v", d)
	}
	if d.Metadata.Description != "pre_run shell hook" {
		t.Errorf("description = %q", d.Metadata.Description)
	}
}

func TestHookCov_ExpectHookSuccess(t *testing.T) {
	t.Parallel()
	if err := expectHookSuccess(nil, "op"); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Errorf("nil: %v", err)
	}
	if err := expectHookSuccess(&internalapi.Response{StatusCode: 200}, "op"); err != nil {
		t.Errorf("200: %v", err)
	}
	err := expectHookSuccess(&internalapi.Response{StatusCode: 500}, "op")
	if err == nil || !strings.Contains(err.Error(), "op: HTTP 500") || strings.Contains(err.Error(), "500:") {
		t.Errorf("500 no body: %v", err)
	}
	err = expectHookSuccess(&internalapi.Response{StatusCode: 403, Body: strings.NewReader("denied")}, "op")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403: denied") {
		t.Errorf("403 with body: %v", err)
	}
}

func TestHookCov_HookDescription(t *testing.T) {
	t.Parallel()
	cases := []struct {
		event, kind, want string
	}{
		{"pre_run", "shell", "pre_run shell hook"},
		{"pre_run", "", "pre_run hook"},
		{"", "shell", "shell hook"},
		{"", "", ""},
	}
	for _, tc := range cases {
		got := hookDescription(hookListRow{Event: tc.event, HandlerKind: tc.kind})
		if got != tc.want {
			t.Errorf("hookDescription(%q,%q) = %q, want %q", tc.event, tc.kind, got, tc.want)
		}
	}
}

func TestHookCov_ReadHelpers(t *testing.T) {
	t.Parallel()
	if got := readSnippet(nil); got != "" {
		t.Errorf("readSnippet(nil) = %q", got)
	}
	if got := readSnippet(strings.NewReader(" body \n")); got != "body" {
		t.Errorf("readSnippet = %q", got)
	}
	if b, err := readAllHook(nil); b != nil || err != nil {
		t.Errorf("readAllHook(nil) = %v, %v", b, err)
	}
}

func TestHookCov_UnchangedDescription(t *testing.T) {
	t.Parallel()
	d := &HookDocument{
		APIVersion: hookAPIVersion, Kind: hookKind,
		Metadata: internalapi.Metadata{Name: "h1", Slug: "h1"},
		Spec:     HookSpec{Enabled: false},
	}
	got := hookUnchangedDescription(d, &HookRemote{ID: "h1", Slug: "h1", Enabled: false})
	if got != `hook "h1" already disabled` {
		t.Errorf("no description: %q", got)
	}
	got = hookUnchangedDescription(d, &HookRemote{ID: "h1", Slug: "h1", Enabled: false, Description: "pre_run shell hook"})
	if got != `hook "h1" already disabled (pre_run shell hook)` {
		t.Errorf("with description: %q", got)
	}
}

// Plan toggle Exec against an erroring server: the enable POST surfaces
// the HTTP failure through expectHookSuccess.
func TestHookCov_Plan_ToggleExecError(t *testing.T) {
	t.Parallel()
	d := &HookDocument{
		APIVersion: hookAPIVersion, Kind: hookKind,
		Metadata: internalapi.Metadata{Name: "h1", Slug: "h1"},
		Spec:     HookSpec{Enabled: true},
	}
	remote := &HookRemote{ID: "h1", Slug: "h1", Enabled: false, Description: "pre_run shell hook"}

	items, err := d.Plan(context.Background(), nil, remote)
	if err != nil || len(items) != 1 || items[0].Action != internalapi.ActionUpdate {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if !strings.Contains(items[0].Description, "enable") {
		t.Errorf("description = %q", items[0].Description)
	}

	c := newCovClient(map[string]covRoute{
		"POST /api/v1/hooks/h1/enable": {status: 500, body: "broken"},
	})
	if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("got %v", err)
	}

	c = newCovClient(map[string]covRoute{
		"POST /api/v1/hooks/h1/enable": {err: errors.New("down")},
	})
	if err := items[0].Exec(context.Background(), c); err == nil || !strings.Contains(err.Error(), "down") {
		t.Fatalf("got %v", err)
	}
}
