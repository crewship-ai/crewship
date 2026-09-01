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
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
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
					// Either removal marker counts. #2205 scrubbed the element
					// the pasted secret arrived in; #2215 removes that element
					// outright, before the scrubber ever sees it, so on this
					// path the placeholder is the marker. What the assertion is
					// for is unchanged: a sanitiser that silently stopped doing
					// anything must fail here rather than pass vacuously.
					if strings.Contains(arg, "[REDACTED") || strings.Contains(arg, "[PROMPT:") {
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
				t.Error("no redaction marker and no prompt placeholder anywhere in the emitted argv — nothing sanitised it")
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
// reads every non-test source file in this package and enforces three rules on
// what may reach a persistent sink. A behavioural test can only cover the emit
// sites it manages to drive; this one fails the moment a new site is written
// with a raw value, which is exactly how the three sites in orchestrator_run.go
// drifted from their scrubbing sibling.
//
//  1. a "cmd" field of a journal payload must take its value from the
//     sanitised journalCmd binding;
//  2. a "cmd" field passed to a structured logger must do the same — the
//     process log is a persistent sink too (#2215 item 3);
//  3. no persisted field may carry a prompt-bearing expression (the raw argv,
//     a request's UserMessage or SystemPrompt, or a local holding either)
//     unless it is wrapped in a helper that removes or bounds it. Rule 3 is
//     what catches a sink that is NOT keyed on "cmd" — chat.user_message
//     stored payload["content"] = req.UserMessage for as long as it existed,
//     one entry type over from the guard that was watching the argv.
//
// "Field" covers the three ways this package writes one: a map literal entry,
// an index assignment onto a payload map, and the Summary/Payload fields of a
// JournalEntry literal. The first is how the emit sites are written today; the
// other two are how they would plausibly be written tomorrow, and a guard that
// only knows the current spelling is a guard against nothing.
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

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			aliases := promptAliases(fn)
			checked += auditPersistedFields(t, fset, fn, aliases)
		}
	}

	if checked == 0 {
		t.Fatal(`no journal payload or log call writing a "cmd" field was found — the guard no longer matches the emit sites it is meant to police`)
	}
}

// auditPersistedFields walks one function and reports how many "cmd" fields it
// checked, failing t for each violation found.
func auditPersistedFields(t *testing.T, fset *token.FileSet, fn *ast.FuncDecl, aliases map[string]bool) int {
	t.Helper()
	checked := 0

	cmdField := func(value ast.Expr, where string) {
		checked++
		if isScrubbedArgvExpr(value) {
			return
		}
		t.Errorf("%s: %s writes a \"cmd\" field from an expression that is not the sanitised "+
			"argv. Emit journalCmd (or journalArgv(...)): the journal is hash-chained and "+
			"append-only, the raw argv carries the full system prompt and the verbatim user "+
			"message, and nothing can redact it after the write. A fourth emit site written raw "+
			"is the regression this guard exists to catch — if you are here because the guard is "+
			"in your way, the value is the thing to change, not the key.",
			fset.Position(value.Pos()), where)
	}

	promptField := func(value ast.Expr, where string) {
		name, pos, leaked := promptBearingRef(value, aliases)
		if !leaked {
			return
		}
		t.Errorf("%s: %s writes %s, which carries prompt text, into a value that can be "+
			"persisted. The scrubber cannot close this — an opaque secret nobody registered "+
			"matches no pattern, which is why it is documented as defence in depth and not a "+
			"boundary (#2215). Wrap it in a helper that removes the prompt (journalArgv), "+
			"records a measurement of it instead of the text (promptRef), or bounds it to a "+
			"fixed length (journalUserMessage, truncateStr).",
			fset.Position(pos), where, name)
	}

	ast.Inspect(fn, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CompositeLit:
			// A map literal is how every payload is written today; a struct
			// literal is how JournalEntry.Summary and .Payload are.
			_, isMap := node.Type.(*ast.MapType)
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if isMap {
					if key, ok := kv.Key.(*ast.BasicLit); ok && key.Kind == token.STRING && key.Value == `"cmd"` {
						cmdField(kv.Value, "a map literal")
						continue
					}
					promptField(kv.Value, "a map literal")
					continue
				}
				// Struct literal: only the fields that get persisted.
				if key, ok := kv.Key.(*ast.Ident); ok && (key.Name == "Summary" || key.Name == "Payload") {
					promptField(kv.Value, "a journal entry's "+key.Name)
				}
			}
		case *ast.AssignStmt:
			// payload["cmd"] = cmd — the spelling the emit sites would take
			// the day one of them grows a conditional field.
			for i, lhs := range node.Lhs {
				idx, ok := lhs.(*ast.IndexExpr)
				if !ok || i >= len(node.Rhs) {
					continue
				}
				key, ok := idx.Index.(*ast.BasicLit)
				if !ok || key.Kind != token.STRING {
					continue
				}
				if key.Value == `"cmd"` {
					cmdField(node.Rhs[i], "an index assignment")
					continue
				}
				promptField(node.Rhs[i], "an index assignment")
			}
		case *ast.CallExpr:
			// Structured-logger key/value pairs: log.Info("msg", "key", x).
			//
			// Keyed on the VALUE, not the key. Keying on `"cmd"` meant
			// log.Info(..., "prompt", req.UserMessage) walked straight past
			// the guard -- and the sink this guard exists for is the prompt,
			// not the spelling of the field someone chose to put it in.
			if !isStructuredLogCall(node) {
				break
			}
			for i := 0; i+1 < len(node.Args); i++ {
				lit, ok := node.Args[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				promptField(node.Args[i+1], "a structured log call under key "+lit.Value)
			}
		}
		return true
	})
	return checked
}

