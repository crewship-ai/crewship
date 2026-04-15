package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var notificationCmd = &cobra.Command{
	Use:     "notification",
	Aliases: []string{"notifications", "notif"},
	Short:   "Manage notifications for the current user",
}

type notificationItem struct {
	ID          string  `json:"id"`
	ActorType   string  `json:"actor_type"`
	ActorID     string  `json:"actor_id"`
	ActorName   *string `json:"actor_name"`
	Action      string  `json:"action"`
	EntityType  string  `json:"entity_type"`
	EntityID    *string `json:"entity_id"`
	EntityTitle *string `json:"entity_title"`
	ReadAt      *string `json:"read_at"`
	CreatedAt   string  `json:"created_at"`
}

var notificationListCmd = &cobra.Command{
	Use:   "list",
	Short: "List notifications for the current user",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		path := "/api/v1/notifications"
		unreadOnly, _ := cmd.Flags().GetBool("unread")
		limit, _ := cmd.Flags().GetInt("limit")
		q := ""
		if unreadOnly {
			q = "?read=false"
		}
		if limit > 0 {
			if q == "" {
				q = fmt.Sprintf("?limit=%d", limit)
			} else {
				q += fmt.Sprintf("&limit=%d", limit)
			}
		}

		client := newAPIClient()
		resp, err := client.Get(path + q)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var items []notificationItem
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "ACTION", "ENTITY", "TITLE", "ACTOR", "READ", "CREATED"}
		var rows [][]string
		for _, n := range items {
			title := "-"
			if n.EntityTitle != nil {
				title = *n.EntityTitle
			}
			actor := n.ActorID
			if n.ActorName != nil && *n.ActorName != "" {
				actor = *n.ActorName
			}
			read := "no"
			if n.ReadAt != nil {
				read = "yes"
			}
			rows = append(rows, []string{
				truncateID(n.ID, 12),
				n.Action,
				n.EntityType,
				truncateStr(title, 40),
				actor,
				read,
				n.CreatedAt,
			})
		}
		return f.Auto(items, headers, rows)
	},
}

var notificationCountCmd = &cobra.Command{
	Use:   "count",
	Short: "Show the number of unread notifications",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/notifications/count")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Unread int `json:"unread"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		fmt.Printf("Unread: %d\n", result.Unread)
		return nil
	},
}

var notificationReadCmd = &cobra.Command{
	Use:   "read <id>",
	Short: "Mark a notification as read",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/notifications/"+args[0]+"/read", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Notification marked as read.")
		return nil
	},
}

var notificationReadAllCmd = &cobra.Command{
	Use:   "read-all",
	Short: "Mark all notifications as read",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/notifications/read-all", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result struct {
			Updated int64 `json:"updated"`
		}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Marked %d notification(s) as read.", result.Updated))
		return nil
	},
}

var notificationDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"remove", "rm"},
	Short:   "Delete a notification",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Delete("/api/v1/notifications/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Notification deleted.")
		return nil
	},
}

func init() {
	notificationListCmd.Flags().Bool("unread", false, "Only show unread notifications")
	notificationListCmd.Flags().Int("limit", 0, "Limit number of notifications (default: server default)")

	notificationCmd.AddCommand(notificationListCmd)
	notificationCmd.AddCommand(notificationCountCmd)
	notificationCmd.AddCommand(notificationReadCmd)
	notificationCmd.AddCommand(notificationReadAllCmd)
	notificationCmd.AddCommand(notificationDeleteCmd)
}
