package main

// `crewship page webhook create|list|revoke` — inbound panel webhooks (PRD
// docs/prd/pages.md §10b.5c).
//
// One command per endpoint, which is the repo rule:
//
//	POST   /api/v1/pages/{slug}/webhooks              page webhook create <slug> --panel <id>
//	GET    /api/v1/pages/{slug}/webhooks              page webhook list   <slug>
//	DELETE /api/v1/pages/{slug}/webhooks/{webhookId}  page webhook revoke <slug> --id <id>
//
// The fourth endpoint of this feature — POST /api/v1/page-webhooks/{token} — has
// no command and must not have one. It is the door for something that CANNOT
// run this binary: a cron on someone else's box, a Zapier step, a PLC gateway, a
// GitHub Action. Anything holding the crewship CLI already has `page set`, which
// is the single write path §11 names. So what `create` prints is a curl line the
// operator pastes somewhere else, not a command they run here.
//
// Three properties this file holds to, all copied from `page publish` because
// they are properties of the token and not of the surface:
//
//  1. It never asks the server for a token back. The token is printed once, by
//     create, out of the 201 — the column holds a SHA-256 digest and there is
//     nothing to re-read. `list` says which tokens exist and what losing one
//     costs; it cannot show you one again.
//
//  2. It never composes the inbound URL itself. The server returns the path
//     (internal/api/pages_webhooks.go PageWebhookPath) and this prints it
//     against the configured server, so a change to the route shape cannot
//     leave the CLI printing a URL that 404s.
//
//  3. It never decides anything. Whether a token may be issued at all — the
//     caller is human, owns the page, and could push that panel themselves — is
//     a server-side question (§10b.5c, §7.1b rule 1), and the CLI's job is to
//     carry the refusal through unedited.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// ── The wire (mirrors internal/api/pages_webhooks.go) ──────────────────────

type pageWebhookJSON struct {
	ID          string `json:"id"`
	Panel       string `json:"panel"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	URL         string `json:"url"`
	CreatedBy   string `json:"created_by"`
	CreatedAt   string `json:"created_at"`
	RevokedAt   string `json:"revoked_at"`
	LastFiredAt string `json:"last_fired_at"`
	FireCount   int64  `json:"fire_count"`
	Live        bool   `json:"live"`
}

type pageWebhooksJSON struct {
	Page     string            `json:"page"`
	Webhooks []pageWebhookJSON `json:"webhooks"`
}

type pageWebhookCreateBody struct {
	Panel string `json:"panel"`
	Name  string `json:"name,omitempty"`
}

// ── The parent noun ────────────────────────────────────────────────────────

var pageWebhookCmd = &cobra.Command{
	Use:   "webhook",
	Short: "Let something that cannot run this binary write one panel",
	Long: `A webhook token is a URL that writes exactly one panel.

It exists for the producer that cannot run the crewship CLI: a cron on
someone else's box, a Zapier step, a PLC gateway, a GitHub Action. Anything
that CAN run the CLI should use "crewship page set" instead — one write
path, provenance attached server-side, no credential to leak.

  crewship page webhook create uzaverka --panel cron --name "PLC hall 2"
  crewship page webhook list uzaverka
  crewship page webhook revoke uzaverka --id pgwh_... --yes

The token is a produce grant in a different coat, and it obeys every rule
that grant does:

  Issued only by a human. An agent may build the page and may not mint a
  credential that writes it from outside.

  Bound to one panel. Not one page — one panel. A leaked token writes that
  panel and nothing else, because the panel is not on the wire: there is no
  field in the request through which a holder could name another.

  Never more than its issuer. The token carries no authority of its own. On
  every request the server re-derives what the human who issued it may do
  RIGHT NOW, so a token stops working the moment they leave the workspace,
  leave the crew, or lose the grant — with no sweep and nobody having to
  remember the token exists.

  Rate limited per panel, and journalled. The same push limits as every
  other write path, and every write recorded with the token id as the actor
  so an operator can tell which token to revoke.`,
}

// ── create ─────────────────────────────────────────────────────────────────

var pageWebhookCreateCmd = &cobra.Command{
	Use:   "create <slug>",
	Short: "Mint a token that writes one panel, and print it once",
	Long: `Mint a webhook token for one panel and print the URL to POST to.

  crewship page webhook create uzaverka --panel cron
  crewship page webhook create uzaverka --panel cron --name "PLC hall 2"

The sender POSTs the panel's payload as the body, exactly as it would pass
it to "crewship page set" on stdin. There is no envelope: the token names
the workspace, the page and the panel, and provenance is attached
server-side — a body claiming produced_at or a producer name is refused,
not believed.

A producer that ran and failed says so on the query string:
POST .../page-webhooks/<token>?state=failed with whatever payload it has.
"fresh" and "stale" are the server's arithmetic and are not a sender's to
claim.

The token is printed ONCE, here. It is stored hashed, so nothing can show
it to you again — copy it into the sender's secret store now, or mint a
second one. Several tokens per panel is the intended shape: revoking the
PLC's must not break the GitHub Action's.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		panel := strings.TrimSpace(mustFlagString(cmd, "panel"))
		if panel == "" {
			return cli.WithExitCode(errors.New(
				"--panel is required: a webhook token is bound to exactly one panel, so there is no page-wide token to mint"),
				cli.ExitValidation)
		}
		body := pageWebhookCreateBody{Panel: panel, Name: strings.TrimSpace(mustFlagString(cmd, "name"))}

		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/webhooks", body)
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
		var wh pageWebhookJSON
		if err := json.Unmarshal(raw, &wh); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		url := strings.TrimSuffix(client.BaseURL, "/") + wh.URL
		fmt.Printf("Webhook on %s/%s.\n\n", args[0], pageDash(wh.Panel))
		fmt.Printf("  POST %s\n\n", url)
		fmt.Printf("  name       %s\n", pageDash(wh.Name))
		fmt.Printf("  issued by  %s\n", pageDash(wh.CreatedBy))
		fmt.Printf("  webhook id %s\n\n", wh.ID)
		fmt.Println("Paste this into the sender, for example:")
		fmt.Printf("  curl -X POST %s \\\n       -H 'Content-Type: application/json' \\\n       -d @payload.json\n\n", url)
		fmt.Println("This is the only time the token is shown — it is stored hashed.")
		fmt.Printf("Withdraw it with: crewship page webhook revoke %s --id %s --yes\n", args[0], wh.ID)
		return nil
	},
}

