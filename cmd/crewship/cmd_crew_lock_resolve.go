package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	dockerprovider "github.com/crewship-ai/crewship/internal/provider/docker"
)

var crewLockResolveCmd = &cobra.Command{
	Use:   "lock-resolve <mise-config-file>",
	Short: "Propose a native mise lock using a local image or a provisioned crew",
	Long: `Resolve tools from a mise JSON/TOML file using a trusted local image ID.
The image must contain /usr/local/bin/mise. The builder accesses registries,
has no host mounts, and receives no config environment values or credentials.
Local mode does not contact Crewship, change a running crew, or install the result.
With --crew instead of --image, an OWNER/ADMIN asks the server to use that
crew's own provisioned immutable image. Only tools and lock are sent; local
environment values and tasks are never sent. Neither mode applies the result.
Output JSON includes the native version proposal, selector/lock hashes, lock,
resolver_image_id, platform and mise_version. These hashes are not authorization.
Pass --bump to update within the configured selectors; exact pins stay exact.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := readResolverConfig(args[0])
		if err != nil {
			return err
		}
		imageID, _ := cmd.Flags().GetString("image")
		crew, _ := cmd.Flags().GetString("crew")
		if (imageID == "") == (crew == "") {
			return fmt.Errorf("select exactly one of --image or --crew")
		}
		bump, _ := cmd.Flags().GetBool("bump")
		ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
		defer cancel()
		if crew != "" {
			client := newAPIClient().WithContext(ctx).WithTimeout(5 * time.Minute)
			id, err := resolveCrewID(client, crew)
			if err != nil {
				return err
			}
			clean := &devcontainer.MiseConfig{Tools: cfg.Tools, Lock: cfg.Lock}
			resp, err := client.Post("/api/v1/crews/"+id+"/provision/lock-resolve", map[string]any{"config": clean, "bump": bump})
			if err != nil {
				return err
			}
			if err := cli.CheckError(resp); err != nil {
				return err
			}
			defer resp.Body.Close()
			var result devcontainer.MiseResolution
			if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(result)
		}
		result, err := dockerprovider.ResolveMiseLock(ctx, imageID, cfg, bump)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	},
}

func readResolverConfig(name string) (*devcontainer.MiseConfig, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const max = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > max {
		return nil, fmt.Errorf("mise resolve: input exceeds 4 MiB")
	}
	cfg, err := devcontainer.ParseMiseConfig(string(raw))
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(cfg.Tools) == 0 {
		return nil, fmt.Errorf("mise resolve: tools are required")
	}
	return cfg, nil
}
func init() {
	crewLockResolveCmd.Flags().String("image", "", "Trusted local immutable Docker image ID containing mise")
	crewLockResolveCmd.Flags().String("crew", "", "Resolve remotely in this provisioned crew's owned image (OWNER/ADMIN)")
	crewLockResolveCmd.Flags().Bool("bump", false, "Update existing lock entries within configured selectors")
	crewProvisionCmd.AddCommand(crewLockResolveCmd)
}
