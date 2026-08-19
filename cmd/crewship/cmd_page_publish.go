package main

// `crewship page publish` / `links` / `unpublish` — public pages (PRD
// docs/prd/pages.md §7.3).
//
// One command per endpoint, which is the repo rule:
//
//	POST   /api/v1/pages/{slug}/public             page publish <slug>
//	GET    /api/v1/pages/{slug}/public             page links <slug>
//	DELETE /api/v1/pages/{slug}/public/{tokenId}   page unpublish <slug> --id <id>
//
// Three things this file deliberately does NOT do:
//
//  1. It never puts a password in argv. §7.3.3 says the password is never in a
//     URL, and the same argument retires `--password <secret>`: argv is in `ps`
//     for every user on the box and in the shell history afterwards. There is
//     only `--password-stdin`, which reads a pipe or prompts on a terminal with
//     echo off.
//
//  2. It never asks the server for a token back. The token is printed once, by
//     the publish command, from the 201 response — the column holds a SHA-256
//     hash and there is nothing to re-read. `page links` lists what exists and
//     what it costs to lose; it cannot show you the link again.
//
//  3. It never invents an expiry. Omitting --expires-in-days sends no field and
//     lets the server apply its own default, so the two cannot drift; the help
//     text names the number rather than the flag defaulting to it.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ── The wire (mirrors internal/api/pages_public_tokens.go) ─────────────────

type pagePublicTokenJSON struct {
	ID             string   `json:"id"`
	Token          string   `json:"token"`
	URL            string   `json:"url"`
	ExpiresAt      string   `json:"expires_at"`
	ShowProvenance bool     `json:"show_provenance"`
	HasPassword    bool     `json:"has_password"`
	CreatedBy      string   `json:"created_by"`
	CreatedAt      string   `json:"created_at"`
	RevokedAt      string   `json:"revoked_at"`
	LastSeenAt     string   `json:"last_seen_at"`
	Live           bool     `json:"live"`
	Panels         []string `json:"panels"`
}

type pagePublicTokensJSON struct {
	Page   string                `json:"page"`
	Tokens []pagePublicTokenJSON `json:"tokens"`
}

type pagePublishBody struct {
	ExpiresInDays  *int    `json:"expires_in_days,omitempty"`
	Password       *string `json:"password,omitempty"`
	ShowProvenance *bool   `json:"show_provenance,omitempty"`
}

// ── publish ────────────────────────────────────────────────────────────────

var pagePublishCmd = &cobra.Command{
	Use:   "publish <slug>",
	Short: "Publish a page to someone outside the workspace, behind an expiring link",
	Long: `Mint a public link to a page — for the accountant, the client, an
external auditor — optionally behind a password.

  crewship page publish uzaverka
  crewship page publish uzaverka --expires-in-days 7
  crewship page publish uzaverka --show-provenance
  printf '%s' "$PASSWORD" | crewship page publish uzaverka --password-stdin

Five rules the server enforces, and this command cannot talk it out of any
of them:

  Read-only. A public page renders no buttons, ever. A button behind a
  public link is remote code execution with a URL for a credential.

  Opt-in per PANEL. Only panels marked "public: true" in the page spec are
  published — publishing is never a bulk action over panels nobody looked
  at. A page with none marked is refused rather than shared empty.

  Only a human publishes. An agent may build the page and may not make it
  public, and may not add a panel to an already-public page without a human
  saving that spec themselves.

  Every link expires. Default 30 days, maximum 1 year. There is no value
  that means "never".

  Provenance is stripped. Run ids, agent slugs, crew slugs and producer
  names are internal vocabulary and do not leave with the page.
  --show-provenance opts them back in, per link.

The token is printed ONCE, here. It is stored hashed, so nothing can show
it to you again — copy it now, or publish a second link. Several links per
page is the intended shape: revoking the accountant's does not break the
client's.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body := pagePublishBody{}

		if cmd.Flags().Changed("expires-in-days") {
			days, _ := cmd.Flags().GetInt("expires-in-days")
			body.ExpiresInDays = &days
		}
		if cmd.Flags().Changed("show-provenance") {
			show, _ := cmd.Flags().GetBool("show-provenance")
			body.ShowProvenance = &show
		}
		if stdin, _ := cmd.Flags().GetBool("password-stdin"); stdin {
			password, err := readPagePassword()
			if err != nil {
				return err
			}
			body.Password = &password
		}

		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/public", body)
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
		var tok pagePublicTokenJSON
		if err := json.Unmarshal(raw, &tok); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("Published %s.\n\n", args[0])
		fmt.Printf("  %s%s\n\n", strings.TrimSuffix(client.BaseURL, "/"), tok.URL)
		fmt.Printf("  panels     %s\n", pageDash(strings.Join(tok.Panels, ", ")))
		fmt.Printf("  expires    %s\n", pagePublicWhen(tok.ExpiresAt))
		fmt.Printf("  password   %s\n", pagePublicYesNo(tok.HasPassword))
		fmt.Printf("  provenance %s\n", pagePublicOnOff(tok.ShowProvenance))
		fmt.Printf("  link id    %s\n\n", tok.ID)
		fmt.Println("This is the only time the link is shown — it is stored hashed.")
		fmt.Printf("Withdraw it with: crewship page unpublish %s --id %s --yes\n", args[0], tok.ID)
		return nil
	},
}

// readPagePassword takes the password off stdin, or prompts for it on a
// terminal with echo off. Never from a flag value: argv is world-readable in
// `ps` and lands in the shell history, which is the same disclosure §7.3.3
// refuses for the URL.
func readPagePassword() (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password for this link: ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", cli.WithExitCode(fmt.Errorf("read password: %w", err), cli.ExitValidation)
		}
		return strings.TrimRight(string(raw), "\r\n"), nil
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 4096))
	if err != nil {
		return "", cli.WithExitCode(fmt.Errorf("read password from stdin: %w", err), cli.ExitValidation)
	}
	password := strings.TrimRight(string(raw), "\r\n")
	if password == "" {
		return "", cli.WithExitCode(errors.New(
			"--password-stdin was given but stdin was empty; pipe the password in, or drop the flag to publish without one"),
			cli.ExitValidation)
	}
	return password, nil
}

// ── links ──────────────────────────────────────────────────────────────────

var pageLinksCmd = &cobra.Command{
	Use:   "links <slug>",
	Short: "List a page's public links, and say which of them still work",
	Long: `Show every public link ever minted for a page: when it expires,
whether it carries a password, whether provenance was opted back in, and
when it was last opened.

  crewship page links uzaverka

No link value is shown, here or anywhere else — the tokens are stored
hashed, so there is nothing to show. What is here is what you need to
decide which one to withdraw.

"last seen" is written at most once a day per link, which is also how
often the workspace journal records that the link was opened, and from
roughly where.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/public")
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
			return pageEmitMachine(f, raw, `{"tokens":[]}`)
		}
		var out pagePublicTokensJSON
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(out.Tokens) == 0 {
			fmt.Printf("Page %s has no public links.\n", args[0])
			fmt.Printf("Publish one with: crewship page publish %s\n", args[0])
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATUS\tEXPIRES\tPASSWORD\tPROVENANCE\tLAST SEEN\tPUBLISHED BY")
		for _, t := range out.Tokens {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				t.ID, pagePublicStatus(t), pagePublicWhen(t.ExpiresAt),
				pagePublicYesNo(t.HasPassword), pagePublicOnOff(t.ShowProvenance),
				pagePublicWhen(t.LastSeenAt), pageDash(t.CreatedBy))
		}
		return w.Flush()
	},
}

