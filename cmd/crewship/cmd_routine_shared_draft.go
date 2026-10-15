package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedRoutineDraft struct {
	DraftID      string         `json:"draft_id" yaml:"draft_id"`
	Revision     *int           `json:"revision" yaml:"revision"`
	BaseRevision *int           `json:"base_revision" yaml:"base_revision"`
	Name         string         `json:"name" yaml:"name"`
	Description  *string        `json:"description" yaml:"description"`
	Definition   map[string]any `json:"definition" yaml:"definition"`
	CanEdit      bool           `json:"can_edit" yaml:"can_edit"`
}

func newRoutineSharedDraftCommand() *cobra.Command {
	command := &cobra.Command{Use: "shared-draft <routine-id-or-slug>", Short: "Read a collaborative routine draft with explicit View and Edit grants", Args: cobra.ExactArgs(1)}
	command.RunE = func(cmd *cobra.Command, args []string) error { return sharedRoutineDraftRequest(args[0], nil) }
	save := &cobra.Command{Use: "save <routine-id-or-slug>", Short: "Save a reviewed draft snapshot without publishing or running it", Args: cobra.ExactArgs(1)}
	save.Flags().String("file", "", "Complete JSON snapshot from routine shared-draft; use - for stdin")
	_ = save.MarkFlagRequired("file")
	save.RunE = func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			handle, err := os.Open(file)
			if err != nil {
				return err
			}
			defer handle.Close()
			reader = handle
		}
		decoder := json.NewDecoder(io.LimitReader(reader, (2<<20)+1))
		decoder.DisallowUnknownFields()
		var snapshot sharedRoutineDraft
		if err := decoder.Decode(&snapshot); err != nil {
			return fmt.Errorf("invalid draft snapshot: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("supply one complete JSON snapshot")
		}
		if snapshot.Revision == nil || *snapshot.Revision < 0 || snapshot.BaseRevision == nil || *snapshot.BaseRevision < 0 || snapshot.Description == nil || snapshot.Name == "" || snapshot.Definition == nil {
			return fmt.Errorf("retain draft_id, revision and base_revision; supply name, description and a definition object")
		}
		// can_edit is a read-only hint in the returned snapshot. Authority is
		// always rechecked by the server; neither a hidden GET nor a retry occurs.
		body := map[string]any{"draft_id": snapshot.DraftID, "revision": *snapshot.Revision, "base_revision": *snapshot.BaseRevision, "name": snapshot.Name, "description": *snapshot.Description, "definition": snapshot.Definition}
		return sharedRoutineDraftRequest(args[0], body)
	}
	command.AddCommand(save)
	command.AddCommand(newRoutineDraftReviewCommand(), newRoutineDraftReviewStatusCommand())
	return command
}

func sharedRoutineDraftRequest(id string, body map[string]any) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("provide a routine identifier")
	}
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	if workspace == "" || workspace == "." || workspace == ".." {
		return fmt.Errorf("invalid workspace identifier")
	}
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library/" + url.PathEscape(id) + "/draft"
	var response *http.Response
	var err error
	if body == nil {
		response, err = client.Get(path)
	} else {
		response, err = client.Put(path, body)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return err
	}
	var snapshot sharedRoutineDraft
	if err = cli.ReadJSON(response, &snapshot); err != nil {
		return err
	}
	if snapshot.Revision == nil || snapshot.BaseRevision == nil {
		return fmt.Errorf("server returned an incomplete draft snapshot")
	}
	return newFormatter().Auto(snapshot, []string{"NAME", "DRAFT", "REVISION", "BASE", "EDIT"}, [][]string{{snapshot.Name, snapshot.DraftID, fmt.Sprint(*snapshot.Revision), fmt.Sprint(*snapshot.BaseRevision), fmt.Sprint(snapshot.CanEdit)}})
}

func init() { pipelineCmd.AddCommand(newRoutineSharedDraftCommand()) }
