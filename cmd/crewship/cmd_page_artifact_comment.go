package main

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
)

type artifactCommentRequest struct {
	Version     int64  `json:"version"`
	Path        string `json:"path"`
	Selector    string `json:"selector"`
	Quote       string `json:"quote"`
	Text        string `json:"text"`
	SendToAgent bool   `json:"send_to_agent"`
}

func newPageArtifactCommentCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "comment <slug>", Args: cobra.ExactArgs(1), Short: "Comment on an exact version and selected element", RunE: func(cmd *cobra.Command, args []string) error {
		var body artifactCommentRequest
		body.Version, _ = cmd.Flags().GetInt64("version")
		body.Path, _ = cmd.Flags().GetString("path")
		body.Selector, _ = cmd.Flags().GetString("selector")
		body.Quote, _ = cmd.Flags().GetString("quote")
		body.Text, _ = cmd.Flags().GetString("text")
		body.SendToAgent, _ = cmd.Flags().GetBool("send-to-agent")
		for _, value := range []string{args[0], body.Path, body.Selector, body.Quote, body.Text} {
			if !utf8.ValidString(value) {
				return fmt.Errorf("artifact comment arguments must be valid UTF-8")
			}
		}
		if body.Version < 1 || strings.TrimSpace(body.Path) == "" || strings.TrimSpace(body.Selector) == "" || strings.TrimSpace(body.Quote) == "" || strings.TrimSpace(body.Text) == "" {
			return fmt.Errorf("positive --version and nonempty --path, --selector, --quote and --text are required")
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/comments", body)
	}}
	cmd.Flags().Int64("version", 0, "Exact selected project version")
	cmd.Flags().String("path", "", "Canonical file path in that version")
	cmd.Flags().String("selector", "", "Stable unique element ID selector, for example #artifact-heading")
	cmd.Flags().String("quote", "", "Selected element text from that exact version")
	cmd.Flags().String("text", "", "Requested change or comment")
	cmd.Flags().Bool("send-to-agent", false, "Queue an exact version-bound task for the agent")
	return cmd
}