// pagePublicStatus is the verdict a person is actually looking for. `live` is
// the server's, computed from both columns and its own clock, so the CLI never
// disagrees with the thing enforcing it.
func pagePublicStatus(t pagePublicTokenJSON) string {
	switch {
	case t.RevokedAt != "":
		return "revoked"
	case t.Live:
		return "live"
	default:
		return "expired"
	}
}

// ── unpublish ──────────────────────────────────────────────────────────────

var pageUnpublishCmd = &cobra.Command{
	Use:   "unpublish <slug>",
	Short: "Withdraw one public link without touching the others",
	Long: `Revoke a single public link. The page and every other link on it are
untouched — that is why the links are individual: revoking the
accountant's must not break the client's.

  crewship page links uzaverka
  crewship page unpublish uzaverka --id cmsq1f... --yes

The row is marked revoked rather than deleted, so "was it opened after we
pulled it" stays answerable.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := mustFlagString(cmd, "id")
		if id == "" {
			return cli.WithExitCode(errors.New(
				"--id is required: run `crewship page links "+args[0]+"` to see which links exist"),
				cli.ExitValidation)
		}
		if err := confirmAction(cmd, fmt.Sprintf(
			"Withdraw public link %s on page %s? Anyone holding it loses access immediately.", id, args[0])); err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/public/" + pagePathEscape(id))
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
		fmt.Printf("Withdrew public link %s on %s.\n", id, args[0])
		return nil
	},
}

// ── Rendering helpers ──────────────────────────────────────────────────────

// pagePublicWhen prints an ABSOLUTE instant, never "in a while". §4 rule 3
// bans the vague phrasing for panel ages and the same argument holds here: an
// expiry a person has to compute is one they get wrong.
func pagePublicWhen(ts string) string {
	if strings.TrimSpace(ts) == "" {
		return "—"
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.Local().Format("2006-01-02 15:04")
		}
	}
	return ts
}

func pagePublicYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func pagePublicOnOff(b bool) string {
	if b {
		return "shown"
	}
	return "stripped"
}

func init() {
	pagePublishCmd.Flags().Int("expires-in-days", 0,
		"Days until the link stops working (default 30, maximum 365)")
	pagePublishCmd.Flags().Bool("password-stdin", false,
		"Read a password for this link from stdin (prompted on a terminal). A password is never taken from a flag value: argv is visible in `ps`")
	pagePublishCmd.Flags().Bool("show-provenance", false,
		"Include producer names and run ids on the public page (stripped by default)")

	pageUnpublishCmd.Flags().String("id", "", "The link to withdraw, from `crewship page links <slug>`")
	pageUnpublishCmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")

	pageCmd.AddCommand(pagePublishCmd)
	pageCmd.AddCommand(pageLinksCmd)
	pageCmd.AddCommand(pageUnpublishCmd)
}
