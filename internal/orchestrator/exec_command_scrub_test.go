package orchestrator

// Guards for the exec.command journal emit: the argv must go through the
// run's credential scrubber BEFORE it is written, for exactly the reason
// exec_stream.go states over its sibling exec.output_chunk emit — the journal
// is hash-chained and append-only, so whatever lands in the payload can never
// be redacted afterwards (GDPR erasure explicitly skips the journal, see
// internal/api/admin_gdpr.go).
//
// Two guards, deliberately:
//
//   - the behavioural test drives a real run through RunAgent and asserts the
//     emitted payloads, which is what proves the fix works end to end;
//   - the static guard reads the source of every exec.command emit site, which
//     is what stops a FOURTH site from being added unscrubbed. Nothing enforced
//     scrub-at-emit before, which is how these three sites drifted from a
//     sibling 180 lines away; a behavioural test only covers the paths it
//     happens to drive.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider"
)

// execCmdPastedSecret stands in for the threat the issue names: a human pastes
// a token into agent chat, BuildCLICommand puts the verbatim user message in
// argv after `--`, and the argv is journalled. The value matches none of the
// scrubber's built-in patterns on purpose — only the per-run registration of
// the credential's plain value can catch it, which is the mechanism under test.
const execCmdPastedSecret = "crewship-guard-pasted-token-9f3a2b7c1d"

// execCmdBuiltinSecret is shaped like a real Anthropic key, so it is caught by
// the scrubber's built-in patterns with no per-run registration at all.
const execCmdBuiltinSecret = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"

// execCmdEntryArgv pulls payload["cmd"] out of a journal entry as a slice.
// The payload must carry the argv as a []string — flattening it to a joined
// string would hide a secret that spans an element boundary from the caller's
// element-wise assertions.
func execCmdEntryArgv(t *testing.T, e JournalEntry) []string {
	t.Helper()
	raw, ok := e.Payload["cmd"]
	if !ok {
		t.Fatalf("exec.command payload has no cmd field: %v", e.Payload)
	}
	argv, ok := raw.([]string)
	if !ok {
		t.Fatalf("exec.command payload cmd is %T, want []string", raw)
	}
	return argv
}

// TestExecCommandEmit_ScrubsArgvBeforeJournalWrite drives every exec.command
// emit site RunAgent can reach and asserts the secret never reaches the
// payload. Before the fix, all three sites wrote BuildCLICommand's argv raw:
// the `--system-prompt` element and the verbatim user message went into the
// append-only journal, where the Timeline tab renders the payload to any
// member and Export writes it to a file in one click.
func TestExecCommandEmit_ScrubsArgvBeforeJournalWrite(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// secret must not appear anywhere in any exec.command payload.
		secret string
		// registerCredential puts the secret in the run's credential set, so
		// only the per-run scrubber values can catch it.
		registerCredential bool
		// failExecCreate makes the container refuse to create the agent exec,
		// which is the orchestrator_run.go failure-path emit.
		failExecCreate bool
		// wantPhases are the payload phases that must have been emitted.
		wantPhases []string
	}{
		{
			// The start emit, fired before the agent CLI produces a byte.
			// This is the site that carries the full 41 KB --system-prompt
			// plus the verbatim user message.
			name:               "start emit scrubs a pasted credential value",
			secret:             execCmdPastedSecret,
			registerCredential: true,
			wantPhases:         []string{"start", "end"},
		},
		{
			// Same argv, no per-run registration: a token shaped like a real
			// provider key must still be caught by the built-in patterns.
			name:       "argv is scrubbed even with no credentials loaded",
			secret:     execCmdBuiltinSecret,
			wantPhases: []string{"start", "end"},
		},
		{
			// The exec-create failure emit. It fires when Docker refuses the
			// exec, i.e. on a path no happy-path test drives — precisely the
			// kind of site that drifts.
			name:               "exec-create failure emit scrubs the argv too",
			secret:             execCmdPastedSecret,
			registerCredential: true,
			failExecCreate:     true,
			wantPhases:         []string{"start", "end"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := covNewRunContainer(covRunOpts{stream: "{}\n"})
			if tc.failExecCreate {
				inner := c.route
				c.route = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
					if len(cfg.Cmd) > 0 && cfg.Cmd[0] == "stdbuf" {
						return nil, errors.New("exec create refused")
					}
					return inner(cfg)
				}
			}

			j := &covJournal{}
			o := New(c, newMemState(), covQuietLogger())
			o.SetJournal(j)

			req := covRunReq()
			req.UserMessage = "ship it with this token: " + tc.secret
			if tc.registerCredential {
				req.Credentials = []Credential{{
					EnvVarName: "GH_TOKEN",
					Type:       "SECRET",
					PlainValue: tc.secret,
				}}
			}

			err := o.RunAgent(context.Background(), req, nil)
			if tc.failExecCreate {
				if err == nil {
					t.Fatal("expected the exec-create failure to surface as an error")
				}
			} else if err != nil {
				t.Fatalf("RunAgent: %v", err)
			}

			entries := j.byType("exec.command")
			if len(entries) == 0 {
				t.Fatal("no exec.command entries emitted — the test drove nothing")
			}

			seenPhases := map[string]bool{}
			redacted := false
			for _, e := range entries {
				phase, _ := e.Payload["phase"].(string)
				seenPhases[phase] = true

				argv := execCmdEntryArgv(t, e)
				for i, arg := range argv {
					if strings.Contains(arg, tc.secret) {
						t.Errorf("exec.command (phase=%q) argv[%d] leaked the secret verbatim into the hash-chained journal: %q",
							phase, i, truncateForFailure(arg))
					}
					if strings.Contains(arg, "[REDACTED") {
						redacted = true
					}
				}
				// The joined form is checked separately: an argv split so
				// that no single element holds the whole secret would pass
				// the element-wise loop while the rendered payload still
				// reads it back.
				if strings.Contains(strings.Join(argv, " "), tc.secret) {
					t.Errorf("exec.command (phase=%q) leaked the secret across an argv element boundary", phase)
				}
				// Summary is rendered next to the payload in the same UI row.
				if strings.Contains(e.Summary, tc.secret) {
					t.Errorf("exec.command (phase=%q) summary leaked the secret: %q", phase, truncateForFailure(e.Summary))
				}
			}
			if !redacted {
				t.Error("no redaction marker anywhere in the emitted argv — the scrubber did not run")
			}
			for _, want := range tc.wantPhases {
				if !seenPhases[want] {
					t.Errorf("no exec.command entry with phase %q; got phases %v", want, seenPhases)
				}
			}
		})
	}
}

