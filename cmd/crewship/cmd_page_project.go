package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
	"github.com/spf13/cobra"
)

func newPageProjectCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Create, save and build a Page application draft"}
	get := &cobra.Command{Use: "get <slug>", Args: cobra.ExactArgs(1), Short: "Read a project draft and its revision", RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/project"
		revision, _ := cmd.Flags().GetInt64("revision")
		if revision < 0 {
			return fmt.Errorf("revision must be positive")
		}
		if revision > 0 {
			endpoint += fmt.Sprintf("/history/%d", revision)
		}
		resp, err := client.Get(endpoint)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, pages.MaxTransferBytes+1))
		if err != nil {
			return err
		}
		if len(b) > pages.MaxTransferBytes {
			return fmt.Errorf("project response exceeds limit")
		}
		var payload any
		if err := json.Unmarshal(b, &payload); err != nil {
			return err
		}
		var writeErr error
		err = resolvedFormatter(cmd).AutoHuman(payload, func() {
			_, writeErr = fmt.Fprintln(cmd.OutOrStdout(), string(b))
		})
		if err != nil {
			return err
		}
		return writeErr
	}}
	get.Flags().Int64("revision", 0, "Read a historical source revision instead of the current draft")
	set := &cobra.Command{Use: "set <slug>", Args: cobra.ExactArgs(1), Short: "Save a source YAML/JSON document against an expected draft revision", RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		revision, _ := cmd.Flags().GetInt64("revision")
		if file == "" || revision < 0 {
			return fmt.Errorf("--file and --revision (0 for a new draft) are required")
		}
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		p, err := pages.ParseSourceProject(reader)
		if err != nil {
			return err
		}
		body := map[string]any{"expected_revision": revision, "project": p}
		definitionFile, _ := cmd.Flags().GetString("definition")
		if definitionFile != "" {
			f, err := os.Open(definitionFile)
			if err != nil {
				return err
			}
			raw, readErr := io.ReadAll(io.LimitReader(f, pages.MaxSpecBytes+1))
			f.Close()
			if readErr != nil {
				return readErr
			}
			if len(raw) > pages.MaxSpecBytes {
				return fmt.Errorf("definition exceeds spec limit")
			}
			definition, err := pages.ParseDocument(raw)
			if err != nil {
				return err
			}
			body["definition"] = definition
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Put("/api/v1/pages/"+pagePathEscape(args[0])+"/project", body)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return err
		}
		var payload any
		if err := json.Unmarshal(b, &payload); err != nil {
			return err
		}
		var writeErr error
		err = resolvedFormatter(cmd).AutoHuman(payload, func() {
			_, writeErr = fmt.Fprintln(cmd.OutOrStdout(), string(b))
		})
		if err != nil {
			return err
		}
		return writeErr
	}}
	set.Flags().String("definition", "", "Optional authored Page YAML to update the draft definition")
	set.Flags().String("file", "", "Source project YAML/JSON; - reads stdin")
	set.Flags().Int64("revision", -1, "Expected revision; 0 creates the first draft")
	init := &cobra.Command{Use: "init", Args: cobra.NoArgs, Short: "Print the supported React/TypeScript source starter as one YAML document", RunE: func(cmd *cobra.Command, args []string) error {
		source := pageprofile.Source()
		directory, _ := cmd.Flags().GetString("dir")
		if directory != "" {
			return unpackPageProject(source, directory)
		}
		f := resolvedFormatter(cmd)
		if f.Format == "json" || f.Format == "ndjson" {
			return f.JSON(source)
		}
		b, err := source.MarshalYAMLSource()
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(cmd.OutOrStdout(), string(b))
		return err
	}}
	init.Flags().String("dir", "", "Write starter files into a new working directory instead of stdout")
	build := &cobra.Command{Use: "build <slug>", Args: cobra.ExactArgs(1), Short: "Start an isolated preview build against a source revision", RunE: func(cmd *cobra.Command, args []string) error {
		rev, _ := cmd.Flags().GetInt64("revision")
		if rev < 1 {
			return fmt.Errorf("--revision is required")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/project/build", map[string]any{"expected_revision": rev})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return err
		}
		return pageEmitMachine(resolvedFormatter(cmd), b, "{}")
	}}
	build.Flags().Int64("revision", 0, "Expected source revision")
	preview := &cobra.Command{Use: "preview <slug>", Args: cobra.ExactArgs(1), Short: "Read the latest preview build status and artifact", RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/project/preview")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := pageCheckError(resp); err != nil {
			return err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, (9<<20)+1))
		if err != nil {
			return err
		}
		if len(b) > 9<<20 {
			return fmt.Errorf("preview exceeds limit")
		}
		return pageEmitMachine(resolvedFormatter(cmd), b, "{}")
	}}

	history := &cobra.Command{Use: "history <slug>", Args: cobra.ExactArgs(1), Short: "List audited source revisions and Git checkpoints", RunE: func(cmd *cobra.Command, args []string) error {
		before, _ := cmd.Flags().GetInt64("before")
		if before < 0 {
			return fmt.Errorf("before must be positive")
		}
		endpoint := "/api/v1/pages/" + pagePathEscape(args[0]) + "/project/history"
		if before > 0 {
			endpoint += fmt.Sprintf("?before=%d", before)
		}
		return projectCLIRequest(cmd, "GET", endpoint, nil)
	}}
	history.Flags().Int64("before", 0, "List older revisions before this revision")
	restore := &cobra.Command{Use: "restore <slug>", Args: cobra.ExactArgs(1), Short: "Restore a source revision as a new draft; never publish it", RunE: func(cmd *cobra.Command, args []string) error {
		revision, _ := cmd.Flags().GetInt64("revision")
		expected, _ := cmd.Flags().GetInt64("expected-revision")
		if revision < 1 || expected < 1 {
			return fmt.Errorf("--revision and --expected-revision are required")
		}
		return projectCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/project/restore", map[string]any{"revision": revision, "expected_revision": expected})
	}}
	restore.Flags().Int64("revision", 0, "Historical source revision to restore")
	restore.Flags().Int64("expected-revision", 0, "Current draft revision; a stale value returns 409")

	check := &cobra.Command{Use: "check <slug>", Args: cobra.ExactArgs(1), Short: "Verify a built candidate's sources, artifact and current bindings", RunE: func(cmd *cobra.Command, args []string) error {
		buildID, _ := cmd.Flags().GetString("build")
		revision, _ := cmd.Flags().GetInt64("revision")
		if buildID == "" || revision < 1 {
			return fmt.Errorf("--build and --revision are required")
		}
		return projectCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/project/check", map[string]any{"build_id": buildID, "expected_revision": revision})
	}}
	check.Flags().String("build", "", "Build receipt ID")
	check.Flags().Int64("revision", 0, "Source revision of the build")
	publish := &cobra.Command{Use: "publish <slug>", Args: cobra.ExactArgs(1), Short: "Publish reviewed application code and its declared Page definition", RunE: func(cmd *cobra.Command, args []string) error {
		buildID, _ := cmd.Flags().GetString("build")
		revision, _ := cmd.Flags().GetInt64("revision")
		expected, _ := cmd.Flags().GetInt64("expected-publication")
		reviewed, _ := cmd.Flags().GetBool("reviewed-code")
		if buildID == "" || revision < 1 || expected < 0 || !reviewed {
			return fmt.Errorf("--build, --revision, --expected-publication and --reviewed-code are required")
		}
		return projectCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/project/publish", map[string]any{"build_id": buildID, "expected_revision": revision, "expected_publication": expected, "reviewed_code": true})
	}}
	publish.Flags().String("build", "", "Reviewed build ID")
	publish.Flags().Int64("revision", 0, "Reviewed source revision")
	publish.Flags().Int64("expected-publication", -1, "Current publication version; 0 for first publication")
	publish.Flags().Bool("reviewed-code", false, "Confirm this exact source/build was reviewed for execution by Page readers")
	rollback := &cobra.Command{Use: "rollback <slug>", Args: cobra.ExactArgs(1), Short: "Republish an earlier application version without undoing routine effects", RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("publication")
		expected, _ := cmd.Flags().GetInt64("expected-publication")
		reviewed, _ := cmd.Flags().GetBool("reviewed-code")
		if version < 1 || expected < 1 || !reviewed {
			return fmt.Errorf("--publication, --expected-publication and --reviewed-code are required")
		}
		return projectCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/project/publish", map[string]any{"rollback_version": version, "expected_publication": expected, "reviewed_code": true})
	}}
	rollback.Flags().Int64("publication", 0, "Earlier publication version to restore")
	rollback.Flags().Int64("expected-publication", 0, "Current publication version")
	rollback.Flags().Bool("reviewed-code", false, "Confirm the earlier application code was reviewed")
	application := &cobra.Command{Use: "application <slug>", Args: cobra.ExactArgs(1), Short: "Read the published application and publication receipt", RunE: func(cmd *cobra.Command, args []string) error {
		return projectCLIRequest(cmd, "GET", "/api/v1/pages/"+pagePathEscape(args[0])+"/application", nil)
	}}
	status := &cobra.Command{Use: "action-status <slug> <pending-id>", Args: cobra.ExactArgs(2), Short: "Read your own published application's pending and run status", RunE: func(cmd *cobra.Command, args []string) error {
		return projectCLIRequest(cmd, "GET", "/api/v1/pages/"+pagePathEscape(args[0])+"/application/actions/"+pagePathEscape(args[1]), nil)
	}}
	publications := &cobra.Command{Use: "publications <slug>", Args: cobra.ExactArgs(1), Short: "List application publication receipts", RunE: func(cmd *cobra.Command, args []string) error {
		before, _ := cmd.Flags().GetInt64("before")
		if before < 0 {
			return fmt.Errorf("--before must be positive")
		}
		path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/project/publications"
		if before > 0 {
			path += fmt.Sprintf("?before=%d", before)
		}
		return projectCLIRequest(cmd, "GET", path, nil)
	}}
	publications.Flags().Int64("before", 0, "Return publications older than this version")
	withdraw := &cobra.Command{Use: "unpublish <slug>", Args: cobra.ExactArgs(1), Short: "Withdraw the custom application while preserving panels and history", RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("expected-publication")
		confirmed, _ := cmd.Flags().GetBool("yes")
		if version < 1 || !confirmed {
			return fmt.Errorf("--expected-publication and --yes are required")
		}
		return projectCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/project/unpublish", map[string]any{"expected_publication": version})
	}}
	withdraw.Flags().Int64("expected-publication", 0, "Current publication version")
	withdraw.Flags().Bool("yes", false, "Confirm withdrawing the application for all viewers")
	fsck := &cobra.Command{Use: "fsck <slug>", Args: cobra.ExactArgs(1), Short: "Verify a Page's SQL source, checkpoint and artifact integrity", RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/project/fsck")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := pageCheckError(response); err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return err
		}
		var report struct {
			Healthy bool `json:"healthy"`
		}
		if err := json.Unmarshal(data, &report); err != nil {
			return err
		}
		if err := pageEmitMachine(resolvedFormatter(cmd), data, "{}"); err != nil {
			return err
		}
		if !report.Healthy {
			return fmt.Errorf("Page storage integrity check failed; see the reported roots")
		}
		return nil
	}}
	compact := &cobra.Command{Use: "compact", Args: cobra.NoArgs, Short: "Reclaim Page storage across the current workspace (administrator)", RunE: func(cmd *cobra.Command, _ []string) error {
		discard, _ := cmd.Flags().GetBool("discard-history")
		confirmed, _ := cmd.Flags().GetBool("yes")
		if discard && !confirmed {
			return fmt.Errorf("--discard-history removes optional history across the workspace; pass --yes to confirm")
		}
		return projectCLIRequest(cmd, "POST", "/api/v1/pages/maintenance", map[string]any{"discard_history": discard, "confirm": confirmed})
	}}
	compact.Flags().Bool("discard-history", false, "Discard optional history while retaining drafts, live publications and running builds")
	compact.Flags().Bool("yes", false, "Confirm discarding optional history across this workspace")
	cmd.AddCommand(fsck, compact)
	cmd.AddCommand(publications, withdraw, status, newPageProjectPackCommand(), newPageProjectUnpackCommand(), get, set, init, build, preview, history, restore, check, publish, rollback, application)
	return cmd
}

func init() { pageCmd.AddCommand(newPageProjectCommand()) }

func projectCLIRequest(cmd *cobra.Command, method, endpoint string, body any) error {
	client, err := pageClient()
	if err != nil {
		return err
	}
	var response *http.Response
	if method == "GET" {
		response, err = client.Get(endpoint)
	} else {
		response, err = client.Post(endpoint, body)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := pageCheckError(response); err != nil {
		return err
	}
	b, err := io.ReadAll(io.LimitReader(response.Body, (9<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 9<<20 {
		return fmt.Errorf("project response exceeds limit")
	}
	return pageEmitMachine(resolvedFormatter(cmd), b, "{}")
}