// isScrubbedArgvExpr reports whether e is an argv expression the sanitiser has
// already been through: the journalCmd binding (or a field of it), or a direct
// journalArgv(...) call.
//
// scrubArgv is deliberately NOT accepted. It redacts credential values, which
// #2205 needed, but it cannot remove prompt text an opaque secret is hiding
// in — accepting it here would let a new emit site satisfy the guard while
// journalling the whole prompt.
func isScrubbedArgvExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "journalCmd"
	case *ast.SelectorExpr:
		id, ok := v.X.(*ast.Ident)
		return ok && id.Name == "journalCmd"
	case *ast.CallExpr:
		fn, ok := v.Fun.(*ast.Ident)
		return ok && fn.Name == "journalArgv"
	}
	return false
}

// boundingHelpers are the calls allowed to carry a prompt-bearing expression
// into a persisted field. Each either removes the prompt text outright
// (journalArgv), replaces it with a measurement (promptRef, len,
// utf8.RuneCountInString), or scrubs and bounds it (journalUserMessage).
// Adding a name here is a deliberate statement that the helper makes the
// value safe to persist forever, which is why the list is short and the
// entries are boring.
//
// truncateStr is deliberately NOT here (#2228 took it off). It bounds
// length but scrubs nothing, so blessing it would let a future call site
// write truncateStr(req.UserMessage, 500) straight past the guard, exactly
// the shape the approval-gate args carried before #2228 — 500 raw,
// unscrubbed characters in approvals_queue.payload. Every current call
// wraps an already-scrubbed value (journalUserMessage's own body) or a
// non-prompt string, so narrowing the allowlist costs nothing today and
// stops it from being reached for.
//
// truncateCmd is NOT here either. It is a pure join-and-clip of whatever
// argv it is handed and scrubs nothing, so blessing it would let
// truncateCmd(cmd, 4096) write 4096 raw characters of a Gemini or Codex
// argv — whose prompt is its second element. Its only safe caller passes
// journalCmd.argv, which rule 1 already covers.
var boundingHelpers = map[string]bool{
	"journalArgv":            true,
	"journalUserMessage":     true,
	"promptRef":              true,
	"len":                    true,
	"utf8.RuneCountInString": true,
	// Returns an int, and the guard's own message recommends exactly this
	// ("records a measurement of it instead of the text"). It was missing
	// only because rule 2 used to be keyed on the field name, so a log call
	// under "est_tokens" was never examined.
	"tokenutil.EstimateTokens": true,
}

