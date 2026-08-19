package main

// `crewship page grant|revoke|grants` — the Pages ACL from the command line
// (docs/prd/pages.md §7.1b, §11, §11b decision 13).
//
//	GET    /api/v1/pages/{slug}/grants   page grants <slug>
//	PUT    /api/v1/pages/{slug}/grants   page grant  <slug> --user|--crew|--agent <ref> --level …
//	DELETE /api/v1/pages/{slug}/grants   page revoke <slug> --user|--crew|--agent <ref>
//
// The flags are the PRD's, verbatim (§7.1b):
//
//	crewship page grant  fleet-201 --agent watcher --level produce --panels sluzby,zatizeni
//	crewship page grant  fleet-201 --crew  lookout --level read
//	crewship page grant  fleet-201 --user  ada@example.com --level write
//	crewship page revoke fleet-201 --agent watcher
//	crewship page grants fleet-201
//
// Three properties this file holds to:
//
//  1. REVOKE IS SYMMETRIC WITH GRANT (§11b decision 13). The same three
//     subject flags, resolved by the same references. "An asymmetric revoke is
//     how a grant becomes impossible to remove", and the CLI is where that
//     asymmetry would first be felt.
//
//  2. THE SUBJECT IS A REFERENCE, NOT AN ID. `--agent watcher`, not a CUID.
//     The server resolves it, exactly as it resolves `owner: crew/lookout` in
//     a page spec — one resolution path, on the side that owns the tables.
//
//  3. IT NEVER DECIDES ANYTHING. Whether a grant may be issued, and whether a
//     stored one is still worth anything, are server-side questions (§7.1
//     rule 5). `page grants` prints the server's own verdict on each row,
//     including the word "inert" and the reason — a client that filtered those
//     out would be hiding the one thing the operator needs to see.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// ── The wire (mirrors internal/api/pages_grants.go) ────────────────────────

type pageGrantJSON struct {
	SubjectType string   `json:"subject_type"`
	Subject     string   `json:"subject"`
	SubjectID   string   `json:"subject_id"`
	Level       string   `json:"level"`
	Panels      []string `json:"panels"`
	GrantedBy   string   `json:"granted_by"`
	GrantedAt   string   `json:"granted_at"`
	Live        bool     `json:"live"`
	InertReason string   `json:"inert_reason"`
}

type pageGrantsJSON struct {
	Page   string          `json:"page"`
	Grants []pageGrantJSON `json:"grants"`
}

// pageGrantWriteBody is the PUT body.
type pageGrantWriteBody struct {
	SubjectType string   `json:"subject_type"`
	Subject     string   `json:"subject"`
	Level       string   `json:"level"`
	Panels      []string `json:"panels,omitempty"`
}

// ── grant ──────────────────────────────────────────────────────────────────

var pageGrantCmd = &cobra.Command{
	Use:   "grant <slug>",
	Short: "Grant read, produce or write on a page to a user, a crew or an agent",
	Long: `Widen who reaches a page. Three verbs, deliberately separate:

  read     may see the page and its panels
  produce  may push payloads into NAMED panels (--panels)
  write    may edit the page spec — add, remove and re-arrange panels

  crewship page grant fleet-201 --agent watcher --level produce --panels sluzby
  crewship page grant fleet-201 --crew lookout --level read
  crewship page grant fleet-201 --user ada@example.com --level write

Two rules the server enforces and this command cannot talk it out of:

  A grant widens access to the PAGE, never to a crew's data. A grantee
  still sees only the panels their own crew membership permits; the rest
  arrive as sealed placeholders.

  Only a human issues a grant. An agent holding "write" may rebuild the
  page freely but can never widen who reaches it — not even to an agent
  in its own crew.

An agent's authority is a subset of yours and is re-checked every time it
is used: if you lose access to a crew, every grant you issued narrows
with you. "crewship page grants <slug>" shows which rows are still live.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		subjectType, subject, err := pageGrantSubjectFromFlags(cmd)
		if err != nil {
			return err
		}
		level := strings.ToLower(strings.TrimSpace(mustFlagString(cmd, "level")))
		if level == "" {
			return cli.WithExitCode(errors.New(
				`--level is required: "read" (see the page), "produce" (push into named panels) or "write" (edit the spec)`),
				cli.ExitValidation)
		}
		panels, _ := cmd.Flags().GetStringSlice("panels")
		panels = pageGrantCleanPanels(panels)
		if len(panels) > 0 && level != "produce" {
			return cli.WithExitCode(fmt.Errorf(
				"--panels scopes a produce grant and nothing else; %q covers the whole page", level), cli.ExitValidation)
		}

		client, err := pageClient()
		if err != nil {
			return err
		}
		body := pageGrantWriteBody{SubjectType: subjectType, Subject: subject, Level: level, Panels: panels}
		resp, err := client.Put("/api/v1/pages/"+pagePathEscape(args[0])+"/grants", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		scope := ""
		if len(panels) > 0 {
			scope = fmt.Sprintf(", scoped to %s", strings.Join(panels, ", "))
		}
		fmt.Printf("Granted %s on %s to %s/%s%s.\n", level, args[0], subjectType, subject, scope)
		return nil
	},
}

// ── revoke ─────────────────────────────────────────────────────────────────

var pageRevokeCmd = &cobra.Command{
	Use:   "revoke <slug>",
	Short: "Withdraw a user's, crew's or agent's grants on a page",
	Long: `Withdraw a grant. Symmetric with "page grant": the same three subject
flags, the same references.

  crewship page revoke fleet-201 --agent watcher
  crewship page revoke fleet-201 --user ada@example.com --level produce

With no --level, every level that subject holds on the page is withdrawn —
"this agent no longer reaches this page" is the thing you mean during an
incident, and having to name each of three grants is how one gets missed.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		subjectType, subject, err := pageGrantSubjectFromFlags(cmd)
		if err != nil {
			return err
		}
		level := strings.ToLower(strings.TrimSpace(mustFlagString(cmd, "level")))

		client, err := pageClient()
		if err != nil {
			return err
		}
		q := url.Values{}
		q.Set("subject_type", subjectType)
		q.Set("subject", subject)
		if level != "" {
			q.Set("level", level)
		}
		// The subject travels on the query string, not in a body: a DELETE
		// body is optional in HTTP and proxies drop it, and a revoke whose
		// subject went missing in flight would remove either nothing or
		// everything.
		resp, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/grants?" + q.Encode())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		what := "every grant"
		if level != "" {
			what = level
		}
		fmt.Printf("Revoked %s on %s from %s/%s.\n", what, args[0], subjectType, subject)
		return nil
	},
}

