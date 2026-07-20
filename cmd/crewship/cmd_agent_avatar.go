package main

import (
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// CLI parity for the persisted-avatar endpoints in
// internal/api/agents_avatar.go (#1297).
//
// An agent's avatar is drawn by a JS generator from (avatar_seed,
// avatar_style), which means the face depends on the installed library
// version — a dependency bump repaints the roster. The API stores the
// render so the face survives that. These commands are how you inspect,
// seed, and reset that stored render without a browser.

var agentAvatarCmd = &cobra.Command{
	Use:   "avatar",
	Short: "Inspect, set, or clear an agent's stored avatar",
	Long: `An agent's avatar is normally generated on the fly from its seed and
style, which makes it depend on the installed generator version. Once a
render is stored, that exact image is served instead, so the agent's face
stops changing when the generator is upgraded.

An agent with no stored render falls back to generating from the seed —
that is the pre-persistence behaviour and is not an error.`,
}

var agentAvatarShowCmd = &cobra.Command{
	Use:   "show <slug-or-id>",
	Short: "Write an agent's stored avatar SVG to stdout or a file",
	Long: `Fetch the stored avatar SVG. Exits with an error if the agent has no
stored render (it is still generating from the seed).

Examples:
  crewship agent avatar show my-agent
  crewship agent avatar show my-agent -o avatar.svg`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		// Not getJSON: this endpoint returns image/svg+xml, not JSON.
		resp, err := client.Get("/api/v1/agents/" + url.PathEscape(agentID) + "/avatar")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return fmt.Errorf("no stored avatar for %q (it generates from its seed): %w", args[0], err)
		}
		svg, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read avatar: %w", err)
		}

		out, _ := cmd.Flags().GetString("out")
		if out == "" {
			_, err := cmd.OutOrStdout().Write(svg)
			return err
		}
		if err := os.WriteFile(out, svg, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		cli.PrintSuccess(fmt.Sprintf("Wrote %d bytes to %s.", len(svg), out))
		return nil
	},
}

var agentAvatarSetCmd = &cobra.Command{
	Use:   "set <slug-or-id> -f <file.svg>",
	Short: "Store a rendered avatar SVG for an agent",
	Long: `Store an SVG as the agent's avatar. The server validates it against an
allowlist of inert drawing elements — anything that could load or execute
something (scripts, event handlers, external references) is refused.

Storing is write-once: an agent that already has a stored avatar returns a
conflict. Use "agent avatar clear" first to replace one.

Examples:
  crewship agent avatar set my-agent -f avatar.svg
  crewship agent avatar set my-agent -f -        # read from stdin`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required")
		}

		var svg []byte
		if file == "-" {
			if svg, err = io.ReadAll(cmd.InOrStdin()); err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
		} else if svg, err = os.ReadFile(file); err != nil {
			return fmt.Errorf("read %s: %w", file, err)
		}
		if len(svg) == 0 {
			return fmt.Errorf("avatar file is empty")
		}

		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		var result struct {
			AvatarURL string `json:"avatar_url"`
		}
		if err := putJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/avatar",
			map[string]any{"svg": string(svg)}, &result); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Stored avatar for %q (%d bytes).", args[0], len(svg)))
		return newFormatter().AutoDetail(result, [][]string{
			{"Agent", args[0]},
			{"Bytes", fmt.Sprintf("%d", len(svg))},
			{"Avatar URL", result.AvatarURL},
		})
	},
}

var agentAvatarClearCmd = &cobra.Command{
	Use:   "clear <slug-or-id>",
	Short: "Drop an agent's stored avatar, returning it to generate-from-seed",
	Long: `Remove the stored render. The agent goes back to being drawn from its
seed and style on every render, which also means the next generator
upgrade will change its face again.

Example:
  crewship agent avatar clear my-agent`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/avatar"); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Cleared stored avatar for %q; it now generates from its seed.", args[0]))
		return nil
	},
}

func init() {
	agentAvatarShowCmd.Flags().StringP("out", "o", "", "Write the SVG to this file instead of stdout")
	agentAvatarSetCmd.Flags().StringP("file", "f", "", "SVG file to store, or \"-\" for stdin")

	agentAvatarCmd.AddCommand(agentAvatarShowCmd)
	agentAvatarCmd.AddCommand(agentAvatarSetCmd)
	agentAvatarCmd.AddCommand(agentAvatarClearCmd)
	agentCmd.AddCommand(agentAvatarCmd)
}