// calleeName renders a call's function as "name" or "pkg.Name", or "" for
// anything more indirect (a method on a value, a func-typed field). Anything
// this cannot name is not on the allowlist, which is the safe direction.
func calleeName(fun ast.Expr) string {
	switch v := fun.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if id, ok := v.X.(*ast.Ident); ok {
			return id.Name + "." + v.Sel.Name
		}
	}
	return ""
}

// boundedByHelper reports whether e is a call to a bounding helper.
//
// truncateStr used to be one, with an extra check here that its limit
// argument was a literal within the payload's own field cap (so
// truncateStr(msg, 100000) would not have counted as bounding). #2228 took
// truncateStr off boundingHelpers entirely — it bounds length but scrubs
// nothing — which made that per-argument check unreachable; removed with it
// rather than left as dead code guarding a name the map no longer allows.
func boundedByHelper(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	return boundingHelpers[calleeName(call.Fun)]
}

// promptAliases returns the locals in fn that hold prompt text: assigned from a
// request's UserMessage or SystemPrompt (directly or through a concatenation),
// or from BuildCLICommand, which folds both into an argv.
//
// Propagation deliberately stops at a call. A call is a transformation the
// author had to write and name, and treating every result as still-prompt
// would flag promptRef's own sha256 OF the prompt — the thing that exists so
// the prompt does not have to be stored.
func promptAliases(fn *ast.FuncDecl) map[string]bool {
	aliases := map[string]bool{}
	// Two passes so a chain (a := req.UserMessage; b := a) is caught
	// regardless of the order ast.Inspect happens to visit in.
	for pass := 0; pass < 2; pass++ {
		ast.Inspect(fn, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.AssignStmt:
				for i, lhs := range node.Lhs {
					name, ok := lhs.(*ast.Ident)
					if !ok || i >= len(node.Rhs) || name.Name == "_" {
						continue
					}
					if isPromptSource(node.Rhs[i], aliases) {
						aliases[name.Name] = true
					}
				}
			case *ast.ValueSpec:
				// `var preview = req.UserMessage` is a ValueSpec, not an
				// AssignStmt. Tracking only `:=` left the plainest possible
				// alias unguarded.
				for i, name := range node.Names {
					if name.Name == "_" || i >= len(node.Values) {
						continue
					}
					if isPromptSource(node.Values[i], aliases) {
						aliases[name.Name] = true
					}
				}
			}
			return true
		})
	}
	return aliases
}

// isPromptSource reports whether an assignment's right-hand side yields prompt
// text: a bare or concatenated prompt reference, or a BuildCLICommand call.
func isPromptSource(e ast.Expr, aliases map[string]bool) bool {
	if call, ok := e.(*ast.CallExpr); ok {
		return calleeName(call.Fun) == "BuildCLICommand"
	}
	_, _, leaked := promptBearingRef(e, aliases)
	return leaked
}

// promptBearingRef reports whether e names prompt text without going through a
// bounding helper. A request's UserMessage and SystemPrompt are matched on any
// receiver, not on the name `req`, so renaming the parameter does not silence
// the guard; `cmd` and any local alias collected by promptAliases count too,
// because BuildCLICommand folds both fields into that argv.
func promptBearingRef(e ast.Expr, aliases map[string]bool) (string, token.Pos, bool) {
	if boundedByHelper(e) {
		return "", token.NoPos, false
	}
	var name string
	var pos token.Pos
	ast.Inspect(e, func(n ast.Node) bool {
		if name != "" {
			return false
		}
		switch v := n.(type) {
		case *ast.CallExpr:
			// A nested bounding call is safe on its own subtree.
			if boundedByHelper(v) {
				return false
			}
		case *ast.SelectorExpr:
			switch v.Sel.Name {
			case "UserMessage", "SystemPrompt":
				name, pos = "a request's "+v.Sel.Name, v.Pos()
				return false
			}
		case *ast.Ident:
			if v.Name == "cmd" {
				name, pos = "the raw argv `cmd`", v.Pos()
				return false
			}
			if aliases[v.Name] {
				name, pos = "`"+v.Name+"`, a local holding prompt text", v.Pos()
				return false
			}
		}
		return true
	})
	return name, pos, name != ""
}

