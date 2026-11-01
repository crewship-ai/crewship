package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Design edits use the exact same authenticated API as the live canvas. A live
// agent run uses the sidecar MCP transport; it cannot fall back to a human login.
func newPageDesignCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "design", Short: "Collaborate on a Design canvas with frames, shapes, text and images"}
	create := &cobra.Command{Use: "create <slug>", Args: cobra.ExactArgs(1), Short: "Create an empty Design artifact", RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			return fmt.Errorf("--name is required")
		}
		body := map[string]any{"slug": args[0], "name": name}
		if cmd.Flags().Changed("test-item") {
			value, _ := cmd.Flags().GetBool("test-item")
			body["created_by_test"] = value
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/design", body)
	}}
	create.Flags().String("name", "", "Design title")
	create.Flags().Bool("test-item", false, "Explicitly mark this new human-created Design as a test item")
	cmd.AddCommand(create)
	for _, action := range []string{"get", "versions", "comments"} {
		action := action
		child := &cobra.Command{Use: action + " <slug>", Args: cobra.ExactArgs(1), Short: "Read current canvas or retained versions", RunE: func(cmd *cobra.Command, args []string) error {
			endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/design"
			if action != "get" {
				endpoint += "/" + action
			}
			if action == "get" {
				version, _ := cmd.Flags().GetInt64("version")
				if version < 0 || version > 9007199254740991 {
					return fmt.Errorf("version must be a nonnegative safe integer")
				}
				if version > 0 {
					endpoint += "?version=" + strconv.FormatInt(version, 10)
				}
			}
			return artifactCLIRequest(cmd, "GET", endpoint, nil)
		}}
		if action == "get" {
			child.Flags().Int64("version", 0, "Retained snapshot version; 0 reads the live canvas")
		}
		cmd.AddCommand(child)
	}
	apply := &cobra.Command{Use: "apply <slug>", Args: cobra.ExactArgs(1), Short: "Apply node upserts, deletions or order changes to the live Yjs document", Long: `Apply a JSON object with operations and optional message. An upsert contains a node
with id, type (frame, rect, text, image), name, x, y, width, height, fill and optional
parentId, text, src or linkTo. linkTo is the ID of the screen (frame) that a click
on this layer opens in prototype playback; "" removes the link. Delete uses {"op":"delete","id":"node-id"}; reorder uses
{"op":"reorder","order":["frame-id","node-id"]}. Preserve IDs of existing nodes.
The API applies these edits even when no browser has the canvas open.`, RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readDesignCLIChanges(cmd)
		if err != nil {
			return err
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/design/operations", body)
	}}
	apply.Flags().String("file", "", "JSON {operations:[...],message?}; - reads stdin")
	cmd.AddCommand(apply)
	snapshot := &cobra.Command{Use: "snapshot <slug>", Aliases: []string{"save"}, Args: cobra.ExactArgs(1), Short: "Save the current live canvas as a retained artifact version", RunE: func(cmd *cobra.Command, args []string) error {
		message, _ := cmd.Flags().GetString("message")
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/design/snapshots", map[string]any{"message": message})
	}}
	snapshot.Flags().String("message", "", "Version description")
	cmd.AddCommand(snapshot)
	restore := &cobra.Command{Use: "restore <slug>", Args: cobra.ExactArgs(1), Short: "Restore a retained canvas as a new version", RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("version")
		if version < 1 || version > 9007199254740991 {
			return fmt.Errorf("--version must be a positive safe integer")
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/design/restore", map[string]any{"version": version})
	}}
	restore.Flags().Int64("version", 0, "Retained version to restore")
	cmd.AddCommand(restore)
	share := &cobra.Command{Use: "share <slug>", Args: cobra.ExactArgs(1), Short: "Pin the retained version shared with readers", RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("version")
		if version < 1 || version > 9007199254740991 {
			return fmt.Errorf("--version must be a positive safe integer")
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/design/share", map[string]any{"version": version})
	}}
	share.Flags().Int64("version", 0, "Retained version to share")
	cmd.AddCommand(share)
	comment := &cobra.Command{Use: "comment <slug>", Args: cobra.ExactArgs(1), Short: "Comment on a stable Design node in an exact version", RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("version")
		node, _ := cmd.Flags().GetString("node")
		text, _ := cmd.Flags().GetString("text")
		quote, _ := cmd.Flags().GetString("quote")
		dispatch, _ := cmd.Flags().GetBool("send-to-agent")
		if version < 1 || version > 9007199254740991 || node == "" || text == "" {
			return fmt.Errorf("positive --version, --node and --text are required")
		}
		body := map[string]any{"version": version, "path": "design.json", "selector": node, "quote": quote, "text": text, "send_to_agent": dispatch}
		wait, timeout, err := artifactCommentWaitRequested(cmd, dispatch)
		if err != nil {
			return err
		}
		if wait {
			return artifactCommentDispatch(cmd, "design", args[0], body, timeout)
		}
		if dispatch && os.Getenv("CREWSHIP_AGENT_TOKEN") == "" {
			fmt.Fprintln(cmd.ErrOrStderr(), "task queued; it starts when the connected chat receives the task message — pass --wait to send it now, or use `crewship run --artifact-target`")
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/design/comments", body)
	}}
	comment.Flags().Int64("version", 0, "Exact retained version containing the node")
	comment.Flags().String("node", "", "Stable node ID")
	comment.Flags().String("text", "", "Comment text")
	comment.Flags().String("quote", "", "Selected node name or text")
	comment.Flags().Bool("send-to-agent", false, "Queue this comment for the selected agent")
	addArtifactCommentWaitFlags(comment)
	cmd.AddCommand(comment)
	agent := &cobra.Command{Use: "agent <slug>", Args: cobra.ExactArgs(1), Short: "Connect an owned private chat for Design changes", RunE: func(cmd *cobra.Command, args []string) error {
		chat, _ := cmd.Flags().GetString("chat-id")
		if chat == "" {
			return fmt.Errorf("--chat-id is required")
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/design/agent", map[string]any{"chat_id": chat})
	}}
	agent.Flags().String("chat-id", "", "Existing private owner chat")
	cmd.AddCommand(agent)
	cmd.AddCommand(newPageDesignWatchCommand())
	cmd.AddCommand(newPageDesignExportCommand())
	return cmd
}

