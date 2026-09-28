package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var chatShareCmd = &cobra.Command{
	Use:   "share",
	Short: "Share one chat's text with an expiring read-only token",
	Long:  "Share the ongoing user/assistant text of one chat. The token grants no workspace membership, files, tools or agent execution. New messages remain readable until expiry or revocation. Keep the token private; it is returned only once.",
}

func chatShareClient(agentRef string) (*cli.Client, string, error) {
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return nil, "", err
	}
	agentID, err := resolveAgentID(client, agentRef)
	if err != nil {
		return nil, "", err
	}
	return client, agentID, nil
}

func chatSharePath(agentID, chatID string) string {
	return "/api/v1/agents/" + url.PathEscape(agentID) + "/chats/" + url.PathEscape(chatID) + "/shares"
}

func validateChatShareReadServer(raw string) error {
	server, err := url.Parse(raw)
	if err != nil || server.Host == "" || (server.Scheme != "http" && server.Scheme != "https") || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return fmt.Errorf("an explicit http(s) --server URL is required")
	}
	if server.Scheme == "http" {
		host := server.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("--server must use https for a non-loopback host")
		}
	}
	return nil
}

var chatShareCreateCmd = &cobra.Command{
	Use: "create <agent> <chat-id>", Short: "Create a read-only share (default 24h, maximum 7 days)", Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		if ttl < time.Second || ttl > 7*24*time.Hour {
			return fmt.Errorf("ttl must be between 1 second and 168 hours")
		}
		client, agentID, err := chatShareClient(args[0])
		path := chatSharePath(agentID, args[1])
		if err != nil {
			return err
		}
		var out struct {
			Share map[string]any `json:"share" yaml:"share"`
			Token string         `json:"token" yaml:"token"`
		}
		if err := postJSON(client, path, map[string]any{"ttl_seconds": int64(ttl / time.Second)}, &out); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "yaml" {
			return f.YAML(out)
		}
		// JSON preserves the one-time token in a predictable structure for secure
		// piping to a file/secret manager. No token goes into a URL or stderr.
		return f.JSON(out)
	},
}
var chatShareListCmd = &cobra.Command{
	Use: "list <agent> <chat-id>", Short: "List chat shares without exposing their tokens", Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, agentID, err := chatShareClient(args[0])
		path := chatSharePath(agentID, args[1])
		if err != nil {
			return err
		}
		var out struct {
			Shares []map[string]any `json:"shares" yaml:"shares"`
		}
		if err := getJSON(client, path, &out); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "yaml" {
			return f.YAML(out)
		}
		return f.JSON(out)
	},
}
var chatShareRevokeCmd = &cobra.Command{
	Use: "revoke <agent> <chat-id> <share-id>", Short: "Immediately revoke a chat read token", Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, agentID, err := chatShareClient(args[0])
		path := chatSharePath(agentID, args[1])
		if err != nil {
			return err
		}
		if err := deleteJSON(client, path+"/"+url.PathEscape(args[2])); err != nil {
			return err
		}
		cmd.Println("Chat share revoked.")
		return nil
	},
}
var chatShareReadCmd = &cobra.Command{
	Use: "read <share-id>", Short: "Read shared text using a token from stdin and an explicit --server",
	Long: "Read one shared transcript without logging in or joining its workspace. Supply --server explicitly and pipe the share token through stdin with --token-stdin. The token is never taken from your normal CLI profile or a URL.", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		stdin, _ := cmd.Flags().GetBool("token-stdin")
		if !stdin {
			return fmt.Errorf("--token-stdin is required")
		}
		if err := validateChatShareReadServer(flagServer); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 4097))
		if err != nil {
			return fmt.Errorf("read share token from stdin: %w", err)
		}
		token := strings.TrimSpace(string(data))
		if len(data) > 4096 || !strings.HasPrefix(token, "cshr_") || strings.ContainsAny(token, "\r\n\t ") {
			return fmt.Errorf("invalid chat share token")
		}
		client := cli.NewClient(strings.TrimRight(flagServer, "/"), token, "").WithContext(cmd.Context())
		client.HTTPClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := client.Get("/api/v1/shared-chats/" + url.PathEscape(args[0]) + "/messages")
		if err != nil {
			return fmt.Errorf("read shared chat: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("read shared chat: HTTP %d", resp.StatusCode)
		}
		var out struct {
			Messages []map[string]any `json:"messages" yaml:"messages"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&out); err != nil {
			return fmt.Errorf("decode shared chat: %w", err)
		}
		f := newFormatter()
		if f.Format == "yaml" {
			return f.YAML(out)
		}
		return f.JSON(out)
	},
}

func init() {
	chatShareCreateCmd.Flags().Duration("ttl", 24*time.Hour, "Read access lifetime, maximum 168h")
	chatShareReadCmd.Flags().Bool("token-stdin", false, "Read the share token from stdin")
	chatShareCmd.AddCommand(chatShareCreateCmd, chatShareListCmd, chatShareRevokeCmd, chatShareReadCmd)
	chatCmd.AddCommand(chatShareCmd)
}