// truncateForFailure keeps a failure message readable when the offending argv
// element is the 41 KB system prompt.
func truncateForFailure(s string) string {
	if len(s) <= 200 {
		return s
	}
	return s[:200] + "…"
}

// TestExecCommandEmitSites_UseScrubbedArgv is the static half of the guard. It
// reads every non-test source file in this package, finds each `"cmd":` entry
// of a map literal (the shape every journal payload uses) and requires the
// value expression to be one the scrubber produced. A behavioural test can only
// cover the emit sites it manages to drive; this one fails the moment a new
// site is written with the raw `cmd` variable, which is exactly how the three
// sites in orchestrator_run.go drifted from their scrubbing sibling.
func TestExecCommandEmitSites_UseScrubbedArgv(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package sources: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no package sources found — the guard is scanning the wrong directory")
	}

	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}

		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if _, isMap := lit.Type.(*ast.MapType); !isMap {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.BasicLit)
				if !ok || key.Kind != token.STRING || key.Value != `"cmd"` {
					continue
				}
				checked++
				if !isScrubbedArgvExpr(kv.Value) {
					t.Errorf("%s: a map literal writes a \"cmd\" field from an expression the "+
						"credential scrubber has not been through. If this is a journal payload, "+
						"emit journalCmd (or scrubArgv(cmd, secretValues)) instead: the journal is "+
						"hash-chained and append-only, the argv carries the full --system-prompt and "+
						"the verbatim user message, and nothing can redact it after the write. If it "+
						"is not a journal payload, name the key something other than \"cmd\" — this "+
						"guard is deliberately keyed on the field name, because a fourth emit site "+
						"written raw is exactly the regression it exists to catch.",
						fset.Position(kv.Value.Pos()))
				}
			}
			return true
		})
	}

	if checked == 0 {
		t.Fatal(`no journal payload writing a "cmd" field was found — the guard no longer matches the emit sites it is meant to police`)
	}
}

// isScrubbedArgvExpr reports whether e is an argv expression the credential
// scrubber has already been through: either the journalCmd binding or a direct
// scrubArgv(...) call.
func isScrubbedArgvExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "journalCmd"
	case *ast.CallExpr:
		fn, ok := v.Fun.(*ast.Ident)
		return ok && fn.Name == "scrubArgv"
	}
	return false
}
