package main

// CLI parity for the link-first Git integration (see
// internal/api/issue_code_links.go). Every /api/v1 route there has a command
// here — that is the repo rule, and it is also the point: the CLI is how an
// agent drives Crewship, and an agent that has just opened a pull request is
// the caller most likely to want to attach it.
//
//	crewship issue links   ENG-4
//	crewship issue link    ENG-4 https://github.com/acme/thing/pull/7
//	crewship issue relink  ENG-4 <link-id>
//	crewship issue unlink  ENG-4 <link-id>

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// codeLinkItem mirrors the API's codeLinkResponse. Only the fields the CLI
// renders are declared; --output json prints the decoded struct, so anything
// added server-side that matters here needs a field here too.
type codeLinkItem struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Host     string `json:"host"`
	Owner    string `json:"owner"`
	Repo     string `json:"repo"`
	Number   int    `json:"number"`
	URL      string `json:"url"`

	Title        *string `json:"title"`
	State        *string `json:"state"`
	Author       *string `json:"author"`
	SourceBranch *string `json:"source_branch"`
	TargetBranch *string `json:"target_branch"`

	LastSyncedAt  *string `json:"last_synced_at"`
	LastSyncError *string `json:"last_sync_error"`
}

// The four paths below are written out with fmt.Sprintf at each call site
// rather than built by a shared helper. That is deliberate: the CLI↔route
// contract test (cli_route_contract_test.go) renders a call's path argument
// statically and only checks the ones that resolve to a literal starting with
// "/api/", so a helper would render as an opaque "{}" and these commands would
// be dropped from the check SILENTLY — passing without ever proving the routes
// exist. The mild repetition buys real static verification.

// resolveIssueForLinks does the auth + workspace + identifier dance every
// command below shares.
func resolveIssueForLinks(identifier string) (*cli.Client, string, string, error) {
	if err := requireAuth(); err != nil {
		return nil, "", "", err
	}
	if err := requireWorkspace(); err != nil {
		return nil, "", "", err
	}
	client := newAPIClient()
	issue, err := fetchIssue(client, identifier)
	if err != nil {
		return nil, "", "", err
	}
	return client, issue.CrewID, derefStr(issue.Identifier, issue.ID), nil
}

var issueLinksCmd = &cobra.Command{
	Use:     "links <identifier>",
	Aliases: []string{"code-links", "prs"},
	Short:   "List the pull requests linked to an issue",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/code-links",
			crewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var links []codeLinkItem
		if err := cli.ReadJSON(resp, &links); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "STATE", "REF", "TITLE", "BRANCH", "SYNCED"}
		rows := make([][]string, 0, len(links))
		for _, l := range links {
			state := derefStr(l.State, "UNKNOWN")
			if l.LastSyncError != nil && *l.LastSyncError != "" {
				// A link whose refresh is failing is showing STALE data;
				// saying so in the state column is the difference between
				// "merged" and "was merged the last time we could look".
				state += " (stale)"
			}
			branch := "—"
			if l.SourceBranch != nil && l.TargetBranch != nil {
				branch = *l.SourceBranch + " → " + *l.TargetBranch
			}
			rows = append(rows, []string{
				truncateID(l.ID, 12),
				state,
				fmt.Sprintf("%s/%s#%d", l.Owner, l.Repo, l.Number),
				// A pull-request title is written by whoever opened it.
				// Strip control bytes before printing so it cannot repaint
				// the terminal.
				truncateStr(strings.ReplaceAll(sanitizeTerminal(derefStr(l.Title, "")), "\n", " "), 44),
				branch,
				derefStr(l.LastSyncedAt, "never"),
			})
		}
		return f.Auto(links, headers, rows)
	},
}

var issueLinkCmd = &cobra.Command{
	Use:   "link <identifier> <pull-request-url>",
	Short: "Attach a pull request / merge request to an issue",
	Long: `Attach a GitHub pull request or GitLab merge request to an issue.

The provider is recognised from the URL, and its state is fetched through the
provider's API using a stored credential in this workspace. For a self-hosted
GitHub or GitLab, label the credential with the forge's host (its "account
label") so it is matched by host.

Examples:
  crewship issue link ENG-4 https://github.com/acme/thing/pull/7
  crewship issue link ENG-4 https://gitlab.com/acme/thing/-/merge_requests/7
  crewship issue link ENG-4 https://ghe.acme.internal/platform/gw/pull/12
`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/code-links",
			crewID, url.PathEscape(identifier)),
			map[string]interface{}{"url": args[1]})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var link codeLinkItem
		if err := cli.ReadJSON(resp, &link); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Linked %s/%s#%d (%s) to %s.",
			link.Owner, link.Repo, link.Number, derefStr(link.State, "UNKNOWN"), identifier))
		return nil
	},
}

var issueRelinkCmd = &cobra.Command{
	Use:     "relink <identifier> <link-id>",
	Aliases: []string{"refresh-link"},
	Short:   "Re-read a linked pull request's state from its provider",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues/%s/code-links/%s/refresh",
			crewID, url.PathEscape(identifier), url.PathEscape(args[1])), nil)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var link codeLinkItem
		if err := cli.ReadJSON(resp, &link); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("%s/%s#%d is %s.",
			link.Owner, link.Repo, link.Number, derefStr(link.State, "UNKNOWN")))
		return nil
	},
}

var issueUnlinkCmd = &cobra.Command{
	Use:   "unlink <identifier> <link-id>",
	Short: "Remove a pull-request link from an issue",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s/code-links/%s",
			crewID, url.PathEscape(identifier), url.PathEscape(args[1])))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Link removed.")
		return nil
	},
}

func init() {
	issueCmd.AddCommand(issueLinksCmd)
	issueCmd.AddCommand(issueLinkCmd)
	issueCmd.AddCommand(issueRelinkCmd)
	issueCmd.AddCommand(issueUnlinkCmd)
}
