package main

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// validateCrewFlags rejects negative resource limits and unknown network modes.
func validateCrewFlags(memoryMB int, cpus float64, ttl int, ttlSet bool, networkMode string) error {
	if memoryMB < 0 {
		return fmt.Errorf("--memory-mb must be >= 0")
	}
	if cpus < 0 {
		return fmt.Errorf("--cpus must be >= 0")
	}
	if ttlSet && ttl < 0 {
		return fmt.Errorf("--ttl must be >= 0")
	}
	if networkMode != "" && networkMode != "free" && networkMode != "restricted" {
		return fmt.Errorf("--network-mode must be one of: free, restricted")
	}
	return nil
}

// sanitizeTerminal strips control characters (ANSI escapes, OSC links,
// CR) from untrusted strings before printing them to the terminal —
// agents have no legitimate need to drive the terminal, and a
// malicious tool result could otherwise rewrite the user's scrollback.
//
// The explicit strings.ReplaceAll passes are functionally redundant
// with the strings.Map below (it already strips control chars including
// CR), but they're the form CodeQL's go/log-injection rule recognises
// as a log-injection sanitiser. Without them, CodeQL still flags every
// fmt.Print site downstream because strings.Map(fn, s) isn't on its
// sanitiser allowlist. Newlines stay preserved for the streaming path.
func sanitizeTerminal(s string) string {
	// Explicit CR strip + control-char stripper — CodeQL sees the
	// ReplaceAll as a recognised sanitiser hook for go/log-injection.
	s = strings.ReplaceAll(s, "\r", "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, s)
}

// deriveSlugFromName makes a best-effort kebab-case slug from a human
// name when the operator omitted --slug. The server's slug validator is
// `^[a-z0-9][a-z0-9_-]*$` length 2-50 (helpers.go:validSlugFormat);
// emoji / accented / cyrillic letters from a name like "Engineering 🛠"
// must be stripped before the slug ever reaches the wire. Returns ""
// when no valid first character can be derived (caller falls back to
// requiring --slug explicitly).
func deriveSlugFromName(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	prevHyphen := true // suppress leading hyphen
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		case r == '_':
			b.WriteRune('_')
			prevHyphen = false
		case r == '-' || r == ' ' || r == '\t' || r == '/' || r == '.':
			if !prevHyphen {
				b.WriteRune('-')
				prevHyphen = true
			}
		default:
			// strip other characters (emoji, accented letters, punctuation)
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return ""
	}
	if len(out) > 50 {
		out = strings.TrimRight(out[:50], "-_")
	}
	return out
}

var crewCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new crew",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		flags := cmd.Flags()
		name, _ := flags.GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}

		body := map[string]interface{}{"name": name}
		slug, _ := flags.GetString("slug")
		if slug == "" {
			// Auto-derive from name when omitted — the --slug flag's help
			// text promises this ("auto from name") but pre-fix the
			// derivation never ran, so `--name Engineering` reached the
			// server with slug="" and the slug-length validator returned
			// 400 "slug must be 2-50 characters". (Issue #533.)
			slug = deriveSlugFromName(name)
		}
		if slug != "" {
			body["slug"] = slug
		}
		if v, _ := flags.GetString("description"); v != "" {
			body["description"] = v
		}
		if v, _ := flags.GetString("color"); v != "" {
			body["color"] = v
		}
		if v, _ := flags.GetString("icon"); v != "" {
			body["icon"] = v
		}
		memoryMB, _ := flags.GetInt("memory-mb")
		cpus, _ := flags.GetFloat64("cpus")
		ttl, _ := flags.GetInt("ttl")
		networkMode, _ := flags.GetString("network-mode")
		if err := validateCrewFlags(memoryMB, cpus, ttl, flags.Changed("ttl"), networkMode); err != nil {
			return err
		}
		if memoryMB > 0 {
			body["container_memory_mb"] = memoryMB
		}
		if cpus > 0 {
			body["container_cpus"] = cpus
		}
		if ttl > 0 {
			body["container_ttl_hours"] = ttl
		}
		if networkMode != "" {
			body["network_mode"] = networkMode
		}
		if v, _ := flags.GetString("allowed-domains"); v != "" {
			domains := strings.Split(v, ",")
			trimmed := make([]string, 0, len(domains))
			for _, d := range domains {
				d = strings.TrimSpace(d)
				if d != "" {
					trimmed = append(trimmed, d)
				}
			}
			body["allowed_domains"] = trimmed
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/crews", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Crew created: %s (%s)", created.Slug, created.ID))
		return nil
	},
}

