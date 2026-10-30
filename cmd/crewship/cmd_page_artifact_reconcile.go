package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// A save whose reply was lost (connection drop, 5xx after commit, or a 409 on
// the retry that the first attempt caused) may still have committed. The only
// safe confirmation is the exact successor: version base+1 holding the same
// entrypoint and the same files byte for byte. Anything else is not ours, and
// the caller keeps the original error and their local manifest.

type artifactManifestFile struct {
	Path      string `json:"path" yaml:"path"`
	Content   string `json:"content" yaml:"content"`
	MediaType string `json:"media_type" yaml:"media_type"`
}

type artifactManifestView struct {
	Version    int64                  `json:"version" yaml:"version"`
	EntryPoint string                 `json:"entrypoint" yaml:"entrypoint"`
	Files      []artifactManifestFile `json:"files" yaml:"files"`
}

type artifactReconcileResult struct {
	Reconciled bool   `json:"reconciled" yaml:"reconciled"`
	PageID     string `json:"page_id,omitempty" yaml:"page_id,omitempty"`
	Slug       string `json:"slug,omitempty" yaml:"slug,omitempty"`
	Version    int64  `json:"version" yaml:"version"`
}

var errArtifactSuccessorAbsent = errors.New("successor version absent")

// errArtifactSuccessorDiffers means base+1 exists with other content: some
// other save took that version. The caller's save was not applied as sent.
var errArtifactSuccessorDiffers = errors.New("successor version differs")

func artifactSameFiles(saved artifactManifestView, base int64, local map[string]any) bool {
	raw, err := json.Marshal(local)
	if err != nil {
		return false
	}
	var want artifactManifestView
	if json.Unmarshal(raw, &want) != nil {
		return false
	}
	if saved.Version != base+1 || saved.EntryPoint != want.EntryPoint || len(saved.Files) != len(want.Files) {
		return false
	}
	byPath := make(map[string]artifactManifestFile, len(saved.Files))
	for _, f := range saved.Files {
		byPath[f.Path] = f
	}
	for _, f := range want.Files {
		other, ok := byPath[f.Path]
		if !ok || other.Content != f.Content || other.MediaType != f.MediaType {
			return false
		}
	}
	return true
}

// artifactReconcile reads version base+1 and compares it with the local
// manifest. It never writes.
func artifactReconcile(client *cli.Client, slug string, base int64, local map[string]any) (artifactReconcileResult, error) {
	result := artifactReconcileResult{Version: base + 1}
	resp, err := client.Get("/api/v1/pages/" + pagePathEscape(slug) + "/artifact?version=" + strconv.FormatInt(base+1, 10))
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return result, errArtifactSuccessorAbsent
	}
	if err := pageCheckError(resp); err != nil {
		return result, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err != nil || len(raw) > 9<<20 {
		return result, fmt.Errorf("artifact response exceeds limit")
	}
	var snapshot struct {
		PageID   string               `json:"page_id" yaml:"page_id"`
		Slug     string               `json:"slug" yaml:"slug"`
		Manifest artifactManifestView `json:"manifest" yaml:"manifest"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return result, fmt.Errorf("decode artifact: %w", err)
	}
	result.PageID, result.Slug = snapshot.PageID, snapshot.Slug
	if !artifactSameFiles(snapshot.Manifest, base, local) {
		return result, errArtifactSuccessorDiffers
	}
	result.Reconciled = true
	return result, nil
}

// artifactPublishHuman is `page artifact publish` for a human credential:
// the same single POST, plus a read-only reconcile when the reply is lost.
func artifactPublishHuman(cmd *cobra.Command, slug string, base int64, body map[string]any) error {
	client, err := pageClient()
	if err != nil {
		return err
	}
	client = client.WithContext(cmd.Context())
	resp, postErr := client.Post("/api/v1/pages/"+pagePathEscape(slug)+"/artifact/versions", body)
	if postErr == nil {
		defer resp.Body.Close()
		postErr = pageCheckError(resp)
		if postErr == nil {
			raw, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
			if err != nil {
				return err
			}
			if len(raw) > 9<<20 {
				return fmt.Errorf("project response exceeds limit")
			}
			return pageEmitMachine(artifactCmdFormatter(cmd), raw, "{}")
		}
	}
	if !artifactSaveOutcomeUncertain(postErr) {
		return postErr
	}
	result, err := artifactReconcile(client, slug, base, body)
	if err != nil {
		return postErr
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "save reply was lost (%v); version %d holds exactly this manifest, so the save is confirmed\n", postErr, result.Version)
	return artifactCmdMachine(cmd, result)
}

// A lost acknowledgement surfaces as a transport error, a 5xx, or a 409 on
// the retry. Every other refusal is definitive and needs no read.
func artifactSaveOutcomeUncertain(err error) bool {
	if err == nil {
		return false
	}
	var api *cli.APIError
	if errors.As(err, &api) {
		return api.Status == http.StatusConflict || api.Status >= 500
	}
	var quota *artifactQuotaError
	if errors.As(err, &quota) {
		return false
	}
	// No HTTP status at all: the request may or may not have reached the server.
	return true
}

func newPageArtifactReconcileCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "reconcile <slug>", Args: cobra.ExactArgs(1), Short: "Confirm whether a publish whose reply was lost was saved", Long: `Read version base+1 and compare it with your local manifest: same entrypoint and the
same files (path, content, media type). Read-only — it never publishes.

Exit 0 with {"reconciled":true,"version":N} when that version holds exactly your
manifest. Exit 3 when base+1 does not exist (nothing was saved; publishing again
with the same --base-version is safe). Exit 5 when base+1 exists with other content
(another save took it; read the latest version and merge before publishing).`, RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
			return errors.New("artifact reconcile requires human authentication; an agent run reads artifact_versions")
		}
		base, _ := cmd.Flags().GetInt64("base-version")
		if base < 1 || !cmd.Flags().Changed("base-version") {
			return cli.WithExitCode(fmt.Errorf("--base-version must be the exact positive version the publish was based on"), cli.ExitValidation)
		}
		body, err := readArtifactCLIManifest(cmd)
		if err != nil {
			return err
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		result, err := artifactReconcile(client.WithContext(cmd.Context()), args[0], base, body)
		switch {
		case errors.Is(err, errArtifactSuccessorAbsent):
			return cli.NotFoundf("version %d does not exist: the save was not applied; publishing again with --base-version %d is safe", base+1, base)
		case errors.Is(err, errArtifactSuccessorDiffers):
			return cli.WithExitCode(fmt.Errorf("version %d exists with different content: the save was not applied as sent; read the latest version and merge before publishing again", base+1), cli.ExitConflict)
		case err != nil:
			return err
		}
		return artifactCmdMachine(cmd, result)
	}}
	cmd.Flags().Int64("base-version", 0, "The --base-version the uncertain publish used")
	cmd.Flags().String("file", "", "The same JSON manifest {entrypoint,files,message} you published; - reads stdin")
	return cmd
}

// artifactCmdFormatter binds the resolved output format to the command's own
// stdout so callers (and tests) that redirect it receive the document.
func artifactCmdFormatter(cmd *cobra.Command) *cli.Formatter {
	f := resolvedFormatter(cmd)
	f.Writer = cmd.OutOrStdout()
	return f
}

func artifactCmdMachine(cmd *cobra.Command, v any) error { return artifactCmdFormatter(cmd).Machine(v) }
