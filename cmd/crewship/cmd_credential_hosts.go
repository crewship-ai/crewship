package main

// `crewship credential hosts` — show or set the human-approved hosts of a
// credential (#5045).
//
// The list decides whether the credential is DELIVERED: a credential with
// approved hosts is withheld unless the agent's crew is restricted and its
// egress allowlist permits every host. It does not control where a delivered
// token can be sent; the output says so every time.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type credentialHostsOut struct {
	CredentialID string   `json:"credential_id" yaml:"credential_id"`
	AllowedHosts []string `json:"allowed_hosts" yaml:"allowed_hosts"`
	Previous     []string `json:"previous,omitempty" yaml:"previous,omitempty"`
	Note         string   `json:"note" yaml:"note"`
}

var (
	credHostsSet   []string
	credHostsClear bool
)

var credHostsCmd = &cobra.Command{
	Use:   "hosts <name-or-id>",
	Short: "Show or set the human-approved hosts of a credential",
	Long: `Show or set the hosts a person has approved for a credential.

With approved hosts, the credential is delivered to an agent only when the
agent's crew is in restricted network mode and the crew's egress allowlist
permits every approved host. Otherwise it is withheld, and 'crewship agent
credential-readiness' says why. A credential with no approved hosts is
delivered as before.

This is a delivery precondition. It does NOT stop an agent that holds the token
from sending it to any other host the crew's allowlist permits.

Only a signed-in OWNER/ADMIN (or an unrestricted operator token) can set hosts.
Hosts an agent proposes in a credential request are suggestions only; nothing
applies them until a person runs this command. Hosts are hostnames or
*.example.com wildcards; case, trailing dots and IDN spellings are normalised.`,
	Example: `  crewship credential hosts gh-token
  crewship credential hosts gh-token --set api.github.com,github.com
  crewship credential hosts gh-token --clear`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		setting := cmd.Flags().Changed("set")
		if setting && credHostsClear {
			return fmt.Errorf("use either --set or --clear, not both")
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		credID, err := resolveCredentialID(client, args[0])
		if err != nil {
			return err
		}
		path := "/api/v1/credentials/" + credID + "/allowed-hosts"
		var out credentialHostsOut
		if setting || credHostsClear {
			hosts := []string{}
			if setting {
				hosts = append(hosts, credHostsSet...)
			}
			r, err := client.Put(path, map[string]any{"allowed_hosts": hosts})
			if err != nil {
				return err
			}
			if err := cli.CheckError(r); err != nil {
				return err
			}
			if err := cli.ReadJSON(r, &out); err != nil {
				return err
			}
		} else {
			r, err := client.Get(path)
			if err != nil {
				return err
			}
			if err := cli.CheckError(r); err != nil {
				return err
			}
			if err := cli.ReadJSON(r, &out); err != nil {
				return err
			}
		}
		if out.AllowedHosts == nil {
			out.AllowedHosts = []string{}
		}
		f := newFormatter()
		return f.AutoHuman(out, func() {
			if len(out.AllowedHosts) == 0 {
				fmt.Println("No approved hosts: the credential is delivered as usual.")
			} else {
				fmt.Printf("Approved hosts: %s\n", strings.Join(out.AllowedHosts, ", "))
			}
			fmt.Printf("\n%s\n", out.Note)
		})
	},
}

func init() {
	credHostsCmd.Flags().StringSliceVar(&credHostsSet, "set", nil, "Replace the approved hosts (comma-separated)")
	credHostsCmd.Flags().BoolVar(&credHostsClear, "clear", false, "Remove all approved hosts")
	credentialCmd.AddCommand(credHostsCmd)
}
