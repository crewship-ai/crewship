package main

import (
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Keep wire names explicit: adding a production capability requires an
// intentional fixture and acceptance-test decision rather than silently
// changing the powers of existing demo accounts.
var demoMatrixCapabilities = []string{
	"routine.run", "routine.create", "issue.create", "page.create",
	"skill.create", "credential.create", "credential.rotate",
	"credentials:reveal", "memory.write",
}

func demoCapabilityUsers() []demoUser {
	var users []demoUser
	for _, role := range []string{"VIEWER", "MEMBER", "MANAGER", "ADMIN"} {
		for _, capability := range append(slices.Clone(demoMatrixCapabilities), "all") {
			profile := strings.NewReplacer(".", "-", ":", "-").Replace(capability)
			caps := []string{"chat", capability}
			if capability == "all" {
				caps = append([]string{"chat"}, demoMatrixCapabilities...)
			}
			users = append(users, demoUser{
				Email:    strings.ToLower(role) + "-" + profile + "@crewship.local",
				FullName: "RBAC " + role + " · " + capability,
				Password: "rbac-demo-1234", Role: role, Capabilities: caps,
			})
		}
	}
	return users
}

func configureSeedCapabilities(client *cli.Client, wsID, userID string, expected []string, created bool) error {
	path := "/api/v1/workspaces/" + url.PathEscape(wsID) + "/members/" + url.PathEscape(userID) + "/capabilities"
	// Re-running the seed must never undo an operator's edits. Only a confirmed
	// newly provisioned account receives the named profile; existing accounts
	// are checked and report drift without changing any grant.
	if created {
		resp, err := client.Patch(path, map[string]any{"set": expected})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
	}
	resp, err := client.Get(path)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var result struct {
		Capabilities []string `json:"capabilities" yaml:"capabilities"`
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return err
	}
	want := slices.Clone(expected)
	slices.Sort(want)
	slices.Sort(result.Capabilities)
	if !slices.Equal(want, result.Capabilities) {
		return fmt.Errorf("capability profile differs from seed; existing grants preserved")
	}
	return nil
}

// This additive command can populate variants on an already edited demo
// workspace without reseeding crews or insisting on baseline-role defaults.
func newSeedRBACMatrixCmd() *cobra.Command {
	return &cobra.Command{Use: "rbac-matrix", Short: "Add 44 English RBAC demo accounts to an explicit workspace without resetting data", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(flagWorkspace) == "" {
			return fmt.Errorf("rbac-matrix requires an explicit --workspace ID")
		}
		if err := seedPreflightMode(cmd, true); err != nil {
			return err
		}
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		fmt.Fprintf(os.Stderr, "RBAC matrix target: %s, workspace %s\n", client.BaseURL, client.GetWorkspaceID())
		if err := seedUserRoster(cmd.Context(), client, append(append([]demoUser{}, demoUsers...), demoCapabilityUsers()...)); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "New variant accounts use rbac-demo-1234. Existing passwords and grants were preserved.")
		return nil
	}}
}

// requireSeedRBACOwner checks before provisioning any part of a roster that
// includes ADMIN profiles. The API still enforces the live grant boundary.
func requireSeedRBACOwner(client *cli.Client) error {
	response, err := client.Get("/api/v1/workspaces")
	if err != nil {
		return fmt.Errorf("verify RBAC roster owner: %w", err)
	}
	defer response.Body.Close()
	if err := cli.CheckError(response); err != nil {
		return err
	}
	var rows []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Role string `json:"currentUserRole"`
	}
	if err := cli.ReadJSON(response, &rows); err != nil {
		return err
	}
	target := client.GetWorkspaceID()
	for _, row := range rows {
		if (row.ID == target || row.Slug == target) && row.Role == "OWNER" {
			return nil
		}
	}
	return fmt.Errorf("RBAC demo roster requires workspace OWNER authority to provision ADMIN profiles; sign in as the workspace owner")
}