// ---------------------------------------------------------------------------
// #2215: scrubbing is not enough — the prompt itself must not be journalled.
//
// The scrubber is defence in depth and explicitly NOT a boundary
// (docs/security/threat-model.mdx, and the comment on
// Scrubber.AddSecretValues): an opaque secret nobody registered matches no
// pattern, so a user who pastes an internal token of a shape we do not know
// still lands it in a hash-chained row. The only closure is to stop persisting
// the prompt-bearing values at all. The tests below assert that property on
// every sink #2215 names.
// ---------------------------------------------------------------------------

// execCmdSystemPromptMarker / execCmdUserMessageMarker are distinctive strings
// planted in the two prompt-bearing request fields. Neither may appear anywhere
// in an exec.command entry — not in the argv, not in a nested payload value,
// not in the summary. They match no scrubber pattern on purpose: that is the
// whole point of #2215.
const (
	execCmdSystemPromptMarker = "SYSTEM-PROMPT-BODY-e3f19a4d"
	execCmdUserMessageMarker  = "USER-MESSAGE-BODY-7c2d84b1"
)

// The bounds the emit sites are held to. Written out here rather than imported
// from the implementation on purpose: a guard that reads the same constant it
// is guarding cannot notice that constant being raised.
const (
	wantExecCmdArgvMaxChars     = 4096
	wantExecCmdFieldMaxChars    = 512
	wantChatUserMessageMaxChars = 240
)

