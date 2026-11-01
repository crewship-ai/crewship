package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/cli"
	wsproto "github.com/crewship-ai/crewship/internal/ws"
)

// `--send-to-agent` alone queues a version-bound task; the task starts only
// when the artifact's source chat receives a message carrying the matching
// artifact_project_intent (internal/api/pages_artifact_chat.go). The viewer
// sends that message right after creating the comment
// (hooks/use-artifact-agent-dispatch.ts). `--wait` does the same from the
// CLI: deliver the intent, confirm the saved echo, then poll the comment
// until the agent answers or asks for clarification.

// artifactDispatchConfirmTimeout matches the viewer's wait for the saved echo.
var artifactDispatchConfirmTimeout = 15 * time.Second

// artifactWaitPollInterval is how often --wait rereads the comment.
var artifactWaitPollInterval = 2 * time.Second

type artifactQueuedComment struct {
	ID       string `json:"id" yaml:"id"`
	Version  int64  `json:"version" yaml:"version"`
	Path     string `json:"path" yaml:"path"`
	Selector string `json:"selector" yaml:"selector"`
	Quote    string `json:"quote" yaml:"quote"`
	Text     string `json:"text" yaml:"text"`
	Status   string `json:"status" yaml:"status"`
}

type artifactOrigin struct {
	ChatID  string `json:"chat_id" yaml:"chat_id"`
	AgentID string `json:"agent_id" yaml:"agent_id"`
}

func addArtifactCommentWaitFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("wait", false, "With --send-to-agent: start the task in the artifact's source chat, then wait until the agent answers or asks for clarification")
	cmd.Flags().Duration("wait-timeout", 15*time.Minute, "With --wait: give up waiting after this long (the task stays queued)")
}

// artifactCommentWaitRequested validates --wait before anything is sent.
func artifactCommentWaitRequested(cmd *cobra.Command, sendToAgent bool) (bool, time.Duration, error) {
	wait, _ := cmd.Flags().GetBool("wait")
	timeout, _ := cmd.Flags().GetDuration("wait-timeout")
	if !wait {
		return false, 0, nil
	}
	if !sendToAgent {
		return false, 0, cli.WithExitCode(errors.New("--wait requires --send-to-agent"), cli.ExitValidation)
	}
	if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
		return false, 0, errors.New("--wait sends a human chat message; an agent run cannot dispatch artifact tasks")
	}
	if timeout <= 0 {
		return false, 0, cli.WithExitCode(errors.New("--wait-timeout must be positive"), cli.ExitValidation)
	}
	return true, timeout, nil
}

