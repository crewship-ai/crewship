package pipeline

import (
	"reflect"
	"strings"
	"testing"
)

// DescribeFiles is the pure projection behind the routine detail's `files`
// member: declared script paths (and file-looking args/env values under the
// crew share) become FileRef rows without any I/O. Order is the contract —
// script.path entries first in recipe order, then args/env discoveries.
func TestDescribeFiles(t *testing.T) {
	tests := []struct {
		name string
		def  string
		want []FileRef
	}{
		{
			name: "no steps",
			def:  `{"name":"empty","steps":[]}`,
			want: []FileRef{},
		},
		{
			name: "non-script steps declare nothing",
			def:  `{"name":"x","steps":[{"id":"a","type":"transform","transform":{"input":"{}","expression":"."}}]}`,
			want: []FileRef{},
		},
		{
			name: "script path with inferred interpreter and language",
			def: `{"name":"x","steps":[
				{"id":"post","type":"script","script":{"path":"scripts/ledger-post.go"}}]}`,
			want: []FileRef{{Path: "scripts/ledger-post.go", Language: "go", Interpreter: "go run", StepIDs: []string{"post"}}},
		},
		{
			name: "explicit interpreter wins and unknown extension has no language",
			def: `{"name":"x","steps":[
				{"id":"a","type":"script","script":{"path":"tools/run.rb","interpreter":"ruby3"}}]}`,
			want: []FileRef{{Path: "tools/run.rb", Language: "", Interpreter: "ruby3", StepIDs: []string{"a"}}},
		},
		{
			name: "same file in two steps is one row with both step ids",
			def: `{"name":"x","steps":[
				{"id":"a","type":"script","script":{"path":"scripts/x.py"}},
				{"id":"b","type":"script","script":{"path":"/crew/shared/scripts/x.py"}}]}`,
			want: []FileRef{{Path: "scripts/x.py", Language: "py", Interpreter: "python3", StepIDs: []string{"a", "b"}}},
		},
		{
			name: "foreach body, step hooks and routine hooks are walked in order",
			def: `{"name":"x",
				"hooks":{"before_all":{"id":"setup","type":"script","script":{"path":"scripts/setup.sh"}},
				         "on_failure":{"id":"alert","type":"script","script":{"path":"scripts/alert.sh"}}},
				"steps":[
				{"id":"loop","type":"foreach","foreach":{"items":"[]","steps":[
					{"id":"item","type":"script","script":{"path":"scripts/item.py"}}]}},
				{"id":"main","type":"transform","transform":{"input":"{}","expression":"."},
				 "hooks":{"before":{"id":"pre","type":"script","script":{"path":"scripts/pre.sh"}},
				          "after":{"id":"after","type":"script","script":{"path":"scripts/after.sh"}}}}]}`,
			want: []FileRef{
				{Path: "scripts/setup.sh", Language: "sh", Interpreter: "bash", StepIDs: []string{"setup"}},
				{Path: "scripts/item.py", Language: "py", Interpreter: "python3", StepIDs: []string{"loop/item"}},
				{Path: "scripts/pre.sh", Language: "sh", Interpreter: "bash", StepIDs: []string{"pre"}},
				{Path: "scripts/after.sh", Language: "sh", Interpreter: "bash", StepIDs: []string{"after"}},
				{Path: "scripts/alert.sh", Language: "sh", Interpreter: "bash", StepIDs: []string{"alert"}},
			},
		},
		{
			name: "args and env file paths under the share come after script paths",
			def: `{"name":"x","steps":[
				{"id":"a","type":"script","script":{"path":"scripts/a.py",
					"args":["--rules","/crew/shared/config/rules.yaml","/crew/shared/data","--n","3"],
					"env":{"MAPPING":"/crew/shared/config/map.json","HOME":"/home/agent"}}},
				{"id":"b","type":"script","script":{"path":"scripts/b.py"}}]}`,
			want: []FileRef{
				{Path: "scripts/a.py", Language: "py", Interpreter: "python3", StepIDs: []string{"a"}},
				{Path: "scripts/b.py", Language: "py", Interpreter: "python3", StepIDs: []string{"b"}},
				{Path: "config/rules.yaml", Language: "yaml", Interpreter: "", StepIDs: []string{"a"}},
				{Path: "config/map.json", Language: "json", Interpreter: "", StepIDs: []string{"a"}},
			},
		},
		{
			name: "paths that escape the share are skipped",
			def: `{"name":"x","steps":[
				{"id":"a","type":"script","script":{"path":"../../etc/passwd"}},
				{"id":"b","type":"script","script":{"path":"/etc/passwd"}},
				{"id":"c","type":"script","script":{"path":"  "}}]}`,
			want: []FileRef{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dsl, err := Parse([]byte(tc.def))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got := DescribeFiles(dsl)
			if got == nil {
				t.Fatal("DescribeFiles returned nil; the API serialises this as `files: []`")
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("DescribeFiles mismatch\n got: %#v\nwant: %#v", got, tc.want)
			}
		})
	}
	if got := DescribeFiles(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil dsl: got %#v, want empty slice", got)
	}
}

// FileDescription reads the header comment of a script so the Files card
// can say what a file does without an LLM in the loop.
func TestFileDescription(t *testing.T) {
	long := strings.Repeat("word ", 60) // 300 chars
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"empty", "", ""},
		{"no comment", "package main\n\nfunc main() {}\n", ""},
		{"go line comments", "// Posts one invoice to the ERP ledger.\n// Retries once on 5xx.\npackage main\n", "Posts one invoice to the ERP ledger. Retries once on 5xx."},
		{"shebang then hash comments", "#!/usr/bin/env python3\n# Parse the bank statement PDF.\n#   Emits JSON lines.\nimport sys\n", "Parse the bank statement PDF. Emits JSON lines."},
		{"decoration lines are ignored", "# ======\n# Nightly rollup\n# ======\nset -e\n", "Nightly rollup"},
		{"block comment", "/*\n * Ledger post.\n * Second line.\n */\npackage x\n", "Ledger post. Second line."},
		{"single line block comment", "/* one-liner */\ncode", "one-liner"},
		{"html comment", "<!-- Release notes template -->\n# Title\n", "Release notes template"},
		{"sql dash comments", "-- Sum invoices per customer\nSELECT 1;\n", "Sum invoices per customer"},
		{"blank line ends the header", "# First paragraph.\n\n# Second paragraph.\n", "First paragraph."},
		{"leading blank lines are skipped", "\n\n// Late header\n", "Late header"},
		{"capped at 200 characters", "# " + long + "\n", strings.TrimSpace(long)[:200]},
		{"code before comment is not a header", "x = 1\n# not a header\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := FileDescription([]byte(tc.content)); got != tc.want {
				t.Fatalf("FileDescription(%q) = %q, want %q", tc.content, got, tc.want)
			}
		})
	}
}