// execCmdAdapterNames returns every registered CLI adapter plus the unknown
// fallback, so a NEW adapter that puts the prompt somewhere new in its argv is
// covered the moment it is registered rather than when someone remembers to
// extend a hard-coded list.
func execCmdAdapterNames() []string {
	names := []string{"NOT_A_REGISTERED_ADAPTER"} // getAdapter's unknownAdapter fallback
	for name := range adapterRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// execCmdPayloadJSON renders a whole payload the way the API serialises it
// (internal/journal/serialize.go puts payload on the wire verbatim), so the
// assertion covers nested values and any key added later — not just the ones
// this test happens to name.
func execCmdPayloadJSON(t *testing.T, e JournalEntry) string {
	t.Helper()
	b, err := json.Marshal(e.Payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(b)
}

// TestExecCommandEmit_DoesNotJournalPromptText is the #2215 guard: no
// exec.command entry may carry the system prompt, the crewship preamble, or the
// user message — for ANY adapter. Every adapter folds the prompt into argv
// somewhere (a --system-prompt flag, a -p value, a "[SYSTEM]…[USER]…" blob
// after `--`), which is why this is table-driven over the registry rather than
// written against the Claude shape.
func TestExecCommandEmit_DoesNotJournalPromptText(t *testing.T) {
	t.Parallel()

	// A distinctive slice of the preamble: the preamble is prompt text the
	// product deliberately does not show, and it is the bulk of the argv.
	preambleProbe := strings.TrimSpace(crewshipSystemPreamble)
	if len(preambleProbe) > 80 {
		preambleProbe = preambleProbe[:80]
	}

	for _, adapter := range execCmdAdapterNames() {
		t.Run(adapter, func(t *testing.T) {
			t.Parallel()

			c := covNewRunContainer(covRunOpts{stream: "{}\n"})
			j := &covJournal{}
			o := New(c, newMemState(), covQuietLogger())
			o.SetJournal(j)

			req := covRunReq()
			req.CLIAdapter = adapter
			req.LLMModel = "test-model"
			req.ToolProfile = "CODING"
			req.SystemPrompt = execCmdSystemPromptMarker
			req.UserMessage = execCmdUserMessageMarker

			if err := o.RunAgent(context.Background(), req, nil); err != nil {
				t.Fatalf("RunAgent: %v", err)
			}

			entries := j.byType("exec.command")
			if len(entries) == 0 {
				t.Fatal("no exec.command entries emitted — the test drove nothing")
			}

			for _, e := range entries {
				phase, _ := e.Payload["phase"].(string)
				body := execCmdPayloadJSON(t, e) + "\n" + e.Summary
				for _, banned := range []struct{ what, text string }{
					{"the system prompt", execCmdSystemPromptMarker},
					{"the user message", execCmdUserMessageMarker},
					{"the crewship system preamble", preambleProbe},
				} {
					if strings.Contains(body, banned.text) {
						t.Errorf("exec.command (adapter=%s phase=%q) persisted %s into the hash-chained journal",
							adapter, phase, banned.what)
					}
				}

				// The argv SHAPE is kept for the Crow's Nest terminal block,
				// so whatever carried the prompt must still be there as a
				// placeholder naming the element and its length.
				argv := execCmdEntryArgv(t, e)
				if !strings.Contains(strings.Join(argv, " "), "[PROMPT:") {
					t.Errorf("exec.command (adapter=%s phase=%q) argv has no [PROMPT:…] placeholder: the prompt-bearing element was dropped silently rather than named: %v",
						adapter, phase, argv)
				}

				// The typed replacement: enough to answer "what ran and with
				// which prompt" without storing the prompt.
				for _, key := range []string{"adapter", "model", "phase", "tool_profile", "container_id", "truncated", "prompt"} {
					if _, ok := e.Payload[key]; !ok {
						t.Errorf("exec.command (adapter=%s phase=%q) payload has no %q field; got keys %v",
							adapter, phase, key, sortedPayloadKeys(e.Payload))
					}
				}
				ref, ok := e.Payload["prompt"].(map[string]any)
				if !ok {
					t.Fatalf("exec.command payload prompt is %T, want map[string]any", e.Payload["prompt"])
				}
				if h, _ := ref["system_sha256"].(string); len(h) != 64 {
					t.Errorf("prompt.system_sha256 = %q, want a 64-char sha256 hex digest — it is the prompt VERSION the run used", h)
				}
				if n, _ := ref["system_chars"].(int); n <= 0 {
					t.Errorf("prompt.system_chars = %v, want the omitted prompt's length", ref["system_chars"])
				}
				// user_turn_chars, not user_chars: by the time the argv is
				// built, req.UserMessage is the composed turn (the human's
				// message plus the conversation history, recall and nudge
				// blocks prependSessionContext folded in). Naming it
				// user_chars invited it to be read as the length of what the
				// human typed, which is the sibling chat.user_message row.
				if n, _ := ref["user_turn_chars"].(int); n < len([]rune(execCmdUserMessageMarker)) {
					t.Errorf("prompt.user_turn_chars = %v, want at least the message's own %d",
						ref["user_turn_chars"], len([]rune(execCmdUserMessageMarker)))
				}
				if _, present := ref["user_chars"]; present {
					t.Error("prompt.user_chars must not exist — it read as the human's message length when it is the composed turn's")
				}
				// A hash of user-typed text is NOT recorded: for a short,
				// low-entropy message a digest is a verifier for the very
				// value #2215 is about. The chat_id is the safe reference.
				if _, present := ref["user_sha256"]; present {
					t.Error("prompt.user_sha256 must not exist — a digest of user-typed text is a verifier for a pasted token")
				}
				if ref["chat_id"] != req.ChatID {
					t.Errorf("prompt.chat_id = %v, want %q (the safe reference to where the message actually lives)", ref["chat_id"], req.ChatID)
				}
			}
		})
	}
}

// sortedPayloadKeys keeps a failure message deterministic.
func sortedPayloadKeys(p map[string]any) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestExecCommandEmit_ArgvIsCapped covers the second half of the fix: whatever
// survives prompt removal is still bounded, and says so. Without a cap the
// payload size is whatever the adapter happened to build — the prompt is the
// big one today, but --config blobs and model identifiers are caller-controlled
// too, and this table is append-only storage.
func TestExecCommandEmit_ArgvIsCapped(t *testing.T) {
	t.Parallel()

	c := covNewRunContainer(covRunOpts{stream: "{}\n"})
	j := &covJournal{}
	o := New(c, newMemState(), covQuietLogger())
	o.SetJournal(j)

	req := covRunReq()
	// A caller-controlled argv element that is not prompt text, so prompt
	// removal cannot be what bounds it.
	req.LLMModel = strings.Repeat("m", 32*1024)

	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	entries := j.byType("exec.command")
	if len(entries) == 0 {
		t.Fatal("no exec.command entries emitted")
	}
	for _, e := range entries {
		argv := execCmdEntryArgv(t, e)
		total := len([]rune(strings.Join(argv, "")))
		if total > wantExecCmdArgvMaxChars+128 {
			t.Errorf("exec.command argv is %d chars, want it capped near %d", total, wantExecCmdArgvMaxChars)
		}
		if truncated, _ := e.Payload["truncated"].(bool); !truncated {
			t.Errorf("exec.command payload truncated = %v, want true — a capped payload must say it was capped", e.Payload["truncated"])
		}
		// The typed fields are caller-controlled too. A cap that bounds the
		// argv and then copies the same 32 KB into a scalar beside it has not
		// bounded the payload.
		for _, key := range []string{"adapter", "model", "tool_profile"} {
			v, _ := e.Payload[key].(string)
			if n := len([]rune(v)); n > wantExecCmdFieldMaxChars+1 {
				t.Errorf("exec.command payload %q is %d chars, want it capped at %d", key, n, wantExecCmdFieldMaxChars)
			}
		}
	}
}

// TestCapArgv_HonoursItsTotalBudget pins the bound the payload doc publishes.
// capArgv charged only the per-element limit against the budget while appending
// a "…[truncated, N chars]" suffix it never accounted for, so enough oversized
// elements walked the total past the number the guard and the docs both quote.
func TestCapArgv_HonoursItsTotalBudget(t *testing.T) {
	t.Parallel()

	argv := make([]string, 16)
	for i := range argv {
		argv[i] = strings.Repeat("x", 900)
	}
	got, truncated := capArgv(argv)
	if !truncated {
		t.Fatal("capArgv reported no truncation for an argv far over the budget")
	}
	if n := len([]rune(strings.Join(got, ""))); n > wantExecCmdArgvMaxChars {
		t.Errorf("capArgv produced %d chars, over its own %d budget", n, wantExecCmdArgvMaxChars)
	}
}

// TestExecCommandEmit_ShortMessageDoesNotEatTheBinary covers the other side of
// prompt removal. Exact matching is honoured at any length so a short message
// delivered as its own argv element is still removed — but argv[0] is the
// binary, never prompt text, and a chat message of "claude" replacing it left
// the terminal block with no command name at all.
func TestExecCommandEmit_ShortMessageDoesNotEatTheBinary(t *testing.T) {
	t.Parallel()

	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	o.SetJournal(j)

	req := covRunReq()
	req.UserMessage = "claude"

	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	entries := j.byType("exec.command")
	if len(entries) == 0 {
		t.Fatal("no exec.command entries emitted")
	}
	for _, e := range entries {
		argv := execCmdEntryArgv(t, e)
		if len(argv) == 0 {
			t.Fatal("empty argv in payload")
		}
		if argv[0] != "claude" {
			t.Errorf("argv[0] = %q, want the binary name — argv[0] is never prompt text", argv[0])
		}
		// The message itself must still be gone.
		if strings.Contains(strings.Join(argv[1:], " "), "-- claude") {
			t.Errorf("the user message survived as a positional argument: %v", argv)
		}
	}
}

// TestChatUserMessageEmit_CapsAndScrubsPayloadContent is #2215's second sink.
// chat.user_message stored payload["content"] = req.UserMessage verbatim and
// uncapped, into the same append-only table, while its own summary was capped
// at 240 chars — so the cap that existed was on the shorter of the two.
func TestChatUserMessageEmit_CapsAndScrubsPayloadContent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name               string
		message            string
		secret             string
		registerCredential bool
		wantTruncated      bool
	}{
		{
			// A credential value the run holds: only per-run registration
			// catches it, and it sits at the front so truncation cannot be
			// what removed it.
			name:               "registered credential value is scrubbed out of the payload",
			message:            execCmdPastedSecret + " — please deploy with that",
			secret:             execCmdPastedSecret,
			registerCredential: true,
		},
		{
			// A provider-shaped key with no credentials loaded: the built-in
			// patterns must still fire on the payload, not just the summary.
			name:    "provider-shaped key is scrubbed with no credentials loaded",
			message: execCmdBuiltinSecret + " — use this key",
			secret:  execCmdBuiltinSecret,
		},
		{
			name:          "a long message is capped, and the entry says so",
			message:       strings.Repeat("u", 4096),
			wantTruncated: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			j := &covJournal{}
			o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
			o.SetJournal(j)

			req := covRunReq()
			req.UserMessage = tc.message
			if tc.registerCredential {
				req.Credentials = []Credential{{
					EnvVarName: "GH_TOKEN",
					Type:       "SECRET",
					PlainValue: tc.secret,
				}}
			}

			if err := o.RunAgent(context.Background(), req, nil); err != nil {
				t.Fatalf("RunAgent: %v", err)
			}

			msgs := j.byType("chat.user_message")
			if len(msgs) != 1 {
				t.Fatalf("want 1 chat.user_message entry, got %d", len(msgs))
			}
			e := msgs[0]

			content, ok := e.Payload["content"].(string)
			if !ok {
				t.Fatalf("chat.user_message payload content is %T, want string", e.Payload["content"])
			}
			if n := len([]rune(content)); n > wantChatUserMessageMaxChars+1 {
				t.Errorf("chat.user_message payload content is %d chars, want it capped at %d like the summary already was",
					n, wantChatUserMessageMaxChars)
			}
			if tc.secret != "" {
				if strings.Contains(content, tc.secret) {
					t.Errorf("chat.user_message payload content leaked the secret into the hash-chained journal: %q", truncateForFailure(content))
				}
				if strings.Contains(e.Summary, tc.secret) {
					t.Errorf("chat.user_message summary leaked the secret: %q", truncateForFailure(e.Summary))
				}
				if !strings.Contains(content, "[REDACTED") {
					t.Error("no redaction marker in the payload content — the scrubber did not run")
				}
			}
			if truncated, _ := e.Payload["truncated"].(bool); truncated != tc.wantTruncated {
				t.Errorf("chat.user_message payload truncated = %v, want %v", e.Payload["truncated"], tc.wantTruncated)
			}
			// The full length is still recorded — the cap bounds what is
			// stored, it does not hide that something was cut. In runes: the
			// field is named chars, and one of these messages carries an em
			// dash, which len() counts three times.
			if e.Payload["length_chars"] != len([]rune(tc.message)) {
				t.Errorf("payload length_chars = %v, want %d", e.Payload["length_chars"], len([]rune(tc.message)))
			}
		})
	}
}

