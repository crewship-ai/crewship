package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Injection points for tests. Production paths use exec.LookPath /
// exec.Command directly; tests substitute deterministic stubs.
var (
	lookPath = exec.LookPath
	newJQCmd = newJQCommand
)

// jqRunner is a minimal interface over *exec.Cmd that lets tests stub out
// jq invocations. Real implementation wraps exec.Command.
type jqRunner interface {
	SetStdin([]byte)
	Output() ([]byte, error)
}

type realJQ struct {
	cmd *exec.Cmd
}

func (r *realJQ) SetStdin(b []byte) { r.cmd.Stdin = bytes.NewReader(b) }
func (r *realJQ) Output() ([]byte, error) {
	return r.cmd.Output()
}

func newJQCommand(jqPath, expr string) jqRunner {
	return &realJQ{cmd: exec.Command(jqPath, expr)}
}

// Shared helpers for the dozens of small REST-wrapper commands. Goal:
// each cmd_*.go stays a thin Cobra binding around a typed payload + a
// printer, with the HTTP and formatting plumbing factored out here.
//
// Naming convention:
//   - getJSON / postJSON / deleteJSON: one-liner request + decode with
//     workspace-scoped paths and unified error formatting.
//   - emitTable / emitListJSON: respect the --format flag uniformly.

// getJSON sends a GET to `path` and decodes the response into `out`.
// Path is relative to BaseURL (e.g. "/api/v1/agents/foo"). Caller is
// responsible for URL-escaping any user-supplied path segments.
func getJSON(client *cli.Client, path string, out any) error {
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if out == nil {
		_ = resp.Body.Close()
		return nil
	}
	return cli.ReadJSON(resp, out)
}

// postJSON sends a POST with body and decodes into out (if non-nil).
func postJSON(client *cli.Client, path string, body any, out any) error {
	resp, err := client.Post(path, body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if out == nil {
		_ = resp.Body.Close()
		return nil
	}
	return cli.ReadJSON(resp, out)
}

// patchJSON sends a PATCH with body and decodes into out (if non-nil).
// Mirrors postJSON — used by the admin-extras CRUD verbs whose handlers
// return the freshly-updated row.
func patchJSON(client *cli.Client, path string, body any, out any) error {
	resp, err := client.Patch(path, body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	if out == nil {
		_ = resp.Body.Close()
		return nil
	}
	return cli.ReadJSON(resp, out)
}

// deleteJSON sends a DELETE; ignores response body on success.
func deleteJSON(client *cli.Client, path string) error {
	resp, err := client.Delete(path)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// queryString returns "?k1=v1&k2=v2" for the given pairs, omitting empty
// values entirely. Pairs are interpreted as alternating keys/values; an
// odd-length slice has its trailing key dropped.
//
// Why a tiny helper: every list command repeats the same url.Values build
// pattern. Inlining a 10-line pattern 20 times across the new commands is
// the kind of duplication that drifts when one site adds a new param.
func queryString(pairs ...string) string {
	if len(pairs) < 2 {
		return ""
	}
	q := url.Values{}
	for i := 0; i+1 < len(pairs); i += 2 {
		k, v := pairs[i], pairs[i+1]
		if v != "" {
			q.Set(k, v)
		}
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// requireAuthAndWorkspace gates commands that need a logged-in client and
// a resolved workspace. Returns the configured client on success.
//
// Many commands repeat this exact 4-line pattern; centralising it keeps
// new commands one line shorter and the error messages consistent.
func requireAuthAndWorkspace() (*cli.Client, error) {
	if err := requireAuth(); err != nil {
		return nil, err
	}
	if err := requireWorkspace(); err != nil {
		return nil, err
	}
	return newAPIClient(), nil
}

// applyJQFilter pipes `out` through jq with the given expression,
// returning the result. Returns the input unchanged when jqExpr is empty.
// Errors when jq is unavailable so the user knows what to install.
//
// Why exec(jq) rather than a Go library: jq is the standard tool for this,
// users already have it, and pulling in a 600 KB Go-jq library for an
// optional flag bloats the binary. Falls back gracefully when jq is
// missing — the unfiltered output still lands.
func applyJQFilter(out []byte, jqExpr string) ([]byte, error) {
	if jqExpr == "" {
		return out, nil
	}
	jqPath, err := lookPath("jq")
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s[--filter requires jq, falling back to raw output]%s\n",
			cli.Yellow, cli.Reset)
		return out, nil
	}
	cmd := newJQCmd(jqPath, jqExpr)
	cmd.SetStdin(out)
	return cmd.Output()
}

// jqExprFlag wires a --filter flag onto the given Cobra command. The flag
// only kicks in when the command's output goes through emitJSONFiltered.
//
// Hooked uniformly across list-style commands so users always have the
// same "give me one field" / "filter to matching rows" escape hatch
// without each command needing bespoke flags.
func jqExprFlag(cmd *cobra.Command) {
	cmd.Flags().String("filter", "", "Pipe JSON output through `jq <expr>` (requires jq in PATH)")
}

// emitJSONFiltered marshals `v` to JSON, optionally pipes through --filter,
// and writes to stdout. Use for commands that always return JSON-shaped
// data and want the optional jq escape hatch.
func emitJSONFiltered(cmd *cobra.Command, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	jqExpr, _ := cmd.Flags().GetString("filter")
	out, err := applyJQFilter(data, jqExpr)
	if err != nil {
		return fmt.Errorf("filter: %w", err)
	}
	fmt.Print(string(out))
	if !strings.HasSuffix(string(out), "\n") {
		fmt.Println()
	}
	return nil
}
