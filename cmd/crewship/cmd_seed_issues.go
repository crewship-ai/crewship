package main

import (
	"fmt"
	"os"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var seedIssuesCmd = &cobra.Command{
	Use:   "seed-issues",
	Short: "Seed sample issues, labels, and comments via the API",
	Long: `Creates a diverse set of labels, issues across crews with different
statuses, priorities, and comments. Useful for demos and testing.
Requires crews with LEAD agents to already exist.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		nuke, _ := cmd.Flags().GetBool("nuke")

		if nuke {
			fmt.Fprintln(os.Stderr, "Cleaning existing issues...")
			// Fetch all issues and delete them
			resp, err := client.Get("/api/v1/issues?limit=100")
			if err != nil {
				return err
			}
			var existing []issueItem
			if err := cli.ReadJSON(resp, &existing); err == nil {
				for _, iss := range existing {
					if iss.Identifier != nil && (iss.Status == "BACKLOG" || iss.Status == "CANCELLED") {
						_, _ = client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier))
					} else if iss.Identifier != nil {
						// Move to CANCELLED first (only BACKLOG/CANCELLED can be deleted)
						_, _ = client.Patch(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier), map[string]string{"status": "CANCELLED"})
						_, _ = client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s", iss.CrewID, *iss.Identifier))
					}
				}
			}
			// Delete projects
			resp, err = client.Get("/api/v1/projects")
			if err == nil {
				var projects []struct{ ID string `json:"id"` }
				if cli.ReadJSON(resp, &projects) == nil {
					for _, p := range projects {
						_, _ = client.Delete("/api/v1/projects/" + p.ID)
					}
				}
			}
			// Delete labels
			resp, err = client.Get("/api/v1/labels")
			if err == nil {
				var labels []struct{ ID string `json:"id"` }
				if cli.ReadJSON(resp, &labels) == nil {
					for _, l := range labels {
						_, _ = client.Delete("/api/v1/labels/" + l.ID)
					}
				}
			}
			cli.PrintSuccess("Cleaned existing issues and labels")
		}

		// ── Step 1: Resolve crews ──
		fmt.Fprintln(os.Stderr, "Resolving crews...")
		resp, err := client.Get("/api/v1/crews")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return fmt.Errorf("failed to list crews: %w", err)
		}
		var crews []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Name string `json:"name"`
		}
		if err := cli.ReadJSON(resp, &crews); err != nil {
			return err
		}
		if len(crews) == 0 {
			return fmt.Errorf("no crews found — run `./dev.sh seed` first to create crews and agents")
		}

		crewBySlug := map[string]string{}
		for _, c := range crews {
			crewBySlug[c.Slug] = c.ID
			fmt.Fprintf(os.Stderr, "  Found crew: %s (%s)\n", c.Slug, c.ID[:8])
		}

		// ── Step 2: Create labels ──
		fmt.Fprintln(os.Stderr, "Creating labels...")
		labels := []struct {
			Name  string `json:"name"`
			Color string `json:"color"`
			Group string `json:"label_group,omitempty"`
		}{
			{Name: "Bug", Color: "#EF4444"},
			{Name: "Feature", Color: "#A855F7"},
			{Name: "Improvement", Color: "#3B82F6"},
			{Name: "Security", Color: "#EF4444"},
			{Name: "Infrastructure", Color: "#F97316"},
			{Name: "Documentation", Color: "#6B7280"},
			{Name: "UX", Color: "#EC4899"},
			{Name: "Performance", Color: "#EAB308"},
		}
		for _, l := range labels {
			resp, err := client.Post("/api/v1/labels", l)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Failed to create label %s: %v\n", l.Name, err)
				continue
			}
			if resp.StatusCode >= 400 {
				resp.Body.Close()
				continue // label may already exist
			}
			resp.Body.Close()
			fmt.Fprintf(os.Stderr, "  + Label: %s\n", l.Name)
		}

		// ── Step 3: Create projects ──
		fmt.Fprintln(os.Stderr, "Creating projects...")
		type projectDef struct {
			name     string
			color    string
			icon     string
			status   string
			priority string
		}
		projectDefs := []projectDef{
			{name: "Core Platform", color: "#3B82F6", icon: "anchor", status: "in_progress", priority: "high"},
			{name: "Orchestration Engine", color: "#F97316", icon: "workflow", status: "planned", priority: "high"},
			{name: "Infrastructure & Integrations", color: "#22C55E", icon: "server", status: "in_progress", priority: "high"},
			{name: "Developer Experience", color: "#EAB308", icon: "sparkles", status: "planned", priority: "medium"},
			{name: "Crewship CLI", color: "#A855F7", icon: "terminal", status: "in_progress", priority: "medium"},
			{name: "Security & Compliance", color: "#EF4444", icon: "shield", status: "in_progress", priority: "urgent"},
			{name: "Technical Debt", color: "#6B7280", icon: "wrench", status: "backlog", priority: "low"},
		}
		projectByName := map[string]string{} // name → id
		for _, pd := range projectDefs {
			resp, err := client.Post("/api/v1/projects", map[string]interface{}{
				"name":     pd.name,
				"color":    pd.color,
				"icon":     pd.icon,
				"status":   pd.status,
				"priority": pd.priority,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Failed to create project %s: %v\n", pd.name, err)
				continue
			}
			if resp.StatusCode >= 400 {
				resp.Body.Close()
				continue
			}
			var created struct {
				ID string `json:"id"`
			}
			if cli.ReadJSON(resp, &created) == nil {
				projectByName[pd.name] = created.ID
				fmt.Fprintf(os.Stderr, "  + Project: %s\n", pd.name)
			}
		}

		// ── Step 4: Create issues ──
		fmt.Fprintln(os.Stderr, "Creating issues...")
		type issueDef struct {
			crew        string
			title       string
			desc        string
			priority    string
			project     string
			targetState string // final status after creation (BACKLOG is default)
			comment     string
		}

		issueDefs := []issueDef{
			// Core Platform
			{crew: "engineering", project: "Core Platform", title: "Implement WebSocket channel authentication", desc: "Clients can subscribe to any WS channel without auth. Add JWT-based channel validation to prevent cross-workspace data leaks.", priority: "urgent", targetState: "IN_PROGRESS", comment: "Started channel_auth.go — using JWT claims for channel subscription validation. Hub already has broadcast isolation."},
			{crew: "engineering", project: "Core Platform", title: "Add rate limiting to public API endpoints", desc: "No rate limiting exists. Implement per-user token bucket with configurable limits per endpoint category.", priority: "high", targetState: "TODO"},
			{crew: "engineering", project: "Core Platform", title: "Implement real-time notification system", desc: "Add notification inbox with desktop push, email digest, and in-app badge counts. Support subscribe/unsubscribe per issue.", priority: "medium"},
			{crew: "engineering", project: "Core Platform", title: "Add database connection pooling for WAL mode", desc: "SQLite single-writer bottleneck under concurrent load. Evaluate WAL mode with busy_timeout and connection pool.", priority: "high", targetState: "TODO"},

			// Security & Compliance
			{crew: "engineering", project: "Security & Compliance", title: "Refactor credential encryption to AES-256-GCM v2", desc: "Current v1 format works but key derivation is slow. Migrate to HKDF-based approach while maintaining backward compat.", priority: "medium", targetState: "DONE", comment: "Migration complete. All v1 credentials auto-upgraded on first decrypt. Backward compat verified with 847 test credentials."},
			{crew: "quality", project: "Security & Compliance", title: "Security audit: validate all API input sanitization", desc: "Review all 47 API handlers for SQL injection, XSS in stored content, SSRF in OAuth flows, path traversal in file operations.", priority: "urgent", targetState: "IN_PROGRESS", comment: "Phase 1 complete: reviewed all 47 API handlers. Found 3 potential SSRF vectors in OAuth discovery and 1 path traversal in file server. Creating sub-tasks for each fix."},

			// Infrastructure
			{crew: "devops", project: "Infrastructure & Integrations", title: "Set up automated SQLite backup with Litestream", desc: "No backup strategy in place. Implement hourly Litestream replication to S3-compatible storage.", priority: "urgent", targetState: "IN_PROGRESS", comment: "Evaluating Litestream vs custom backup. Litestream supports continuous WAL replication to S3. Setting up MinIO locally for testing."},
			{crew: "devops", project: "Infrastructure & Integrations", title: "Configure Prometheus metrics endpoint", desc: "Expose /metrics for Go runtime stats, HTTP request latency, active WebSocket connections, mission execution metrics.", priority: "medium", targetState: "TODO"},
			{crew: "devops", project: "Infrastructure & Integrations", title: "Dockerize production deployment", desc: "Create multi-stage Dockerfile with embedded Next.js static export. Target: single container under 100MB.", priority: "low"},
			{crew: "devops", project: "Infrastructure & Integrations", title: "Monitor crew container resource usage", desc: "Track CPU, memory, and disk usage per crew container. Alert when approaching limits. Add dashboard widget.", priority: "medium"},

			// Orchestration Engine
			{crew: "research", project: "Orchestration Engine", title: "Evaluate local LLM models for Keeper engine", desc: "Test Llama 3.2, Mistral 7B, and Phi-3 for keeper confidence scoring. Compare accuracy vs latency on real decisions.", priority: "medium", targetState: "REVIEW", comment: "Llama 3.2 8B: 87% accuracy, 340ms avg. Mistral 7B: 82%, 280ms. Phi-3 mini: 71%, 180ms. Recommending Llama 3.2 for production."},
			{crew: "research", project: "Orchestration Engine", title: "Benchmark A2A protocol message throughput", desc: "Measure agent-to-agent communication latency and throughput under load. Target: <50ms p99 for intra-crew messages.", priority: "low"},
			{crew: "quality", project: "Orchestration Engine", title: "Write integration tests for mission orchestration engine", desc: "MissionEngine has no integration tests. Cover: DAG resolution, task dispatch, failure recovery, circuit breaker, approval gates.", priority: "high", targetState: "TODO"},

			// Crewship CLI / DX
			{crew: "quality", project: "Crewship CLI", title: "Add E2E test suite for issue tracker API", desc: "Test full CRUD lifecycle: create issue, update status, add comments, label assignment, delete. Cover edge cases and error paths.", priority: "medium"},
			{crew: "engineering", project: "Developer Experience", title: "Build project management module", desc: "Add projects table to group issues. Include milestones, progress tracking, lead assignment, and timeline view.", priority: "low"},

			// Technical Debt
			{crew: "engineering", project: "Technical Debt", title: "Clean up unused MCP template providers", desc: "Several MCP provider templates are unused or outdated. Remove dead code and consolidate.", priority: "low"},
		}

		for _, def := range issueDefs {
			crewID, ok := crewBySlug[def.crew]
			if !ok {
				fmt.Fprintf(os.Stderr, "  ! Crew %q not found, skipping: %s\n", def.crew, def.title)
				continue
			}

			body := map[string]interface{}{
				"title":    def.title,
				"priority": def.priority,
			}
			if def.desc != "" {
				body["description"] = def.desc
			}
			if def.project != "" {
				if pid, ok := projectByName[def.project]; ok {
					body["project_id"] = pid
				}
			}

			resp, err := client.Post(fmt.Sprintf("/api/v1/crews/%s/issues", crewID), body)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ! Failed: %s — %v\n", def.title, err)
				continue
			}
			if err := cli.CheckError(resp); err != nil {
				fmt.Fprintf(os.Stderr, "  ! Failed: %s — %v\n", def.title, err)
				continue
			}
			var created struct {
				ID         string  `json:"id"`
				Identifier *string `json:"identifier"`
			}
			if err := cli.ReadJSON(resp, &created); err != nil {
				fmt.Fprintf(os.Stderr, "  ! Failed to read response: %v\n", err)
				continue
			}
			ident := ""
			if created.Identifier != nil {
				ident = *created.Identifier
			}
			fmt.Fprintf(os.Stderr, "  + %s: %s (%s)\n", ident, truncate(def.title, 50), def.priority)

			// Transition to target state if needed
			if def.targetState != "" && def.targetState != "BACKLOG" && ident != "" {
				transitions := statusPath(def.targetState)
				for _, status := range transitions {
					r, err := client.Patch(
						fmt.Sprintf("/api/v1/crews/%s/issues/%s", crewID, ident),
						map[string]string{"status": status},
					)
					if err != nil {
						fmt.Fprintf(os.Stderr, "    ! Transition to %s failed: %v\n", status, err)
						break
					}
					r.Body.Close()
					if r.StatusCode >= 400 {
						fmt.Fprintf(os.Stderr, "    ! Transition to %s failed: HTTP %d\n", status, r.StatusCode)
						break
					}
				}
			}

			// Add comment if provided
			if def.comment != "" && ident != "" {
				r, err := client.Post(
					fmt.Sprintf("/api/v1/crews/%s/issues/%s/comments", crewID, ident),
					map[string]string{"body": def.comment},
				)
				if err == nil {
					r.Body.Close()
				}
			}

			// Small delay to get different timestamps
			time.Sleep(50 * time.Millisecond)
		}

		fmt.Fprintln(os.Stderr, "")
		cli.PrintSuccess(fmt.Sprintf("Seeded %d projects, %d issues across %d crews", len(projectByName), len(issueDefs), len(crewBySlug)))
		return nil
	},
}

// statusPath returns the sequence of status transitions needed to reach target from BACKLOG.
func statusPath(target string) []string {
	switch target {
	case "TODO":
		return []string{"TODO"}
	case "IN_PROGRESS":
		return []string{"IN_PROGRESS"}
	case "REVIEW":
		return []string{"IN_PROGRESS", "REVIEW"}
	case "DONE":
		return []string{"IN_PROGRESS", "DONE"}
	case "FAILED":
		return []string{"IN_PROGRESS", "FAILED"}
	case "CANCELLED":
		return []string{"CANCELLED"}
	default:
		return nil
	}
}

func init() {
	seedIssuesCmd.Flags().Bool("nuke", false, "Delete all existing issues and labels before seeding")
}