// ── grants ─────────────────────────────────────────────────────────────────

var pageGrantsCmd = &cobra.Command{
	Use:   "grants <slug>",
	Short: "List a page's grants, and say which of them are still live",
	Long: `Show the page's ACL.

The STATUS column is the server's use-time verdict, not a stored field: a
grant is only worth anything while the human who issued it could issue it
again today. A row that reads "inert" is still stored and still visible —
it simply authorises nothing, and the reason says why.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/grants")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response: %w", err)
		}
		f := newFormatter()
		switch f.Format {
		case "json", "yaml", "ndjson":
			return pageEmitMachine(f, raw, "{}")
		}
		var doc pageGrantsJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(doc.Grants) == 0 {
			fmt.Printf("Page %s has no grants — it is reachable by its owner, workspace admins, and the crews that own its panels.\n", args[0])
			fmt.Println("Widen it: crewship page grant " + args[0] + " --crew <slug> --level read")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "SUBJECT\tLEVEL\tPANELS\tGRANTED BY\tGRANTED AT\tSTATUS")
		for _, g := range doc.Grants {
			fmt.Fprintf(w, "%s/%s\t%s\t%s\t%s\t%s\t%s\n",
				g.SubjectType, pageDash(g.Subject), pageDash(g.Level),
				pageDash(strings.Join(g.Panels, ",")), pageDash(g.GrantedBy),
				pageDash(g.GrantedAt), pageGrantStatus(g))
		}
		return w.Flush()
	},
}

// pageGrantStatus renders the server's verdict. The reason travels with the
// word: "inert" on its own tells an operator something is wrong without
// telling them what to fix.
func pageGrantStatus(g pageGrantJSON) string {
	if g.Live {
		return "live"
	}
	if strings.TrimSpace(g.InertReason) == "" {
		return "inert"
	}
	return "inert — " + g.InertReason
}

// pageGrantSubjectFromFlags reads exactly one of --user / --crew / --agent.
//
// Exactly one, enforced here rather than left to the server: two subject flags
// on one command line is an operator who meant two commands, and picking one
// of them silently is how the other grant never gets issued.
func pageGrantSubjectFromFlags(cmd *cobra.Command) (subjectType, subject string, err error) {
	found := make([]string, 0, 3)
	for _, kind := range []string{"user", "crew", "agent"} {
		if ref := strings.TrimSpace(mustFlagString(cmd, kind)); ref != "" {
			found = append(found, kind)
			subjectType, subject = kind, ref
		}
	}
	switch len(found) {
	case 1:
		return subjectType, subject, nil
	case 0:
		return "", "", cli.WithExitCode(errors.New(
			"name the subject: --user <email>, --crew <slug> or --agent <slug>"), cli.ExitValidation)
	default:
		return "", "", cli.WithExitCode(fmt.Errorf(
			"--%s and --%s were both given; a grant has exactly one subject, so run one command per subject",
			found[0], found[1]), cli.ExitValidation)
	}
}

// pageGrantCleanPanels drops blanks and duplicates while keeping the order
// they were written in.
func pageGrantCleanPanels(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// mustFlagString (cmd_credential_binding.go) reads a string flag, trimmed,
// treating a lookup miss as empty.

func init() {
	for _, c := range []*cobra.Command{pageGrantCmd, pageRevokeCmd} {
		c.Flags().String("user", "", "Subject: a workspace member, by email or id")
		c.Flags().String("crew", "", "Subject: a crew, by slug or id")
		c.Flags().String("agent", "", "Subject: an agent, by slug or id")
	}
	pageGrantCmd.Flags().String("level", "", `What is granted: "read", "produce" or "write"`)
	pageGrantCmd.Flags().StringSlice("panels", nil,
		"Panel ids a produce grant covers (comma-separated, or repeat the flag); omit to cover every panel")
	pageRevokeCmd.Flags().String("level", "",
		`Withdraw only this level ("read", "produce", "write"); omit to withdraw every level the subject holds`)

	pageCmd.AddCommand(pageGrantCmd)
	pageCmd.AddCommand(pageRevokeCmd)
	pageCmd.AddCommand(pageGrantsCmd)
}
