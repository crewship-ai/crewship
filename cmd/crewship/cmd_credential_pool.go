package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/providerlogin"
)

type poolMemberOut struct {
	CredentialID string `json:"credential_id"`
	Priority     int    `json:"priority"`
}

type poolDefinitionOut struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Provider        string          `json:"provider"`
	Mode            string          `json:"mode"`
	AllowCrossOwner bool            `json:"allow_cross_owner"`
	CreatedBy       *string         `json:"created_by"`
	MemberCount     int             `json:"member_count"`
	Members         []poolMemberOut `json:"members,omitempty"`
}

func poolDefinitionDetail(out poolDefinitionOut) error {
	pairs := [][]string{{"ID", out.ID}, {"Name", out.Name}, {"Provider", out.Provider}, {"Mode", out.Mode}, {"Cross-owner consent", strconv.FormatBool(out.AllowCrossOwner)}, {"Accounts", strconv.Itoa(out.MemberCount)}, {"Delivery", "Not assigned: pool definitions do not grant access"}}
	for _, member := range out.Members {
		pairs = append(pairs, []string{"Account", fmt.Sprintf("%s (priority %d)", member.CredentialID, member.Priority)})
	}
	return newFormatter().AutoDetail(out, pairs)
}

// Build a fresh command tree for tests; never execute the shared root in
// parallel. These commands only address administrative definitions.
func newCredentialPoolCmd() *cobra.Command {
	root := &cobra.Command{Use: "pool", Short: "Manage provider account sets (owner/admin only)", Long: "Manage provider account set definitions. Creating a pool does not assign it to an agent or enable failover."}
	check := func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		return requireWorkspace()
	}
	list := &cobra.Command{Use: "list", Short: "List one page of provider pools", Args: cobra.NoArgs, PreRunE: check, RunE: func(cmd *cobra.Command, args []string) error {
		path := "/api/v1/provider-logins/pools"
		if after := mustFlagString(cmd, "after"); after != "" {
			path += "?" + url.Values{"after": {after}}.Encode()
		}
		resp, err := newAPIClient().WithContext(cmd.Context()).Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var page struct {
			Items      []poolDefinitionOut `json:"items"`
			NextCursor *string             `json:"next_cursor"`
		}
		if err := cli.ReadJSON(resp, &page); err != nil {
			return err
		}
		f := newFormatter()
		return f.AutoHuman(page, func() {
			rows := make([][]string, 0, len(page.Items))
			for _, p := range page.Items {
				rows = append(rows, []string{p.ID, p.Name, p.Provider, p.Mode, strconv.Itoa(p.MemberCount)})
			}
			f.Table([]string{"ID", "NAME", "PROVIDER", "MODE", "ACCOUNTS"}, rows)
			if page.NextCursor != nil {
				fmt.Printf("Next page: credential pool list --after %s\n", *page.NextCursor)
			}
		})
	}}
	list.Flags().String("after", "", "Cursor from the previous page")
	get := &cobra.Command{Use: "get <pool-id>", Short: "Inspect a pool definition without selecting an account", Args: cobra.ExactArgs(1), PreRunE: check, RunE: func(cmd *cobra.Command, args []string) error {
		id := strings.TrimSpace(args[0])
		if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") {
			return cli.WithExitCode(fmt.Errorf("invalid pool ID"), cli.ExitValidation)
		}
		resp, err := newAPIClient().WithContext(cmd.Context()).Get("/api/v1/provider-logins/pools/" + url.PathEscape(id))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out poolDefinitionOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return poolDefinitionDetail(out)
	}}
	create := &cobra.Command{Use: "create --name <name> --provider <provider> --mode <mode> --member <credential-id>[=priority]", Short: "Create an account set without granting access", Args: cobra.NoArgs, PreRunE: check, RunE: func(cmd *cobra.Command, args []string) error {
		name := strings.TrimSpace(mustFlagString(cmd, "name"))
		provider := providerlogin.Canonical(mustFlagString(cmd, "provider"))
		mode := mustFlagString(cmd, "mode")
		members, _ := cmd.Flags().GetStringArray("member")
		consent, _ := cmd.Flags().GetBool("allow-cross-owner")
		if name == "" || !providerlogin.IsProvider(provider) || !providerlogin.ValidMode(mode) || len(members) == 0 || len(members) > 100 {
			return cli.WithExitCode(fmt.Errorf("provide --name, valid --provider, --mode subscription|api_key and 1–100 --member IDs"), cli.ExitValidation)
		}
		parsed := make([]poolMemberOut, 0, len(members))
		seen := map[string]bool{}
		for _, member := range members {
			id, rank, has := strings.Cut(member, "=")
			id = strings.TrimSpace(id)
			priority := 0
			if has {
				var err error
				priority, err = strconv.Atoi(rank)
				if err != nil {
					return cli.WithExitCode(fmt.Errorf("member priority must be an integer"), cli.ExitValidation)
				}
			}
			if id == "" || seen[id] {
				return cli.WithExitCode(fmt.Errorf("member IDs must be nonempty and unique"), cli.ExitValidation)
			}
			seen[id] = true
			parsed = append(parsed, poolMemberOut{id, priority})
		}
		body := map[string]any{"name": name, "provider": provider, "mode": mode, "allow_cross_owner": consent, "members": parsed}
		resp, err := newAPIClient().WithContext(cmd.Context()).Post("/api/v1/provider-logins/pools", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out poolDefinitionOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return poolDefinitionDetail(out)
	}}
	create.Flags().String("name", "", "Pool display name")
	create.Flags().String("provider", "", "Model provider, for example OPENAI")
	create.Flags().String("mode", "", "subscription or api_key (must match every account)")
	create.Flags().StringArray("member", nil, "Credential ID, optionally =priority; repeat for each account (lowest priority first)")
	create.Flags().Bool("allow-cross-owner", false, "Explicitly consent to pooling accounts owned by different people; provider terms still apply")
	root.AddCommand(list, get, create)
	return root
}

func init() { credentialCmd.AddCommand(newCredentialPoolCmd()) }
