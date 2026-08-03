package memport

import (
	"testing"
	"testing/fstest"
)

// A source tree is identified by the files that only that harness
// writes, never by a file every markdown corpus has. "there is a
// CLAUDE.md somewhere" describes half the repos on disk; "there is a
// groups/ directory whose children hold CLAUDE.md" describes NanoClaw.
func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		fsys fstest.MapFS
		want Format
	}{
		{
			name: "crewship: canonical tier files at the root of a .memory dir",
			fsys: fstest.MapFS{
				"AGENT.md":            &fstest.MapFile{Data: []byte("# agent")},
				"pins.md":             &fstest.MapFile{Data: []byte("# pins")},
				"daily/2026-08-01.md": &fstest.MapFile{Data: []byte("today")},
			},
			want: FormatCrewship,
		},
		{
			name: "okf: markdown carrying YAML frontmatter",
			fsys: fstest.MapFS{
				"concepts/orders.md": &fstest.MapFile{
					Data: []byte("---\ntype: table\ntitle: Orders\n---\n\nbody\n"),
				},
			},
			want: FormatOKF,
		},
		{
			name: "nanoclaw: groups/<name>/CLAUDE.md",
			fsys: fstest.MapFS{
				"groups/global/CLAUDE.md":            &fstest.MapFile{Data: []byte("shared")},
				"groups/telegram_dev-team/CLAUDE.md": &fstest.MapFile{Data: []byte("group")},
			},
			want: FormatNanoClaw,
		},
		{
			name: "openclaw: SOUL.md + memory/ dated notes",
			fsys: fstest.MapFS{
				"SOUL.md":                &fstest.MapFile{Data: []byte("persona")},
				"MEMORY.md":              &fstest.MapFile{Data: []byte("long term")},
				"memory/2026-02-13.md":   &fstest.MapFile{Data: []byte("daily")},
				"memory/projects.md":     &fstest.MapFile{Data: []byte("topic")},
				"sessions/abc/state.jsn": &fstest.MapFile{Data: []byte("{}")},
			},
			want: FormatOpenClaw,
		},
		{
			name: "crewship wins over okf when its own tier files carry frontmatter",
			fsys: fstest.MapFS{
				"AGENT.md": &fstest.MapFile{Data: []byte("---\ntype: agent\n---\nbody")},
				"CREW.md":  &fstest.MapFile{Data: []byte("---\ntype: crew\n---\nbody")},
			},
			want: FormatCrewship,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Detect(tt.fsys)
			if err != nil {
				t.Fatalf("Detect() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Detect() = %q, want %q", got, tt.want)
			}
		})
	}
}

// An unrecognised tree must fail loudly. Guessing a format and then
// writing into somebody's agent memory is the one outcome worse than
// refusing the import.
func TestDetectUnknown(t *testing.T) {
	fsys := fstest.MapFS{
		"README.md":   &fstest.MapFile{Data: []byte("# a repo")},
		"src/main.go": &fstest.MapFile{Data: []byte("package main")},
	}
	if _, err := Detect(fsys); err == nil {
		t.Fatal("Detect() on an unrecognised tree returned nil error; want a refusal")
	}
}
