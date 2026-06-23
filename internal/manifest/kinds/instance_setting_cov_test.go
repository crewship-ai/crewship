package kinds

// Coverage-focused tests for instance_setting.go. Targets the HTTP
// helpers (fetch / put / delete), the response-shape tolerance of
// fetchInstanceSettings, and the resolveEnv malformed-placeholder
// branches. Uses the shared covClient from routine_cov_test.go.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

const instanceSettingsPath = "/api/v1/instance/settings"

func TestInstanceSettingCov_ReadErrBodyAndFirstBytes(t *testing.T) {
	t.Parallel()
	if got := readErrBody(nil); got != "" {
		t.Errorf("readErrBody(nil) = %q, want empty", got)
	}
	if got := readErrBody(strings.NewReader("  oops \n")); got != "oops" {
		t.Errorf("readErrBody = %q, want trimmed 'oops'", got)
	}

	if got := firstBytes([]byte("short"), 80); got != "short" {
		t.Errorf("firstBytes short = %q", got)
	}
	long := strings.Repeat("x", 100)
	got := firstBytes([]byte(long), 10)
	if got != strings.Repeat("x", 10)+"…" {
		t.Errorf("firstBytes long = %q", got)
	}
}

func TestInstanceSettingCov_FetchInstanceSettings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		route       covRoute
		wantErr     string
		wantEmpty   bool
		wantKey     string
		wantVal     string
		isTransport bool
	}{
		{name: "transport error", route: covRoute{err: errors.New("down")}, isTransport: true},
		{name: "nil response → empty", route: covRoute{nilResp: true}, wantEmpty: true},
		{name: "404 → empty", route: covRoute{status: 404, body: "nope"}, wantEmpty: true},
		{name: "500 with body", route: covRoute{status: 500, body: "broken"}, wantErr: "status 500: broken"},
		{name: "body read failure", route: covRoute{badBody: true}, wantErr: "read body"},
		{name: "empty body → empty", route: covRoute{body: ""}, wantEmpty: true},
		{name: "flat map", route: covRoute{body: `{"smtp.host":"mail.example.com"}`}, wantKey: "smtp.host", wantVal: "mail.example.com"},
		{name: "wrapped map", route: covRoute{body: `{"settings":{"smtp.host":"mail.example.com"}}`}, wantKey: "smtp.host", wantVal: "mail.example.com"},
		{name: "rows array", route: covRoute{body: `[{"key":"smtp.host","value":"mail.example.com"}]`}, wantKey: "smtp.host", wantVal: "mail.example.com"},
		{name: "unknown shape", route: covRoute{body: `42`}, wantErr: "unknown response shape"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := newCovClient(map[string]covRoute{"GET " + instanceSettingsPath: tc.route})
			out, err := fetchInstanceSettings(context.Background(), c)
			switch {
			case tc.isTransport:
				if err == nil {
					t.Fatal("want transport error")
				}
			case tc.wantErr != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, tc.wantErr)
				}
			case tc.wantEmpty:
				if err != nil || len(out) != 0 {
					t.Fatalf("out=%v err=%v, want empty/nil", out, err)
				}
			default:
				if err != nil || out[tc.wantKey] != tc.wantVal {
					t.Fatalf("out=%v err=%v, want %s=%s", out, err, tc.wantKey, tc.wantVal)
				}
			}
		})
	}
}

