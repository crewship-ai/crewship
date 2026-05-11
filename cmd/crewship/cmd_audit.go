package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// auditCmd surfaces the workspace audit log. The server's
// /api/v1/audit endpoint accepts a richer filter set than the older
// CLI exposed — action, entity_type, entity_id, user_id, date range,
// free-text search, and offset paging. Each flag below maps 1:1 to a
// server-side parameter so users get the same expressiveness as the
// admin UI.
//
// --action values are intentionally NOT enumerated in the help text.
// The audit table stores domain verbs (e.g. `agent.run`,
// `workspace.create`, `credential.rotate`) that grow over time;
// listing a stale "create/update/delete" trio in the help would
// misdirect users. Pointing at the server-side enum is more honest.
var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "View audit logs",
	Long: `View audit logs for the workspace.

Filters mirror the server-side /api/v1/audit query params:
  --action          Domain verb (agent.run, workspace.create, …)
  --entity-type     Entity kind (AGENT, BACKUP, CREDENTIAL, …)
  --entity-id       Narrow to a specific entity row
  --user            User ID who performed the action
  --since/--until   Date range (RFC3339 or 1h/24h/7d duration)
  --search          Free-text across action, entity_type, user email/name
  --page            Pagination (--lines per page)

Examples:
  crewship audit
  crewship audit --action agent.run --lines 100
  crewship audit --entity-type CREDENTIAL --since 24h
  crewship audit --search rotate --until 2026-05-01T00:00:00Z
  crewship audit --user u_abc123 --page 2`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		lines, _ := cmd.Flags().GetInt("lines")
		action, _ := cmd.Flags().GetString("action")
		entityType, _ := cmd.Flags().GetString("entity-type")
		entityID, _ := cmd.Flags().GetString("entity-id")
		userID, _ := cmd.Flags().GetString("user")
		since, _ := cmd.Flags().GetString("since")
		until, _ := cmd.Flags().GetString("until")
		search, _ := cmd.Flags().GetString("search")
		page, _ := cmd.Flags().GetInt("page")

		q := url.Values{}
		q.Set("limit", fmt.Sprintf("%d", lines))
		if page > 0 {
			q.Set("page", fmt.Sprintf("%d", page))
		}
		if action != "" {
			q.Set("action", action)
		}
		if entityType != "" {
			q.Set("entity_type", entityType)
		}
		if entityID != "" {
			q.Set("entity_id", entityID)
		}
		if userID != "" {
			q.Set("user_id", userID)
		}
		// Accept the same flexible since/until syntax journal uses so the
		// surface is uniform — RFC3339 passthrough plus 1h/24h/7d sugar.
		if since != "" {
			t, err := parseSince(since)
			if err != nil {
				return fmt.Errorf("bad --since: %w", err)
			}
			q.Set("date_from", t.Format(time.RFC3339))
		}
		if until != "" {
			t, err := parseSince(until)
			if err != nil {
				return fmt.Errorf("bad --until: %w", err)
			}
			q.Set("date_to", t.Format(time.RFC3339))
		}
		if search != "" {
			q.Set("search", search)
		}

		path := "/api/v1/audit?" + q.Encode()

		resp, err := client.Get(path)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Data []struct {
				ID         string  `json:"id"`
				Action     string  `json:"action"`
				EntityType string  `json:"entity_type"`
				EntityID   *string `json:"entity_id"`
				UserEmail  *string `json:"user_email"`
				CreatedAt  string  `json:"created_at"`
			} `json:"data"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"TIME", "ACTION", "ENTITY", "ENTITY_ID", "USER"}
		var rows [][]string
		for _, a := range result.Data {
			ts := a.CreatedAt
			if t, err := time.Parse(time.RFC3339Nano, a.CreatedAt); err == nil {
				ts = t.Format("2006-01-02 15:04:05")
			}
			entityID := "-"
			if a.EntityID != nil {
				entityID = *a.EntityID
				if len(entityID) > 12 {
					entityID = entityID[:12]
				}
			}
			user := "-"
			if a.UserEmail != nil {
				user = *a.UserEmail
			}
			rows = append(rows, []string{ts, a.Action, a.EntityType, entityID, user})
		}
		return f.Auto(result.Data, headers, rows)
	},
}

func init() {
	// Filter flags map 1:1 to /api/v1/audit query params. Names match the
	// admin UI's filter chips so a user clicking through the dashboard can
	// reproduce the same view from the CLI by reading the URL bar.
	auditCmd.Flags().String("action", "", "Filter by action (domain verb, e.g. agent.run, workspace.create)")
	auditCmd.Flags().String("entity-type", "", "Filter by entity type (AGENT, BACKUP, CREDENTIAL, …)")
	auditCmd.Flags().String("entity-id", "", "Filter by entity ID")
	auditCmd.Flags().String("user", "", "Filter by user ID who performed the action")
	auditCmd.Flags().String("since", "", "Start of date range (RFC3339 or duration: 1h, 24h, 7d)")
	auditCmd.Flags().String("until", "", "End of date range (RFC3339 or duration)")
	auditCmd.Flags().String("search", "", "Free-text search across action, entity_type, user email/name")
	auditCmd.Flags().Int("page", 0, "Page number (1-based, default unspecified)")
	auditCmd.Flags().Int("lines", 50, "Number of audit entries per page (server caps at 100)")
}
