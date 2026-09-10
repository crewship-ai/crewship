package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// A draft file is the revision envelope returned by get. Keeping that envelope
// makes CLI edits use the same compare-and-swap contract as the browser.
var routineDraftCmd = &cobra.Command{
	Use:   "draft",
	Short: "Save unpublished recipes and publish an explicitly reviewed revision",
	Long: `Drafts do not change live recipes or activate schedules. Get a revision envelope,
edit its document, then save that file. Publish validates the saved revision without
executing work. Concurrent edits or changes to the live recipe return a conflict.
The legacy routine save command continues to publish directly.

  crewship routine draft get digest -f json > draft.json
  crewship routine draft save draft.json -f json > saved.json
  crewship routine draft publish saved.json

Publish accepts --approve-risk for an explicit review of capability changes.`,
}

func newRoutineDraftCommand(action string) *cobra.Command {
	use := action
	if action == "get" {
		use += " <slug>"
	}
	if action == "save" || action == "publish" || action == "discard" {
		use += " <draft.json>"
	}
	cmd := &cobra.Command{Use: use, Short: action + " a routine draft", Args: cobra.ExactArgs(1)}
	if action == "list" {
		cmd.Args = cobra.NoArgs
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		base := "/api/v1/workspaces/" + url.PathEscape(client.GetWorkspaceID()) + "/pipelines"
		var slug string
		var body any
		if action == "get" {
			slug = args[0]
		}
		if action == "save" || action == "publish" || action == "discard" {
			raw, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var draft struct {
				ID       string                     `json:"id"`
				Slug     string                     `json:"slug"`
				Revision int                        `json:"revision"`
				Document map[string]json.RawMessage `json:"document"`
			}
			if err = json.Unmarshal(raw, &draft); err != nil {
				return fmt.Errorf("read draft: %w", err)
			}
			if draft.Slug == "" || draft.Document == nil {
				return fmt.Errorf("use the revision envelope from routine draft get/save")
			}
			slug = draft.Slug
			body = json.RawMessage(raw)
			if action == "publish" || action == "discard" {
				if draft.ID == "" || draft.Revision < 1 {
					return fmt.Errorf("save the draft before %s", action)
				}
				body = map[string]any{"id": draft.ID, "revision": draft.Revision}
				if action == "publish" {
					var definition map[string]json.RawMessage
					if err := json.Unmarshal(draft.Document["definition"], &definition); err != nil || definition == nil {
						return fmt.Errorf("draft document has no definition object to validate")
					}
					// Validate exactly the file's document; publication rejects if another
					// editor saved a different revision while this proof was being minted.
					testBody := map[string]any{"definition": draft.Document["definition"]}
					if crew, ok := draft.Document["author_crew_id"]; ok {
						testBody["author_crew_id"] = crew
					}
					resp, err := client.Post(base+"/test_run", testBody)
					if err != nil {
						return err
					}
					if err = cli.CheckError(resp); err != nil {
						resp.Body.Close()
						return err
					}
					var proof struct {
						SaveToken string `json:"save_token"`
					}
					err = json.NewDecoder(resp.Body).Decode(&proof)
					resp.Body.Close()
					if err != nil {
						return err
					}
					if proof.SaveToken == "" {
						return fmt.Errorf("validation did not return a publication proof")
					}
					approved, _ := cmd.Flags().GetBool("approve-risk")
					body = map[string]any{"id": draft.ID, "revision": draft.Revision, "save_token": proof.SaveToken, "approve_risk": approved}
				}
			}
		}
		var resp *http.Response
		var err error
		switch action {
		case "list":
			resp, err = client.Get(base + "/drafts")
		case "get":
			resp, err = client.Get(base + "/" + url.PathEscape(slug) + "/draft")
		case "save":
			resp, err = client.Post(base+"/drafts", body)
		case "publish":
			resp, err = client.Post(base+"/"+url.PathEscape(slug)+"/publish", body)
		case "discard":
			resp, err = client.Do("DELETE", base+"/"+url.PathEscape(slug)+"/draft", body)
		default:
			return fmt.Errorf("unknown draft action %q", action)
		}
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err = cli.CheckError(resp); err != nil {
			return err
		}
		if action == "discard" {
			return resolvedFormatter(cmd).JSON(map[string]bool{"discarded": true})
		}
		var result any
		if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(result, func() { b, _ := json.MarshalIndent(result, "", "  "); fmt.Fprintln(cmd.OutOrStdout(), string(b)) })
	}
	if action == "publish" {
		cmd.Flags().Bool("approve-risk", false, "Approve the capability changes in this exact publication")
	}
	return cmd
}
func init() {
	for _, action := range []string{"list", "get", "save", "publish", "discard"} {
		routineDraftCmd.AddCommand(newRoutineDraftCommand(action))
	}
	pipelineCmd.AddCommand(routineDraftCmd)
}
