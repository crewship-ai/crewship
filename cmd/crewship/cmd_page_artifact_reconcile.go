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

// errArtifactSuccessorUnknown means the successor read returned 404 although
// newer versions exist (an Edit user cannot read history), so absence is not
// proven.
var errArtifactSuccessorUnknown = errors.New("successor version outcome unknown")

// artifactLatestVersion reads the page artifact's latest version. It reports
// the latest only when the caller may write: a reader is served the shared
// version in its place, which says nothing about newer saves.
func artifactLatestVersion(client *cli.Client, slug string) (int64, error) {
	resp, err := client.Get("/api/v1/pages/" + pagePathEscape(slug) + "/artifact")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := pageCheckError(resp); err != nil {
		return 0, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err != nil {
		return 0, fmt.Errorf("read artifact: %w", err)
	}
	if len(raw) > 9<<20 {
		return 0, fmt.Errorf("artifact response exceeds limit")
	}
	var snapshot struct {
		CanWrite      bool  `json:"can_write" yaml:"can_write"`
		LatestVersion int64 `json:"latest_version" yaml:"latest_version"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return 0, fmt.Errorf("decode artifact: %w", err)
	}
	if !snapshot.CanWrite || snapshot.LatestVersion < 1 {
		return 0, errors.New("your access no longer includes the latest version (the read returned the shared version), so absence cannot be proven; ask the owner, then confirm before publishing again")
	}
	return snapshot.LatestVersion, nil
}

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
		if _, duplicate := byPath[f.Path]; duplicate {
			return false
		}
		byPath[f.Path] = f
	}
	for _, f := range want.Files {
		other, ok := byPath[f.Path]
		if !ok || other.Content != f.Content || other.MediaType != f.MediaType {
			return false
		}
		delete(byPath, f.Path) // A duplicate local path cannot confirm a save.
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
		// 404 is not proof of absence: the server also answers 404 to an
		// editor asking for a historical version. Absence is proven only when
		// the latest version is not newer than the base.
		latest, err := artifactLatestVersion(client, slug)
		if err != nil {
			return result, fmt.Errorf("version %d returned 404 and the latest version could not be read: %w", base+1, err)
		}
		if latest > base {
			return result, fmt.Errorf("%w: latest is version %d, but version %d is not readable with your access, so the save may have been applied", errArtifactSuccessorUnknown, latest, base+1)
		}
		return result, errArtifactSuccessorAbsent
	}
	if err := pageCheckError(resp); err != nil {
		return result, err
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
	if err != nil {
		return result, fmt.Errorf("read artifact: %w", err)
	}
	if len(raw) > 9<<20 {
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
		// Keep received HTTP refusals definitive even when pageCheckError
		// enriches them into a validation/rate-limit error without APIError.
		if postErr != nil && resp.StatusCode != http.StatusConflict && resp.StatusCode < 500 {
			return postErr
		}
		if postErr == nil {
			raw, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
			// Successful headers do not prove the acknowledgement arrived:
			// the server may have committed before its body was interrupted.
			// Keep this uncertain outcome on the same read-only reconcile path.
			switch {
			case err != nil:
				postErr = fmt.Errorf("read publish acknowledgement: %w", err)
			case len(raw) > 9<<20:
				postErr = fmt.Errorf("project response exceeds limit")
			case !json.Valid(raw):
				// A connection closed without a length ends the read cleanly
				// on a partial body; that is the same lost acknowledgement.
				postErr = fmt.Errorf("read publish acknowledgement: incomplete response body")
			default:
				return pageEmitMachine(artifactCmdFormatter(cmd), raw, "{}")
			}
		}
	}
	if !artifactSaveOutcomeUncertain(postErr) {
		return postErr
	}
	result, err := artifactReconcile(client, slug, base, body)
	if err != nil {
		return artifactReconcileError(err, base, postErr)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "save reply was lost (%v); version %d holds exactly this manifest, so the save is confirmed\n", postErr, result.Version)
	return artifactCmdMachine(cmd, result)
}

// artifactReconcileError turns a failed successor read into the outcome the
// caller acts on. postErr is the lost publish reply, nil for a manual reconcile.
func artifactReconcileError(err error, base int64, postErr error) error {
	lost := ""
	if postErr != nil {
		lost = fmt.Sprintf("save reply was lost (%v); ", postErr)
	}
	switch {
	case errors.Is(err, errArtifactSuccessorAbsent):
		return cli.NotFoundf("%sversion %d does not exist: the save was not applied; publishing again with --base-version %d is safe", lost, base+1, base)
	case errors.Is(err, errArtifactSuccessorDiffers):
		return cli.WithExitCode(fmt.Errorf("%sversion %d exists with different content: the save was not applied as sent; read the latest version and merge before publishing again", lost, base+1), cli.ExitConflict)
	case postErr != nil:
		// Unknown outcome. Not %w: the original 409 would otherwise exit 5,
		// which promises "another save took it" — a fact nobody has read.
		return fmt.Errorf("%v; version %d could not be read (%v), so the save may or may not have been applied; confirm with `crewship page artifact reconcile --base-version %d` before publishing again", postErr, base+1, err, base)
	}
	return err
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
(another save took it; read the latest version and merge before publishing).
Any other failure means the version could not be read; the outcome is still unknown.`, RunE: func(cmd *cobra.Command, args []string) error {
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
		if err != nil {
			return artifactReconcileError(err, base, nil)
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
