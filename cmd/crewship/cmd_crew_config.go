package main

import (
	"encoding/json"
	"fmt"
	"os"

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

		// Mutual exclusion: show / export / clear / set are mutually exclusive.
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
		if modeCount == 0 {
			return fmt.Errorf("specify one of --show, --export, --clear, or a set flag (--devcontainer, --mise, --runtime-image)")
		}
		if modeCount > 1 {
			return fmt.Errorf("--show, --export, --clear, and set flags (--devcontainer/--mise/--runtime-image) are mutually exclusive")
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

func readConfigFile(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("file not found: %s", path)
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

	crewCmd.AddCommand(crewConfigCmd)
}
