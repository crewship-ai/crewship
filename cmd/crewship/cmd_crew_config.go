package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewConfigCmd = &cobra.Command{
	Use:   "config <slug-or-id>",
	Short: "Manage runtime configuration (devcontainer, mise, runtime_image) for a crew",
	Long: `Manage runtime configuration for a crew.

Examples:
  crewship crew config my-crew --show
  crewship crew config my-crew --devcontainer ./devcontainer.json
  crewship crew config my-crew --mise ./mise.json
  crewship crew config my-crew --runtime-image debian:bookworm-slim
  crewship crew config my-crew --export
  crewship crew config my-crew --clear`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		show, _ := cmd.Flags().GetBool("show")
		exportJSON, _ := cmd.Flags().GetBool("export")
		clear, _ := cmd.Flags().GetBool("clear")
		devcontainerPath, _ := cmd.Flags().GetString("devcontainer")
		misePath, _ := cmd.Flags().GetString("mise")
		runtimeImage, _ := cmd.Flags().GetString("runtime-image")

		hasSet := devcontainerPath != "" || misePath != "" || runtimeImage != ""

		// #1380: security knobs merge onto the crew's stored devcontainer_config
		// and are server-validated on PATCH (privileged → 403 without the
		// workspace flag; disallowed cap → 400). They form their own set mode so
		// they never clobber image/features on the round-trip.
		privileged, _ := cmd.Flags().GetBool("privileged")
		initFlag, _ := cmd.Flags().GetBool("init")
		capAdd, _ := cmd.Flags().GetStringSlice("cap-add")
		secChanged := cmd.Flags().Changed("privileged") ||
			cmd.Flags().Changed("init") || cmd.Flags().Changed("cap-add")

		// Mutual exclusion: show / export / clear / set / security-set.
		modeCount := 0
		if show {
			modeCount++
		}
		if exportJSON {
			modeCount++
		}
		if clear {
			modeCount++
		}
		if hasSet {
			modeCount++
		}
		if secChanged {
			modeCount++
		}
		if modeCount == 0 {
			return fmt.Errorf("specify one of --show, --export, --clear, a set flag (--devcontainer, --mise, --runtime-image), or a security flag (--privileged, --cap-add, --init)")
		}
		if modeCount > 1 {
			return fmt.Errorf("--show, --export, --clear, set flags (--devcontainer/--mise/--runtime-image), and security flags (--privileged/--cap-add/--init) are mutually exclusive")
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		switch {
		case show:
			return showCrewConfig(client, crewID)
		case exportJSON:
			return exportCrewConfig(client, crewID)
		case clear:
			return clearCrewConfig(client, crewID)
		case secChanged:
			return setCrewSecurity(client, crewID,
				cmd.Flags().Changed("privileged"), privileged,
				cmd.Flags().Changed("init"), initFlag,
				cmd.Flags().Changed("cap-add"), capAdd)
		default:
			return setCrewConfig(client, crewID, devcontainerPath, misePath, runtimeImage)
		}
	},
}

// crewInfo is a minimal projection of GET /api/v1/crews/{id} fields we need.
type crewInfo struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Slug               string  `json:"slug"`
	RuntimeImage       *string `json:"runtime_image"`
	DevcontainerConfig *string `json:"devcontainer_config"`
	MiseConfig         *string `json:"mise_config"`
	CachedImage        *string `json:"cached_image"`
	ConfigHash         *string `json:"config_hash"`
}

// provisionStatus is a minimal projection of GET /api/v1/crews/{id}/provision.
type provisionStatus struct {
	Status             string  `json:"status"`
	CachedImage        *string `json:"cached_image"`
	ConfigHash         *string `json:"config_hash"`
	DevcontainerConfig *string `json:"devcontainer_config"`
	MiseConfig         *string `json:"mise_config"`
	Error              string  `json:"error"`
}

