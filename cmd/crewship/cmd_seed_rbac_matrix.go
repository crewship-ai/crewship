package main

import (
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
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
	for _, role := range []string{"VIEWER", "MEMBER", "MANAGER"} {
		for _, capability := range append(slices.Clone(demoMatrixCapabilities), "all") {
			profile := strings.NewReplacer(".", "-", ":", "-").Replace(capability)
			caps := []string{"chat", capability}
			if capability == "all" {
				caps = append([]string{"chat"}, demoMatrixCapabilities...)
			}
			users = append(users, demoUser{
				Email: strings.ToLower(role) + "-" + profile + "@crewship.local",
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
	var result struct { Capabilities []string `json:"capabilities" yaml:"capabilities"` }
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
