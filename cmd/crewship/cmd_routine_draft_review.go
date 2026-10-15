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

type sharedRoutineReview struct {
	ID           string `json:"id" yaml:"id"`
	RoutineID    string `json:"routine_id" yaml:"routine_id"`
	DraftID      string `json:"draft_id" yaml:"draft_id"`
	Revision     int    `json:"revision" yaml:"revision"`
	BaseRevision int    `json:"base_revision" yaml:"base_revision"`
	State        string `json:"state" yaml:"state"`
	Version      int    `json:"published_version" yaml:"published_version"`
}

func newRoutineDraftReviewCommand() *cobra.Command {
	command := &cobra.Command{Use: "request-review <routine-id-or-slug>", Short: "Request publication review of an exact saved shared draft", Args: cobra.ExactArgs(1)}
	command.Flags().String("file", "", "Complete saved JSON snapshot from routine shared-draft; use - for stdin")
	_ = command.MarkFlagRequired("file")
	command.RunE = func(cmd *cobra.Command, args []string) error {
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
			return fmt.Errorf("invalid saved draft snapshot: %w", err)
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("supply one complete saved JSON snapshot")
		}
		if snapshot.DraftID == "" || snapshot.Revision == nil || *snapshot.Revision < 1 || snapshot.BaseRevision == nil || *snapshot.BaseRevision < 0 || snapshot.Description == nil || snapshot.Name == "" || snapshot.Definition == nil {
			return fmt.Errorf("supply a complete saved draft snapshot with draft_id, positive revision and base_revision")
		}
		body := map[string]any{"draft_id": snapshot.DraftID, "revision": *snapshot.Revision, "base_revision": *snapshot.BaseRevision}
		return sharedRoutineReviewRequest(args[0], "", body)
	}
	return command
}

func newRoutineDraftReviewStatusCommand() *cobra.Command {
	command := &cobra.Command{Use: "review-status <routine-id-or-slug> [review-id]", Short: "Read a shared draft publication receipt (latest by default)", Args: cobra.RangeArgs(1, 2)}
	command.RunE = func(cmd *cobra.Command, args []string) error {
		id := ""
		if len(args) == 2 {
			id = args[1]
			if id == "" || id == "." || id == ".." {
				return fmt.Errorf("invalid review identifier")
			}
		}
		return sharedRoutineReviewRequest(args[0], id, nil)
	}
	return command
}

func sharedRoutineReviewRequest(routine, review string, body map[string]any) error {
	if routine == "" || routine == "." || routine == ".." {
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
	base := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library/" + url.PathEscape(routine) + "/draft/reviews"
	var response *http.Response
	var err error
	if body == nil {
		path := base
		if review != "" {
			path += "/" + url.PathEscape(review)
		}
		response, err = client.Get(path)
	} else {
		response, err = client.Post(base, body)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return err
	}
	var receipt *sharedRoutineReview
	if err = cli.ReadJSON(response, &receipt); err != nil {
		return err
	}
	headers := []string{"REVIEW", "DRAFT", "REVISION", "BASE", "STATE", "PUBLISHED VERSION"}
	if receipt == nil {
		if body != nil || review != "" {
			return fmt.Errorf("server returned no publication receipt")
		}
		return newFormatter().Auto(nil, headers, nil)
	}
	validState := false
	switch receipt.State {
	case "pending", "published", "denied", "cancelled", "expired", "superseded":
		validState = true
	}
	if !validState || receipt.ID == "" || receipt.RoutineID == "" || receipt.DraftID == "" || receipt.Revision < 1 || receipt.BaseRevision < 0 || receipt.Version < 0 || (receipt.State == "published") != (receipt.Version > 0) {
		return fmt.Errorf("server returned an invalid publication receipt")
	}
	if body != nil && (receipt.DraftID != body["draft_id"] || receipt.Revision != body["revision"] || receipt.BaseRevision != body["base_revision"]) {
		return fmt.Errorf("server returned a receipt for a different draft snapshot")
	}
	version := "—"
	if receipt.Version > 0 {
		version = fmt.Sprint(receipt.Version)
	}
	return newFormatter().Auto(receipt, headers, [][]string{{receipt.ID, receipt.DraftID, fmt.Sprint(receipt.Revision), fmt.Sprint(receipt.BaseRevision), receipt.State, version}})
}