// ── list ───────────────────────────────────────────────────────────────────

var pageWebhookListCmd = &cobra.Command{
	Use:   "list <slug>",
	Short: "List a page's webhook tokens, and say which of them still work",
	Long: `Show every webhook token ever minted for a page: which panel it
writes, who issued it, when it last fired and how often.

  crewship page webhook list uzaverka

No token value is shown, here or anywhere else — they are stored hashed, so
there is nothing to show. What is here is what you need in order to decide
which one to withdraw.

A revoked token stays in the list on purpose: "was it used after we pulled
it" is the question an incident asks, and a deleted row cannot answer it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/webhooks")
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
			return pageEmitMachine(f, raw, `{"webhooks":[]}`)
		}
		var out pageWebhooksJSON
		if err := json.Unmarshal(raw, &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if len(out.Webhooks) == 0 {
			if f.Format != "quiet" {
				fmt.Printf("Page %s has no webhooks.\n", args[0])
				fmt.Printf("Mint one with: crewship page webhook create %s --panel <panel>\n", args[0])
			}
			return nil
		}
		rows := make([][]string, 0, len(out.Webhooks))
		for _, wh := range out.Webhooks {
			rows = append(rows, []string{
				wh.ID, pageDash(wh.Panel), pageWebhookStatus(wh), pageDash(wh.Name),
				pagePublicWhen(wh.LastFiredAt), fmt.Sprintf("%d", wh.FireCount), pageDash(wh.CreatedBy),
			})
		}
		f.Table([]string{"ID", "PANEL", "STATUS", "NAME", "LAST FIRED", "FIRES", "ISSUED BY"}, rows)
		return nil
	},
}

// pageWebhookStatus is the verdict a person is looking for. `live` is the
// server's, so the CLI never disagrees with the thing enforcing it.
func pageWebhookStatus(wh pageWebhookJSON) string {
	switch {
	case wh.RevokedAt != "":
		return "revoked"
	case wh.Live:
		return "live"
	default:
		return "inert"
	}
}

// ── revoke ─────────────────────────────────────────────────────────────────

var pageWebhookRevokeCmd = &cobra.Command{
	Use:   "revoke <slug>",
	Short: "Withdraw one webhook token without touching the others",
	Long: `Revoke a single webhook token. The page, the panel and every other
token on it are untouched — that is why the tokens are individual.

  crewship page webhook list uzaverka
  crewship page webhook revoke uzaverka --id pgwh_... --yes

It takes effect on the sender's very next request, not after a sweep or a
restart. The row is marked revoked rather than deleted, so "was it used
after we pulled it" stays answerable.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := strings.TrimSpace(mustFlagString(cmd, "id"))
		if id == "" {
			return cli.WithExitCode(errors.New(
				"--id is required: run `crewship page webhook list "+args[0]+"` to see which tokens exist"),
				cli.ExitValidation)
		}
		if err := confirmAction(cmd, fmt.Sprintf(
			"Withdraw webhook %s on page %s? Whatever is posting to it stops working immediately.", id, args[0])); err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/pages/" + pagePathEscape(args[0]) + "/webhooks/" + pagePathEscape(id))
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
		var out struct {
			Already bool `json:"already"`
		}
		_ = json.Unmarshal(raw, &out)
		if out.Already {
			fmt.Printf("Webhook %s on %s was already revoked.\n", id, args[0])
			return nil
		}
		fmt.Printf("Revoked webhook %s on %s. Anything still posting to it now gets 404.\n", id, args[0])
		return nil
	},
}

func init() {
	pageWebhookCreateCmd.Flags().String("panel", "",
		"The panel this token writes — one token, one panel, and it is not on the wire")
	pageWebhookCreateCmd.Flags().String("name", "",
		"A label for the list (\"PLC hall 2\"), so tokens can be told apart when one has to be revoked")

	pageWebhookRevokeCmd.Flags().String("id", "", "The token to withdraw, from `crewship page webhook list <slug>`")
	pageWebhookRevokeCmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")

	pageWebhookCmd.AddCommand(pageWebhookCreateCmd)
	pageWebhookCmd.AddCommand(pageWebhookListCmd)
	pageWebhookCmd.AddCommand(pageWebhookRevokeCmd)
	pageCmd.AddCommand(pageWebhookCmd)
}