// TestChatUserMessageEmit_OpaqueSecretSurvivesTheScrub pins a LIMITATION, not
// a property. It asserts the thing this entry still cannot do, so the trade-off
// lives in code rather than only in a paragraph nobody re-reads.
//
// exec.command closed the opaque-secret case by removing the prompt-bearing
// text outright — it could, because nothing needed a 41 KB system prompt in a
// journal payload. chat.user_message cannot use the same move without changing
// what the Timeline renders: the human's message IS the entry, and "what kicked
// this off" is the whole reason the row exists. So its 240-char preview is
// scrubbed, and a secret the scrubber does not recognise survives inside it —
// the exact gap that makes the scrubber defence in depth rather than a
// boundary.
//
// Raised by CodeRabbit on #2212; the decision (drop the text and keep only a
// length plus chat_id, at the cost of the Timeline row) is #2229.
//
// If you are here because this test failed: the limitation was closed, which is
// good. Replace this test with the assertion CodeRabbit asked for — that the
// marker appears in neither the payload nor the summary — and update
// docs/guides/crew-journal.mdx.
func TestChatUserMessageEmit_OpaqueSecretSurvivesTheScrub(t *testing.T) {
	t.Parallel()

	j := &covJournal{}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	o.SetJournal(j)

	req := covRunReq()
	// Unregistered, and shaped like nothing the pattern set knows. At the very
	// front, so the cap is not what would remove it either.
	req.UserMessage = execCmdPastedSecret + " — deploy with that please"

	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	msgs := j.byType("chat.user_message")
	if len(msgs) != 1 {
		t.Fatalf("want 1 chat.user_message entry, got %d", len(msgs))
	}
	content, _ := msgs[0].Payload["content"].(string)

	if !strings.Contains(content, execCmdPastedSecret) {
		t.Errorf("the opaque secret is gone from payload content — the limitation this test documents was closed; see the doc comment for what to do now: %q",
			truncateForFailure(content))
	}
	if !strings.Contains(msgs[0].Summary, execCmdPastedSecret) {
		t.Errorf("the opaque secret is gone from the summary — same as above: %q", truncateForFailure(msgs[0].Summary))
	}
	// And the reference that makes closing it cheap is already there: the
	// message lives in the chat, which unlike this row can be erased.
	if msgs[0].Payload["chat_id"] != req.ChatID {
		t.Errorf("payload chat_id = %v, want %q", msgs[0].Payload["chat_id"], req.ChatID)
	}
	if msgs[0].Payload["length_chars"] != len([]rune(req.UserMessage)) {
		t.Errorf("payload length_chars = %v, want %d", msgs[0].Payload["length_chars"], len([]rune(req.UserMessage)))
	}
}

