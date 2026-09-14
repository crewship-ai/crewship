package main

// `crewship routine webhooks receipts` — the read side of routine webhook
// deliveries. A routine delivery is executed by the pipeline engine and never
// produces a work item; what it leaves behind is a receipt: its identity, the
// body's fingerprint and the run it produced. These commands read those
// receipts so "did you receive this one, and what ran" can be answered
// without re-sending the delivery.
//
// Nothing here prints the payload. body_sha256 and body_bytes identify it
// without publishing whatever the sender put inside.

import (
	"fmt"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// routineReceiptRow mirrors the API's RoutineWebhookReceipt. A NAMED type so
// cli_yaml_key_parity_test.go can hold it to the json/yaml contract.
type routineReceiptRow struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id" yaml:"workspace_id"`
	WebhookID        string  `json:"webhook_id" yaml:"webhook_id"`
	SourceDeliveryID string  `json:"source_delivery_id" yaml:"source_delivery_id"`
	Profile          string  `json:"profile"`
	BodySHA256       string  `json:"body_sha256" yaml:"body_sha256"`
	BodyBytes        int64   `json:"body_bytes" yaml:"body_bytes"`
	RunID            string  `json:"run_id" yaml:"run_id"`
	RunStatus        *string `json:"run_status" yaml:"run_status"`
	ReceivedAt       string  `json:"received_at" yaml:"received_at"`
	DedupExpiresAt   string  `json:"dedup_expires_at" yaml:"dedup_expires_at"`
}

type routineReceiptPageBody struct {
	Items      []routineReceiptRow `json:"items"`
	NextCursor *string             `json:"next_cursor" yaml:"next_cursor"`
}

var routineWebhookReceiptsCmd = &cobra.Command{
	Use:   "receipts",
	Short: "Inspect routine webhook receipts — which deliveries were accepted and what ran",
	Long: `Read the receipts routine webhook deliveries leave behind.

A receipt records that one delivery was accepted: the webhook, the sender's
delivery id, the body's fingerprint and the pipeline run it produced. Inside
its dedup window (30 days from acceptance) a redelivery of the same id is
answered with that run instead of executing again; once the window closes the
receipt is removed and the same id is new work.

The payload itself is never returned.`,
}

var routineWebhookReceiptsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List routine webhook receipts",
	Long: `List receipts, paginated at 100 rows.

Passing --webhook together with --source-id asks the identity question — "did
you receive this one" — and answers with zero or one row. A sender's delivery
id is only unique within a webhook, so --source-id on its own is refused.`,
	Example: `  crewship routine webhooks receipts list
  crewship routine webhooks receipts list --webhook pwh_123
  crewship routine webhooks receipts list --webhook pwh_123 --source-id evt-8f4c1a2e`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		query := url.Values{}
		for flag, param := range map[string]string{
			"webhook": "webhook_id", "source-id": "source_delivery_id", "after": "after",
		} {
			if value, _ := cmd.Flags().GetString(flag); value != "" {
				query.Set(param, value)
			}
		}
		path := workspacePath(client, "/routine-webhook-receipts")
		if encoded := query.Encode(); encoded != "" {
			path += "?" + encoded
		}
		var page routineReceiptPageBody
		if err := workGetJSON(client, path, &page); err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		return f.AutoHuman(page, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tWEBHOOK\tSOURCE ID\tRUN\tRUN STATUS\tRECEIVED\tDEDUP UNTIL")
			for _, rcpt := range page.Items {
				status := workDeref(rcpt.RunStatus)
				if status == "" {
					status = "(run record not available)"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					workShortID(f, rcpt.ID), rcpt.WebhookID, rcpt.SourceDeliveryID,
					workShortID(f, rcpt.RunID), status,
					workShortTime(rcpt.ReceivedAt), workShortTime(rcpt.DedupExpiresAt))
			}
			_ = w.Flush()
			if len(page.Items) == 0 {
				fmt.Println("(no receipts)")
			}
			if page.NextCursor != nil {
				fmt.Printf("\nMore results: crewship routine webhooks receipts list --after %s\n", *page.NextCursor)
			}
		})
	},
}

var routineWebhookReceiptsGetCmd = &cobra.Command{
	Use:   "get <receipt-id>",
	Short: "Show one routine webhook receipt",
	Long: `Show one receipt: which webhook accepted the delivery, the sender's delivery
id, the body's fingerprint, the pipeline run it produced and that run's current
status, and until when a redelivery is deduplicated.

The payload is not returned; inspect the run with 'crewship routine logs <run-id>'.`,
	Example: `  crewship routine webhooks receipts get crcp000000000000`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := workClient()
		if err != nil {
			return err
		}
		var rcpt routineReceiptRow
		if err := workGetJSON(client, workspacePath(client, "/routine-webhook-receipts/"+url.PathEscape(args[0])), &rcpt); err != nil {
			return err
		}
		f := resolvedFormatter(cmd)
		return f.AutoHuman(rcpt, func() {
			status := workDeref(rcpt.RunStatus)
			if status == "" {
				status = "(run record not available)"
			}
			f.Detail([][]string{
				{"ID", rcpt.ID},
				{"Webhook", rcpt.WebhookID},
				{"Source delivery id", rcpt.SourceDeliveryID},
				{"Profile", rcpt.Profile},
				{"Body", fmt.Sprintf("%s (%d bytes)", rcpt.BodySHA256, rcpt.BodyBytes)},
				{"Run", rcpt.RunID},
				{"Run status", status},
				{"Received", workShortTime(rcpt.ReceivedAt)},
				{"Dedup until", workShortTime(rcpt.DedupExpiresAt)},
			})
		})
	},
}

func init() {
	routineWebhookReceiptsListCmd.Flags().String("webhook", "", "Only receipts accepted by this webhook id")
	routineWebhookReceiptsListCmd.Flags().String("source-id", "", "The sender's delivery id; requires --webhook and answers with zero or one row")
	routineWebhookReceiptsListCmd.Flags().String("after", "", "Cursor from a previous page's next_cursor")
	routineWebhookReceiptsCmd.AddCommand(routineWebhookReceiptsListCmd)
	routineWebhookReceiptsCmd.AddCommand(routineWebhookReceiptsGetCmd)
	routineWebhooksCmd.AddCommand(routineWebhookReceiptsCmd)
}