// artifactCommentDispatch creates the comment, delivers its task to the
// source chat and waits for the outcome. kind is "project" or "design".
func artifactCommentDispatch(cmd *cobra.Command, kind, slug string, body any, timeout time.Duration) error {
	client, err := pageClient()
	if err != nil {
		return err
	}
	client = client.WithContext(cmd.Context())
	base := "/api/v1/pages/" + pagePathEscape(slug) + "/artifact"
	if kind == "design" {
		base = "/api/v1/pages/" + pagePathEscape(slug) + "/design"
	}
	var comment artifactQueuedComment
	if err := artifactJSONRequest(client, http.MethodPost, base+"/comments", body, &comment); err != nil {
		return err
	}
	if comment.ID == "" || comment.Version < 1 || comment.Status != "pending" {
		return fmt.Errorf("comment was saved but no agent task was queued (status %q)", comment.Status)
	}
	stderr := cmd.ErrOrStderr()
	fmt.Fprintf(stderr, "comment %s queued on version %d\n", comment.ID, comment.Version)

	snapshotPath := base + "?version=" + strconv.FormatInt(comment.Version, 10)
	if kind == "design" {
		snapshotPath = base
	}
	var snapshot struct {
		PageID string          `json:"page_id" yaml:"page_id"`
		Origin *artifactOrigin `json:"origin" yaml:"origin"`
	}
	if err := artifactJSONRequest(client, http.MethodGet, snapshotPath, nil, &snapshot); err != nil {
		return fmt.Errorf("comment %s is queued, but its artifact could not be read: %w", comment.ID, err)
	}
	if snapshot.Origin == nil || snapshot.Origin.ChatID == "" || snapshot.Origin.AgentID == "" {
		hint := "the artifact has no source chat; it was not created from an agent chat"
		if kind == "design" {
			hint = "the Design has no connected chat; connect one with `crewship page design agent <slug> --chat-id <chat>`"
		}
		return cli.WithExitCode(fmt.Errorf("comment %s is queued, but %s", comment.ID, hint), cli.ExitValidation)
	}
	intent := artifacts.ProjectIntent{SubmissionID: "artifact-comment-" + comment.ID, PageID: snapshot.PageID, CommentID: comment.ID, BaseVersion: comment.Version}
	if kind == "design" {
		intent.Kind = "design"
	}
	if intent.Validate() != nil {
		return fmt.Errorf("comment %s is queued, but the server returned an unusable page or comment identifier", comment.ID)
	}
	if err := artifactDeliverIntent(client, *snapshot.Origin, artifactCommentPrompt(kind, snapshot.PageID, comment), intent); err != nil {
		retry, _ := json.Marshal(intent)
		fmt.Fprintf(stderr, "comment %s stays queued; to retry, save %s to a file and run: crewship run %s --chat %s --artifact-target <file> \"<instruction>\"\n", comment.ID, retry, snapshot.Origin.AgentID, snapshot.Origin.ChatID)
		return err
	}
	fmt.Fprintf(stderr, "task delivered to chat %s; waiting for the agent's reply\n", snapshot.Origin.ChatID)
	return artifactWaitForAnswer(cmd, client, kind, base, comment, timeout)
}

// artifactCommentPrompt is the viewer's exact instruction text.
func artifactCommentPrompt(kind, page string, c artifactQueuedComment) string {
	if kind == "design" {
		return fmt.Sprintf("Update Design @%s according to comment %s: %s. Selected node: %s, original version: %d. Use design_get, then design_apply; a successful design_apply already saves the new version and answers the comment, so do not call design_snapshot afterwards. Preserve concurrent changes and report the confirmed version.", page, c.ID, c.Text, c.Selector, c.Version)
	}
	return fmt.Sprintf("Update the artifact project according to this exact comment. Page: @%s. Base version: %d. File: %s. Element: %s. Selected text: %s\nRequested change: %s\nPublish a new version with base_version %d, then reply to comment %s with the new version and what changed. If the anchor no longer matches, ask for clarification.", page, c.Version, c.Path, c.Selector, c.Quote, c.Text, c.Version, c.ID)
}

// artifactDeliverIntent sends one message carrying the intent and waits for
// the host's saved echo of it. A busy agent exits with cli.ExitBusy: nothing
// was stored, so the same intent can be sent again later.
func artifactDeliverIntent(client *cli.Client, origin artifactOrigin, prompt string, intent artifacts.ProjectIntent) error {
	restricted, err := restrictedChatProfile(client, origin.ChatID)
	if err != nil {
		return err
	}
	if restricted {
		return cli.WithExitCode(errors.New("targeted artifact edits require a trusted human chat"), cli.ExitValidation)
	}
	token, err := cli.WSTokenFromServer(client)
	if err != nil {
		return fmt.Errorf("get WS token: %w", err)
	}
	ws, err := cli.NewWSClient(streamServerURL(), token)
	if err != nil {
		return err
	}
	defer ws.Close()
	if err := ws.Subscribe("session:" + origin.ChatID); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	if err := sendRunMessage(ws, "agent:"+origin.AgentID, origin.ChatID, prompt, 0, map[string]any{artifacts.ProjectIntentMetadataKey: intent}); err != nil {
		return fmt.Errorf("send message: %w", err)
	}
	outcome := make(chan error, 1)
	go func() { outcome <- artifactAwaitEcho(ws, intent.SubmissionID) }()
	select {
	case err := <-outcome:
		return err
	case <-time.After(artifactDispatchConfirmTimeout):
		_ = ws.Close()
		return errors.New("the host did not confirm the task message in time; it may still start — check the comment before retrying")
	}
}

