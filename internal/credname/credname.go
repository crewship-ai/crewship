// Package credname is the single statement of what a credential's DELIVERY
// NAME may look like — the environment variable an agent reads, the path
// component under /secrets, and the token that ends up interpolated into the
// `sh -c` that writes it.
//
// It exists because that rule used to be stated five times, in five files, and
// the five did not agree (#1657):
//
//	credentials_mutate.go   display name: 1–255 chars, any charset
//	agent_credentials.go    env_var_name: non-empty, no charset check at all
//	credential_bindings.go  slot:         ^[A-Za-z_][A-Za-z0-9_]*$
//	credential_reconcile.go env var:      ^[A-Za-z_][A-Za-z0-9_]*$
//	orchestrator/exec.go    env var:      ^[A-Z_][A-Z0-9_]*$
//
// So a name that was legal everywhere it was WRITTEN was illegal where it was
// READ, and the reader was the one place that treated the disagreement as
// fatal: a `CLI_TOKEN` a user had called `github-token` failed the uppercase
// gate in buildCredFileScript, which returned an error rather than skipping,
// which abandoned every OTHER credential in the batch, which aborted the run.
//
// Two functions, one rule:
//
//	Valid      — what a container can actually export. The reader's rule, and
//	             the only shape that is ever written to disk or to an env block.
//	Canonical  — the mapping from what an operator typed onto Valid, or an
//	             honest "no". The writer's rule is "Canonical must succeed".
//
// Canonical is deliberately narrow. It folds ASCII case and maps `-` to `_`,
// which are the two ways a credential NAME conventionally differs from an
// environment VARIABLE name (`github-acme` is the codebase's own example of a
// well-formed account name). It does not invent a name for anything else: a
// space, a colon, a leading digit or any non-ASCII byte is refused rather than
// mangled into something the operator never chose. A refusal costs one
// undelivered credential and a warning; a mangling costs a name nobody can
// predict, and two different credentials can mangle onto the same one.
package credname

// Valid reports whether name is exactly the form a container can export: an
// uppercase POSIX environment variable name. This is the reader's rule — the
// orchestrator's file writer, the revoke reconciler's `rm`, and the field
// namer all gate on it, and nothing is ever written under a name that fails.
func Valid(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'A' && c <= 'Z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Canonical maps a name an operator typed onto the one form a container can
// export, reporting false when no such form exists.
//
// The mapping is byte-wise ASCII on purpose rather than strings.ToUpper: Go's
// Unicode case folding turns 'ß' into "SS" and lowercases 'İ' into two runes,
// so a display name in a non-Latin script would silently acquire a length and a
// spelling nobody asked for on its way to a filename. Anything outside
// [A-Za-z0-9_-] is a refusal here, and the caller reports it.
//
// A name already in Valid form round-trips unchanged, which is what lets
// callers treat "Canonical(n) == n" as "the operator named a variable" and
// everything else as "the operator named an account".
func Canonical(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
			out[i] = c - ('a' - 'A')
		case c >= 'A' && c <= 'Z', c == '_':
			out[i] = c
		case c >= '0' && c <= '9':
			// A leading digit cannot be fixed by folding — `2FA_TOKEN` is not
			// an environment variable name in any shell, and prefixing an
			// underscore would hand back a name the operator never typed.
			if i == 0 {
				return "", false
			}
			out[i] = c
		case c == '-':
			out[i] = '_'
		default:
			return "", false
		}
	}
	return string(out), true
}

// Deliverable reports whether a name can reach a container under some variable.
// The writer-side gate: an endpoint that accepts a slot or an env_var_name
// rejects what this refuses, so the refusal lands on the request that chose the
// name instead of on a run days later.
func Deliverable(name string) bool {
	_, ok := Canonical(name)
	return ok
}
