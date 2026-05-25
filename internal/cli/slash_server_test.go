package cli

import (
	"reflect"
	"testing"
)

// TestParseKeyValueArgs covers the wire shape the REPL feeds in.
// The repl.dispatchSlash splits on whitespace, so a quoted value
// arrives as multiple tokens; the parser re-joins and runs the
// regex to reconstruct.
func TestParseKeyValueArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "single bare pair",
			args: []string{"slug=daily-digest"},
			want: map[string]string{"slug": "daily-digest"},
		},
		{
			name: "multiple bare pairs",
			args: []string{"slug=x", "type=SECRET"},
			want: map[string]string{"slug": "x", "type": "SECRET"},
		},
		{
			name: "quoted value with spaces",
			// REPL split: ["name=\"Weekly", "digest\"", "cron=0", "7", "*", "*", "MON"]
			// — but `cron=0 7 * * MON` isn't a quoted form so it
			// only picks up `cron=0` and drops `7 * * MON`. That's
			// expected: cron values need quotes too. We test the
			// quoted form here.
			args: []string{"name=\"Weekly", "digest\""},
			want: map[string]string{"name": "Weekly digest"},
		},
		{
			name: "multiple quoted values",
			args: []string{"name=\"A", "B\"", "cron=\"0", "7", "*", "*", "MON\""},
			want: map[string]string{"name": "A B", "cron": "0 7 * * MON"},
		},
		{
			name: "empty",
			args: []string{},
			want: map[string]string{},
		},
		{
			name:    "garbage with no kv pairs",
			args:    []string{"hello", "world"},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseKeyValueArgs(tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err == nil && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSlashCommandEndpoint asserts the id → endpoint mapping is
// stable. A typo here would land a slash POST on the wrong handler
// (or a 404), so explicit per-id coverage is worth the test surface.
func TestSlashCommandEndpoint(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"routine", "/api/v1/workspaces/ws-1/pipeline-schedules"},
		{"skill", "/api/v1/workspaces/ws-1/skills/generate"},
		{"credential", "/api/v1/credentials?workspace_id=ws-1"},
		{"issue", "/api/v1/issues?workspace_id=ws-1"},
		{"remember", "/api/v1/memory/write?workspace_id=ws-1"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got, err := slashCommandEndpoint(tc.id, "ws-1")
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
	t.Run("unknown id errors", func(t *testing.T) {
		_, err := slashCommandEndpoint("nonsense", "ws-1")
		if err == nil {
			t.Error("want error for unknown id")
		}
	})
}

// TestSlashCommandPayload covers the per-id body shaping. The
// reshape is small but per-command, so a bug here would silently
// submit the wrong field names to the backend.
func TestSlashCommandPayload(t *testing.T) {
	t.Run("routine", func(t *testing.T) {
		got := slashCommandPayload("routine", map[string]string{
			"name": "Weekly", "cron": "0 7 * * MON", "timezone": "UTC",
		}).(map[string]any)
		if got["cron_expr"] != "0 7 * * MON" {
			t.Errorf("cron→cron_expr mapping broken: %v", got)
		}
	})
	t.Run("skill", func(t *testing.T) {
		got := slashCommandPayload("skill", map[string]string{
			"slug": "x", "prompt": "Use when …",
		}).(map[string]any)
		if got["slug"] != "x" || got["prompt"] != "Use when …" {
			t.Errorf("skill mapping broken: %v", got)
		}
	})
	t.Run("unknown id passes raw values", func(t *testing.T) {
		got := slashCommandPayload("future-command", map[string]string{
			"x": "1", "y": "2",
		}).(map[string]any)
		if got["x"] != "1" || got["y"] != "2" {
			t.Errorf("fall-through mapping dropped fields: %v", got)
		}
	})
}