var crewUpdateCmd = &cobra.Command{
	Use:   "update <slug-or-id>",
	Short: "Update a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()

		// Validate resource flags up-front so bad input fails fast.
		memoryMB, _ := flags.GetInt("memory-mb")
		cpus, _ := flags.GetFloat64("cpus")
		ttl, _ := flags.GetInt("ttl")
		networkMode, _ := flags.GetString("network-mode")
		if err := validateCrewFlags(memoryMB, cpus, ttl, flags.Changed("ttl"), networkMode); err != nil {
			return err
		}

		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("description") {
			v, _ := flags.GetString("description")
			body["description"] = v
		}
		if flags.Changed("color") {
			v, _ := flags.GetString("color")
			body["color"] = v
		}
		if flags.Changed("icon") {
			v, _ := flags.GetString("icon")
			body["icon"] = v
		}
		if flags.Changed("memory-mb") {
			v, _ := flags.GetInt("memory-mb")
			body["container_memory_mb"] = v
		}
		if flags.Changed("cpus") {
			v, _ := flags.GetFloat64("cpus")
			body["container_cpus"] = v
		}
		if flags.Changed("ttl") {
			v, _ := flags.GetInt("ttl")
			body["container_ttl_hours"] = v // 0 = clear TTL on server side
		}
		if flags.Changed("network-mode") {
			v, _ := flags.GetString("network-mode")
			body["network_mode"] = v
		}
		if flags.Changed("allowed-domains") {
			v, _ := flags.GetString("allowed-domains")
			if v == "" {
				body["allowed_domains"] = []string{}
			} else {
				domains := strings.Split(v, ",")
				trimmed := make([]string, 0, len(domains))
				for _, d := range domains {
					d = strings.TrimSpace(d)
					if d != "" {
						trimmed = append(trimmed, d)
					}
				}
				body["allowed_domains"] = trimmed
			}
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		resp, err := client.Patch("/api/v1/crews/"+crewID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Crew updated successfully.")
		return nil
	},
}

var crewDeleteCmd = &cobra.Command{
	Use:   "delete <slug-or-id>",
	Short: "Delete a crew",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Delete crew %q?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Delete("/api/v1/crews/" + crewID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Crew deleted.")
		return nil
	},
}

var crewSuggestCmd = &cobra.Command{
	Use:   "suggest",
	Short: "Get AI-powered crew suggestions based on a goal",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		goal, _ := cmd.Flags().GetString("goal")
		if goal == "" {
			return fmt.Errorf("--goal is required")
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/crew-ai-suggest", map[string]string{"goal": goal})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			CrewName    string `json:"crew_name"`
			Description string `json:"description"`
			Agents      []struct {
				Name      string `json:"name"`
				RoleTitle string `json:"role_title"`
				AgentRole string `json:"agent_role"`
			} `json:"agents"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		fmt.Printf("Suggested crew: %s\n", sanitizeTerminal(result.CrewName))
		fmt.Printf("Description: %s\n\n", sanitizeTerminal(result.Description))
		fmt.Println("Agents:")
		for _, a := range result.Agents {
			fmt.Printf("  - %s (%s, %s)\n",
				sanitizeTerminal(a.Name),
				sanitizeTerminal(a.RoleTitle),
				sanitizeTerminal(a.AgentRole),
			)
		}
		return nil
	},
}

func init() {
	crewCreateCmd.Flags().String("name", "", "Crew name (required)")
	crewCreateCmd.Flags().String("slug", "", "Crew slug (auto from name)")
	crewCreateCmd.Flags().String("description", "", "Description")
	crewCreateCmd.Flags().String("color", "", "Hex color (#3B82F6)")
	crewCreateCmd.Flags().String("icon", "", "Emoji icon")
	crewCreateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewCreateCmd.Flags().Float64("cpus", 0, "Container CPU limit")
	crewCreateCmd.Flags().Int("ttl", 0, "Auto-stop after idle hours (0 = never stop)")
	crewCreateCmd.Flags().String("network-mode", "", "Network policy mode: free or restricted")
	crewCreateCmd.Flags().String("allowed-domains", "", "Comma-separated allowed domains for restricted mode")

	crewUpdateCmd.Flags().String("name", "", "Crew name")
	crewUpdateCmd.Flags().String("description", "", "Description")
	crewUpdateCmd.Flags().String("color", "", "Hex color")
	crewUpdateCmd.Flags().String("icon", "", "Emoji icon")
	crewUpdateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewUpdateCmd.Flags().Float64("cpus", 0, "Container CPU limit")
	crewUpdateCmd.Flags().Int("ttl", -1, "Auto-stop after idle hours (0 = disable TTL)")
	crewUpdateCmd.Flags().String("network-mode", "", "Network policy mode: free or restricted")
	crewUpdateCmd.Flags().String("allowed-domains", "", "Comma-separated allowed domains for restricted mode")

	crewDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	crewSuggestCmd.Flags().String("goal", "", "What should this crew accomplish? (required)")
}