func artifactAwaitEcho(ws *cli.WSClient, submission string) error {
	var closeReason string
	for {
		msg, err := ws.ReadMessage()
		if err != nil {
			if closeReason != "" {
				return fmt.Errorf("server rejected the connection: %s", closeReason)
			}
			return fmt.Errorf("ws read: %w", err)
		}
		if reason, ok := cli.CloseReason(msg); ok {
			closeReason = reason
			continue
		}
		event, err := cli.ParseChatEvent(msg)
		if err != nil || event == nil {
			continue
		}
		switch event.Type {
		case wsproto.AgentBusyEventType:
			notice := sanitizeTerminal(event.Content)
			if notice == "" {
				notice = "the agent is busy with another run"
			}
			return agentBusyError(notice)
		case "error":
			return fmt.Errorf("task message refused: %s", sanitizeTerminal(event.Content))
		case "user_message":
			if artifactEchoSubmission(event.Metadata) == submission {
				return nil
			}
		case "done":
			return errors.New("the chat ended the exchange without saving the task message")
		}
	}
}

func artifactEchoSubmission(metadata any) string {
	fields, _ := metadata.(map[string]any)
	intent, _ := fields[artifacts.ProjectIntentMetadataKey].(map[string]any)
	submission, _ := intent["submission_id"].(string)
	return submission
}

// artifactWaitForAnswer polls the comment until its task is answered or the
// agent asks for clarification, then prints the comment with its replies.
func artifactWaitForAnswer(cmd *cobra.Command, client *cli.Client, kind, base string, queued artifactQueuedComment, timeout time.Duration) error {
	path := base + "/comments?version=" + strconv.FormatInt(queued.Version, 10)
	if kind == "design" {
		path = base + "/comments"
	}
	deadline := time.Now().Add(timeout)
	last := queued.Status
	for {
		var listing struct {
			Comments []json.RawMessage `json:"comments" yaml:"comments"`
		}
		if err := artifactJSONRequest(client, http.MethodGet, path, nil, &listing); err != nil {
			return fmt.Errorf("comment %s: %w", queued.ID, err)
		}
		found := false
		for _, raw := range listing.Comments {
			var c artifactQueuedComment
			if json.Unmarshal(raw, &c) != nil || c.ID != queued.ID {
				continue
			}
			found = true
			if c.Status != last {
				fmt.Fprintf(cmd.ErrOrStderr(), "comment %s: %s\n", c.ID, c.Status)
				last = c.Status
			}
			if c.Status == "answered" || c.Status == "needs_clarification" {
				var doc any
				if err := json.Unmarshal(raw, &doc); err != nil {
					return err
				}
				return artifactCmdMachine(cmd, doc)
			}
		}
		if !found {
			return cli.NotFoundf("comment %s is no longer listed on version %d", queued.ID, queued.Version)
		}
		if time.Now().Add(artifactWaitPollInterval).After(deadline) {
			noun := "artifact"
			if kind == "design" {
				noun = "design"
			}
			return fmt.Errorf("comment %s is still %s after %s; the task stays queued — read it later with `crewship page %s comments`", queued.ID, last, timeout, noun)
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(artifactWaitPollInterval):
		}
	}
}

func artifactJSONRequest(client *cli.Client, method, path string, body any, out any) error {
	var resp *http.Response
	var err error
	if method == http.MethodGet {
		resp, err = client.Get(path)
	} else {
		resp, err = client.Post(path, body)
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := pageCheckError(resp); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err != nil || len(raw) > 9<<20 {
		return fmt.Errorf("artifact response exceeds limit")
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}
