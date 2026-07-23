package main

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/sidecar"
)

// mergeDomains unions extra into base, preserving base's order and appending
// only entries not already present (case-insensitive). Used to fold the
// package-registry preset into a crew's allowed_domains without duplicating or
// re-ordering the domains the operator already set (#1377).
func mergeDomains(base, extra []string) []string {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]string, 0, len(base)+len(extra))
	for _, d := range append(append([]string{}, base...), extra...) {
		key := strings.ToLower(strings.TrimSpace(d))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

// fetchCrewAllowedDomains reads a crew's current allowed_domains so a
// preset-only `crew update --allow-package-registries` can union onto them
// instead of overwriting. Returns an error rather than an empty slice on
// failure, so a transient GET error never silently wipes the crew's domains.
func fetchCrewAllowedDomains(client *cli.Client, crewID string) ([]string, error) {
	resp, err := client.Get("/api/v1/crews/" + crewID)
	if err != nil {
		return nil, err
	}
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var crew struct {
		AllowedDomains []string `json:"allowed_domains"`
	}
	if err := cli.ReadJSON(resp, &crew); err != nil {
		return nil, err
	}
	return crew.AllowedDomains, nil
}

// splitDomainsCSV parses a comma-separated --allowed-domains value into a
// trimmed, non-empty slice.
func splitDomainsCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, d := range parts {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

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
		if flags.Changed("allow-private-endpoints") {
			v, _ := flags.GetBool("allow-private-endpoints")
			body["allow_private_endpoints"] = v
		}
		var domains []string
		if v, _ := flags.GetString("allowed-domains"); v != "" {
			domains = splitDomainsCSV(v)
		}
		if pkg, _ := flags.GetBool("allow-package-registries"); pkg {
			domains = mergeDomains(domains, sidecar.PackageRegistryDomains)
		}
		if len(domains) > 0 {
			body["allowed_domains"] = domains
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
		if flags.Changed("allow-private-endpoints") {
			v, _ := flags.GetBool("allow-private-endpoints")
			body["allow_private_endpoints"] = v
		}
		pkgRegistries, _ := flags.GetBool("allow-package-registries")
		if flags.Changed("allowed-domains") || pkgRegistries {
			var base []string
			switch {
			case flags.Changed("allowed-domains"):
				v, _ := flags.GetString("allowed-domains")
				base = splitDomainsCSV(v)
			case pkgRegistries:
				// Preset-only update: fold the registries into the crew's
				// EXISTING allowed_domains rather than clobbering them.
				existing, ferr := fetchCrewAllowedDomains(client, crewID)
				if ferr != nil {
					return fmt.Errorf("fetch current allowed domains to merge registry preset: %w", ferr)
				}
				base = existing
			}
			if pkgRegistries {
				base = mergeDomains(base, sidecar.PackageRegistryDomains)
			} else {
				base = mergeDomains(base, nil) // dedup + normalize
			}
			// A restricted crew with no domains is meaningful (locks egress),
			// so send an explicit empty array rather than dropping the field.
			if base == nil {
				base = []string{}
			}
			body["allowed_domains"] = base
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
	crewCreateCmd.Flags().String("icon", "", "Lucide icon name (e.g. code, rocket, terminal)")
	crewCreateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewCreateCmd.Flags().Float64("cpus", 0, "Container CPU limit")
	crewCreateCmd.Flags().Int("ttl", 0, "Auto-stop after idle hours (0 = never stop)")
	crewCreateCmd.Flags().String("network-mode", "", "Network policy mode: free or restricted")
	crewCreateCmd.Flags().String("allowed-domains", "", "Comma-separated allowed domains for restricted mode (supports *.example.com wildcards)")
	crewCreateCmd.Flags().Bool("allow-package-registries", false, "Also allow the common package registries (npm, pip, cargo, go, apt, Docker Hub)")
	crewCreateCmd.Flags().Bool("allow-private-endpoints", false, "Allow agents to reach a private/LAN model endpoint (RFC1918/loopback); link-local/metadata stay blocked")

	crewUpdateCmd.Flags().String("name", "", "Crew name")
	crewUpdateCmd.Flags().String("description", "", "Description")
	crewUpdateCmd.Flags().String("color", "", "Hex color")
	crewUpdateCmd.Flags().String("icon", "", "Lucide icon name (e.g. code, rocket, terminal)")
	crewUpdateCmd.Flags().Int("memory-mb", 0, "Container memory limit in MB")
	crewUpdateCmd.Flags().Float64("cpus", 0, "Container CPU limit")
	crewUpdateCmd.Flags().Int("ttl", -1, "Auto-stop after idle hours (0 = disable TTL)")
	crewUpdateCmd.Flags().String("network-mode", "", "Network policy mode: free or restricted")
	crewUpdateCmd.Flags().String("allowed-domains", "", "Comma-separated allowed domains for restricted mode (supports *.example.com wildcards)")
	crewUpdateCmd.Flags().Bool("allow-package-registries", false, "Append the common package registries (npm, pip, cargo, go, apt, Docker Hub) to the crew's allowed domains")
	crewUpdateCmd.Flags().Bool("allow-private-endpoints", false, "Allow agents to reach a private/LAN model endpoint (RFC1918/loopback); link-local/metadata stay blocked")

	crewDeleteCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	crewSuggestCmd.Flags().String("goal", "", "What should this crew accomplish? (required)")
}