func fetchCrewInfo(client *cli.Client, crewID string) (*crewInfo, error) {
	resp, err := client.Get("/api/v1/crews/" + crewID)
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var info crewInfo
	if err := cli.ReadJSON(resp, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func fetchProvisionStatus(client *cli.Client, crewID string) (*provisionStatus, error) {
	resp, err := client.Get("/api/v1/crews/" + crewID + "/provision")
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var st provisionStatus
	if err := cli.ReadJSON(resp, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func prettyJSON(s string) string {
	if s == "" {
		return "-"
	}
	var tmp interface{}
	if err := json.Unmarshal([]byte(s), &tmp); err != nil {
		return s
	}
	out, err := json.MarshalIndent(tmp, "", "  ")
	if err != nil {
		return s
	}
	return string(out)
}

func derefOrDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

func showCrewConfig(client *cli.Client, crewID string) error {
	info, err := fetchCrewInfo(client, crewID)
	if err != nil {
		return err
	}
	status, err := fetchProvisionStatus(client, crewID)
	if err != nil {
		return err
	}

	devStr := ""
	if info.DevcontainerConfig != nil {
		devStr = *info.DevcontainerConfig
	}
	miseStr := ""
	if info.MiseConfig != nil {
		miseStr = *info.MiseConfig
	}

	fmt.Printf("Name:          %s\n", info.Name)
	fmt.Printf("Slug:          %s\n", info.Slug)
	fmt.Printf("Runtime Image: %s\n", derefOrDash(info.RuntimeImage))
	fmt.Printf("Cached Image:  %s\n", derefOrDash(info.CachedImage))
	fmt.Printf("Config Hash:   %s\n", derefOrDash(info.ConfigHash))
	fmt.Printf("Status:        %s\n", status.Status)
	fmt.Println()
	fmt.Println("Devcontainer Config:")
	fmt.Println(prettyJSON(devStr))
	fmt.Println()
	fmt.Println("Mise Config:")
	fmt.Println(prettyJSON(miseStr))
	return nil
}

func exportCrewConfig(client *cli.Client, crewID string) error {
	info, err := fetchCrewInfo(client, crewID)
	if err != nil {
		return err
	}

	out := map[string]interface{}{
		"runtime_image":       nil,
		"devcontainer_config": nil,
		"mise_config":         nil,
	}
	if info.RuntimeImage != nil && *info.RuntimeImage != "" {
		out["runtime_image"] = *info.RuntimeImage
	}
	if info.DevcontainerConfig != nil && *info.DevcontainerConfig != "" {
		// Embed parsed JSON if valid, else raw string.
		var parsed interface{}
		if err := json.Unmarshal([]byte(*info.DevcontainerConfig), &parsed); err == nil {
			out["devcontainer_config"] = parsed
		} else {
			out["devcontainer_config"] = *info.DevcontainerConfig
		}
	}
	if info.MiseConfig != nil && *info.MiseConfig != "" {
		var parsed interface{}
		if err := json.Unmarshal([]byte(*info.MiseConfig), &parsed); err == nil {
			out["mise_config"] = parsed
		} else {
			out["mise_config"] = *info.MiseConfig
		}
	}

	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode export: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func clearCrewConfig(client *cli.Client, crewID string) error {
	empty := ""
	body := map[string]interface{}{
		"devcontainer_config": &empty,
		"mise_config":         &empty,
		"runtime_image":       &empty,
	}
	return patchCrew(client, crewID, body, "Runtime configuration cleared.")
}

func setCrewConfig(client *cli.Client, crewID, devcontainerPath, misePath, runtimeImage string) error {
	body := map[string]interface{}{}

	if devcontainerPath != "" {
		data, err := readConfigFile(devcontainerPath)
		if err != nil {
			return err
		}
		body["devcontainer_config"] = data
	}
	if misePath != "" {
		data, err := readConfigFile(misePath)
		if err != nil {
			return err
		}
		body["mise_config"] = data
	}
	if runtimeImage != "" {
		body["runtime_image"] = runtimeImage
	}

	return patchCrew(client, crewID, body, "Crew configuration updated.")
}

// setCrewSecurity merges the #1380 container-privilege knobs (privileged / init
// / capAdd) onto the crew's stored devcontainer_config and PATCHes it back. The
// keys are top-level devcontainer.json fields the runtime honours; the server
// re-validates them on write (privileged requires the workspace
// allow_privileged_credentials flag → 403; capAdd is bounded to the
// NET_BIND_SERVICE allowlist → 400). We GET-merge rather than replace so image
// / features / mise stay intact.
func setCrewSecurity(client *cli.Client, crewID string,
	setPriv, privileged, setInit, initFlag, setCap bool, capAdd []string) error {
	info, err := fetchCrewInfo(client, crewID)
	if err != nil {
		return err
	}
	if info.DevcontainerConfig == nil || *info.DevcontainerConfig == "" {
		return fmt.Errorf("crew has no devcontainer_config; set a base config with --devcontainer <file> before toggling privilege controls")
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal([]byte(*info.DevcontainerConfig), &cfg); err != nil {
		return fmt.Errorf("stored devcontainer_config is not valid JSON: %w", err)
	}

	if setPriv {
		if privileged {
			cfg["privileged"] = true
		} else {
			delete(cfg, "privileged")
		}
	}
	if setInit {
		if initFlag {
			cfg["init"] = true
		} else {
			delete(cfg, "init")
		}
	}
	if setCap {
		// Normalize CAP_ prefix / case so "cap_net_bind_service" and
		// "NET_BIND_SERVICE" both land as the canonical Docker cap name the
		// server allowlist compares against.
		if len(capAdd) == 0 {
			delete(cfg, "capAdd")
		} else {
			norm := make([]string, 0, len(capAdd))
			for _, c := range capAdd {
				norm = append(norm, normalizeCapCLI(c))
			}
			cfg["capAdd"] = norm
		}
	}

	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode devcontainer_config: %w", err)
	}
	s := string(encoded)
	body := map[string]interface{}{"devcontainer_config": &s}
	return patchCrew(client, crewID, body, "Crew privilege controls updated.")
}

// normalizeCapCLI upper-cases and strips a leading CAP_ from a capability name.
func normalizeCapCLI(raw string) string {
	up := strings.ToUpper(strings.TrimSpace(raw))
	return strings.TrimPrefix(up, "CAP_")
}

func readConfigFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", cli.NotFoundf("file not found: %s", path)
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

func patchCrew(client *cli.Client, crewID string, body map[string]interface{}, successMsg string) error {
	resp, err := client.Patch("/api/v1/crews/"+crewID, body)
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	resp.Body.Close()
	cli.PrintSuccess(successMsg)
	return nil
}

func init() {
	crewConfigCmd.Flags().Bool("show", false, "Pretty-print current runtime configuration")
	crewConfigCmd.Flags().Bool("export", false, "Export runtime configuration as JSON to stdout")
	crewConfigCmd.Flags().Bool("clear", false, "Clear all runtime configuration (sets to NULL)")
	crewConfigCmd.Flags().String("devcontainer", "", "Path to devcontainer.json file to upload")
	crewConfigCmd.Flags().String("mise", "", "Path to mise config JSON file to upload")
	crewConfigCmd.Flags().String("runtime-image", "", "Custom base image reference (e.g. debian:bookworm-slim)")
	crewConfigCmd.Flags().Bool("privileged", false, "Run the crew container privileged (server-validated: requires the workspace allow_privileged_credentials flag)")
	crewConfigCmd.Flags().Bool("init", false, "Run a docker --init reaper (PID 1) inside the crew container")
	crewConfigCmd.Flags().StringSlice("cap-add", nil, "Linux capabilities to add (server-validated against the NET_BIND_SERVICE allowlist)")

	crewCmd.AddCommand(crewConfigCmd)
}