func TestInstanceSettingCov_PutInstanceSetting(t *testing.T) {
	t.Parallel()
	key := instanceSettingsPath + "/smtp.host"

	t.Run("success", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"PUT " + key: {status: 200, body: `{}`}})
		if err := putInstanceSetting(context.Background(), c, "smtp.host", "x"); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil response tolerated", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"PUT " + key: {nilResp: true}})
		if err := putInstanceSetting(context.Background(), c, "smtp.host", "x"); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"PUT " + key: {status: 422, body: "bad value"}})
		err := putInstanceSetting(context.Background(), c, "smtp.host", "x")
		if err == nil || !strings.Contains(err.Error(), "status 422") || !strings.Contains(err.Error(), "bad value") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"PUT " + key: {err: errors.New("down")}})
		err := putInstanceSetting(context.Background(), c, "smtp.host", "x")
		if err == nil || !strings.Contains(err.Error(), "down") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestInstanceSettingCov_DeleteInstanceSetting(t *testing.T) {
	t.Parallel()
	key := instanceSettingsPath + "/old.key"

	t.Run("success", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"DELETE " + key: {status: 204}})
		if err := deleteInstanceSetting(context.Background(), c, "old.key"); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("404 is a no-op", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"DELETE " + key: {status: 404, body: "gone already"}})
		if err := deleteInstanceSetting(context.Background(), c, "old.key"); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil response tolerated", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"DELETE " + key: {nilResp: true}})
		if err := deleteInstanceSetting(context.Background(), c, "old.key"); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("non-2xx", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"DELETE " + key: {status: 500, body: "locked"}})
		err := deleteInstanceSetting(context.Background(), c, "old.key")
		if err == nil || !strings.Contains(err.Error(), "status 500") || !strings.Contains(err.Error(), "locked") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("transport error", func(t *testing.T) {
		c := newCovClient(map[string]covRoute{"DELETE " + key: {err: errors.New("down")}})
		err := deleteInstanceSetting(context.Background(), c, "old.key")
		if err == nil || !strings.Contains(err.Error(), "down") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestInstanceSettingCov_ValidateStructural(t *testing.T) {
	t.Parallel()

	t.Run("bad apiVersion", func(t *testing.T) {
		d := InstanceSettingDocument{APIVersion: "crewship/v2"}
		if err := d.Validate(internalapi.WorkspaceContext{}); err == nil || !strings.Contains(err.Error(), "unsupported apiVersion") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("bad kind", func(t *testing.T) {
		d := InstanceSettingDocument{Kind: "Project"}
		if err := d.Validate(internalapi.WorkspaceContext{}); err == nil || !strings.Contains(err.Error(), "unexpected kind") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("nil settings is legal", func(t *testing.T) {
		d := InstanceSettingDocument{APIVersion: "crewship/v1", Kind: "InstanceSetting"}
		if err := d.Validate(internalapi.WorkspaceContext{}); err != nil {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("empty key rejected", func(t *testing.T) {
		d := InstanceSettingDocument{
			Spec: InstanceSettingSpec{Settings: map[string]string{"  ": "v"}},
		}
		if err := d.Validate(internalapi.WorkspaceContext{}); err == nil || !strings.Contains(err.Error(), "empty key") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestInstanceSettingCov_ResolveEnvMalformed(t *testing.T) {
	t.Parallel()
	lookup := func(name string) (string, bool) {
		if name == "GOOD" {
			return "resolved", true
		}
		return "", false
	}

	t.Run("no placeholders pass through", func(t *testing.T) {
		out, err := resolveEnv("plain value", nil)
		if err != nil || out != "plain value" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
	t.Run("default with shell syntax rejected", func(t *testing.T) {
		_, err := resolveEnv("${X:-fallback}", lookup)
		if err == nil || !strings.Contains(err.Error(), "malformed placeholder") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unbalanced opener rejected with long snippet", func(t *testing.T) {
		_, err := resolveEnv("${UNCLOSED"+strings.Repeat("y", 64), lookup)
		if err == nil || !strings.Contains(err.Error(), "malformed placeholder") || !strings.Contains(err.Error(), "…") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("missing variable rejected", func(t *testing.T) {
		_, err := resolveEnv("${MISSING_VAR_COV}", lookup)
		if err == nil || !strings.Contains(err.Error(), `"MISSING_VAR_COV"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("good variable resolved", func(t *testing.T) {
		out, err := resolveEnv("prefix-${GOOD}", lookup)
		if err != nil || out != "prefix-resolved" {
			t.Fatalf("out=%q err=%v", out, err)
		}
	})
}

func TestInstanceSettingCov_LooksSensitive(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"smtp.password":              true, // prefix
		"oauth.github.client_secret": true, // prefix + suffix
		"some.api_key":               true, // suffix
		"some.token":                 true, // suffix
		"smtp.host":                  false,
	}
	for key, want := range cases {
		if got := looksSensitive(key); got != want {
			t.Errorf("looksSensitive(%q) = %v, want %v", key, got, want)
		}
	}
}

// Plan in Replace mode against a remote with a deletable + protected
// key; exercises the delete Exec end-to-end (URL-escaped key).
func TestInstanceSettingCov_PlanReplaceDeleteExec(t *testing.T) {
	t.Parallel()
	d := InstanceSettingDocument{
		Spec: InstanceSettingSpec{Settings: map[string]string{}},
	}
	remote := InstanceSettingRemote{
		"old.key":               "stale",
		"instance.bootstrap_at": "2026-01-01", // protected
	}
	c := newCovClient(map[string]covRoute{
		"DELETE " + instanceSettingsPath + "/old.key": {status: 204},
	})

	items, err := d.Plan(context.Background(), c, &remote, PlanInstanceSettingsOptions{Replace: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var sawProtected, sawDelete bool
	for _, it := range items {
		if it.Slug == "instance.bootstrap_at" {
			sawProtected = true
			if it.Exec != nil {
				t.Error("protected key must not carry an Exec")
			}
		}
		if it.Slug == "old.key" {
			sawDelete = true
			if err := it.Exec(context.Background(), c); err != nil {
				t.Errorf("delete Exec: %v", err)
			}
		}
	}
	if !sawProtected || !sawDelete {
		t.Fatalf("items = %+v, want protected skip + delete", items)
	}
	if !c.sawCall("DELETE " + instanceSettingsPath + "/old.key") {
		t.Errorf("expected DELETE call, got %v", c.calls)
	}
}

func TestInstanceSettingCov_ExportFetchError(t *testing.T) {
	t.Parallel()
	c := newCovClient(map[string]covRoute{
		"GET " + instanceSettingsPath: {status: 500, body: "boom"},
	})
	_, err := ExportInstanceSettings(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "export fetch") {
		t.Fatalf("got %v", err)
	}
}