func readDesignCLIChanges(cmd *cobra.Command) (map[string]any, error) {
	file, _ := cmd.Flags().GetString("file")
	if file == "" {
		return nil, fmt.Errorf("--file is required")
	}
	var reader io.Reader = cmd.InOrStdin()
	if file != "-" {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		reader = f
	}
	raw, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, fmt.Errorf("design changes exceed 1 MiB")
	}
	if err := validateArtifactManifestJSON(raw); err != nil {
		return nil, err
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return nil, fmt.Errorf("design changes must be one JSON object")
	}
	for key := range body {
		if key != "operations" && key != "message" {
			return nil, fmt.Errorf("unknown design field %q", key)
		}
	}
	operations, ok := body["operations"].([]any)
	if !ok || len(operations) == 0 || len(operations) > 500 {
		return nil, fmt.Errorf("operations must contain 1–500 changes")
	}
	if message, exists := body["message"]; exists {
		if _, ok := message.(string); !ok {
			return nil, fmt.Errorf("message must be a string")
		}
	}
	return body, nil
}

func init() { pageCmd.AddCommand(newPageDesignCommand()) }

// watch exposes the standard binary transport without inventing a second sync
// format. Consumers receive the original y-protocols frames as base64 NDJSON.
func newPageDesignWatchCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "watch <slug>", Args: cobra.ExactArgs(1), Short: "Watch standard Yjs sync and awareness frames as base64 NDJSON", RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
			return fmt.Errorf("live agents read Design through design_get; websocket watch requires human authentication")
		}
		formatter := resolvedFormatter(cmd)
		formatter.Writer = cmd.OutOrStdout()
		switch formatter.Format {
		case "table", "json", "ndjson", "yaml":
		default:
			return fmt.Errorf("Design streams require json, ndjson or yaml output")
		}
		count, _ := cmd.Flags().GetInt("count")
		if count < 0 {
			return fmt.Errorf("--count must be nonnegative")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/design/ws"
		if revision, _ := cmd.Flags().GetString("authority-revision"); revision != "" {
			if !designAuthorityRevisionValid(revision) {
				return cli.WithExitCode(fmt.Errorf("--authority-revision must be the decimal authority_revision from page design get"), cli.ExitValidation)
			}
			path += "?authority_revision=" + revision
		}
		request, err := client.NewRequest(cmd.Context(), http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		switch request.URL.Scheme {
		case "http":
			request.URL.Scheme = "ws"
		case "https":
			request.URL.Scheme = "wss"
		default:
			return fmt.Errorf("Design websocket requires HTTP or HTTPS server")
		}
		dialer := websocket.Dialer{Proxy: http.ProxyFromEnvironment, HandshakeTimeout: 15 * time.Second}
		conn, response, err := dialer.DialContext(cmd.Context(), request.URL.String(), request.Header)
		if err != nil {
			if response != nil {
				defer response.Body.Close()
				if response.StatusCode != http.StatusSwitchingProtocols {
					return designWatchRefused(response)
				}
			}
			return err
		}
		defer conn.Close()
		conn.SetReadLimit(4 << 20)
		stop := context.AfterFunc(cmd.Context(), func() { _ = conn.Close() })
		defer stop()
		// messageSync + syncStep1 + varUint8Array(empty state vector).
		if err = conn.WriteMessage(websocket.BinaryMessage, []byte{0, 0, 1, 0}); err != nil {
			return err
		}
		for received := 0; count == 0 || received < count; {
			typ, frame, e := conn.ReadMessage()
			if e != nil {
				if cmd.Context().Err() != nil {
					return cmd.Context().Err()
				}
				if websocket.IsCloseError(e, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return nil
				}
				return e
			}
			if typ != websocket.BinaryMessage {
				continue
			}
			if err = formatter.WriteStreamRow(map[string]string{"frame": base64.StdEncoding.EncodeToString(frame)}); err != nil {
				return err
			}
			received++
		}
		return nil
	}}
	cmd.Flags().Int("count", 0, "Stop after N binary frames; 0 watches until interrupted")
	cmd.Flags().String("authority-revision", "", "Admit the socket only if the workspace authority_revision still equals this value (from page design get)")
	return cmd
}

// The server compares the query value with CAST(authority_revision AS TEXT),
// a decimal integer. Anything else could only ever be refused.
func designAuthorityRevisionValid(value string) bool {
	if len(value) == 0 || len(value) > 20 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// A refused upgrade carries an ordinary API error body; route it through the
// same status → exit-code mapping as every other command.
func designWatchRefused(response *http.Response) error {
	if response.Body == nil {
		response.Body = http.NoBody
	}
	err := pageCheckError(response)
	if err == nil {
		return fmt.Errorf("Design websocket refused (HTTP %d)", response.StatusCode)
	}
	return fmt.Errorf("Design websocket refused: %w", err)
}