// TestScrubArgv_StillRedactsNonPromptElements keeps the #2205 layer honest.
// Removing the prompt-bearing elements outright means the scrubber no longer
// sees the argv element that used to carry a pasted token, so the end-to-end
// test above can no longer prove the scrubber runs. It still must: a credential
// can reach a NON-prompt element (an adapter --config value, a model id routed
// from a credential), and #2205's answer to that is defence in depth.
func TestScrubArgv_StillRedactsNonPromptElements(t *testing.T) {
	t.Parallel()

	argv := []string{"claude", "--config", "key=" + execCmdPastedSecret, "--model", execCmdBuiltinSecret}
	got := scrubArgv(argv, []string{execCmdPastedSecret})

	joined := strings.Join(got, " ")
	if strings.Contains(joined, execCmdPastedSecret) {
		t.Errorf("scrubArgv left the registered credential value in place: %q", joined)
	}
	if strings.Contains(joined, execCmdBuiltinSecret) {
		t.Errorf("scrubArgv left a provider-shaped key in place: %q", joined)
	}
	if !strings.Contains(joined, "[REDACTED") {
		t.Errorf("scrubArgv produced no redaction marker: %q", joined)
	}
}

// The element and field caps above are per-value. Nothing was asserting the
// thing the change exists for: the SIZE of the finished payload. Five new
// fields, each comfortably under its own cap, would put this entry back into
// kilobytes without turning a single existing test red — and this type was
// 64% of all journal content at 43 KB per entry, written twice per run.
//
// The budget is deliberately loose (2 KiB against a measured 773/817 bytes for
// a 41,011-char prompt) so ordinary additions do not trip it. It is a
// tripwire against drifting back toward the old order of magnitude, not a
// style rule about adding a field.
func TestExecCommandEmit_PayloadStaysSmall(t *testing.T) {
	t.Parallel()

	const budget = 2048

	c := covNewRunContainer(covRunOpts{stream: "{}\n"})
	j := &covJournal{}
	o := New(c, newMemState(), covQuietLogger())
	o.SetJournal(j)

	req := covRunReq()
	req.LLMModel = "test-model"
	req.ToolProfile = "CODING"
	// The measured real shape: the system prompt that made these entries
	// 43 KB, plus a long typed turn.
	req.SystemPrompt = strings.Repeat("S", 41011)
	req.UserMessage = strings.Repeat("U", 4000)

	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	entries := j.byType("exec.command")
	if len(entries) == 0 {
		t.Fatal("no exec.command entries emitted — the test drove nothing")
	}

	for _, e := range entries {
		phase, _ := e.Payload["phase"].(string)
		got := len(execCmdPayloadJSON(t, e))
		if got > budget {
			t.Errorf("exec.command (phase=%q) payload is %d bytes, over the %d-byte budget.\n"+
				"The journal is append-only: an oversized payload here cannot be trimmed later, "+
				"and this entry type is written twice per run. If the growth is deliberate, "+
				"raise the budget in this test and say why in the commit.",
				phase, got, budget)
		}
	}
}

// isStructuredLogCall reports whether e is a slog-style logging call.
//
// The value-keyed check above must not fire on argv CONSTRUCTION. BuildCLICommand
// legitimately does append(cmd, "--system-prompt", systemPrompt) -- the prompt has
// to reach the CLI, that is the whole point. Both are CallExprs with a string
// literal followed by prompt text, so without this the guard flags the one site
// that is required to carry it.
func isStructuredLogCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Debug", "Info", "Warn", "Error",
		"DebugContext", "InfoContext", "WarnContext", "ErrorContext":
		return true
	}
	return false
}
