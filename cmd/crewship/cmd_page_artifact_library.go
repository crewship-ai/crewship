package main

import (
	"errors"
	"io"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// newArtifactLibraryCommands are the Library publication and consumer pin
// commands (#5189), one per endpoint:
//
//	page artifact library show <slug>               GET  …/artifact/library
//	page artifact library publish <slug> --version  POST …/artifact/library/publish
//	page artifact pin show <slug>                   GET  …/artifact/library-pin
//	page artifact pin set <slug> --library …        PUT  …/artifact/library-pin
//	page artifact pin check <slug>                  GET  …/artifact/library-pin/update
func newArtifactLibraryCommands() []*cobra.Command {
	library := &cobra.Command{Use: "library", Short: "Read or publish a Library's published version (publishing is owner only)"}
	show := &cobra.Command{Use: "show <slug>", Args: cobra.ExactArgs(1), Short: "Read the published version and whether you may publish", RunE: func(cmd *cobra.Command, args []string) error {
		return artifactCLIRequest(cmd, "GET", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/library", nil)
	}}
	publish := &cobra.Command{Use: "publish <slug>", Args: cobra.ExactArgs(1), Short: "Publish a retained version that carries design-system.json (owner only)", RunE: func(cmd *cobra.Command, args []string) error {
		version, _ := cmd.Flags().GetInt64("version")
		if version < 1 {
			return cli.WithExitCode(errors.New("--version is required and must be positive"), cli.ExitValidation)
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/library/publish", map[string]any{"version": version})
	}}
	publish.Flags().Int64("version", 0, "Retained library version to publish")
	library.AddCommand(show, publish)

	pin := &cobra.Command{Use: "pin", Short: "Keep a self-contained copy of a Library's published tokens in a project"}
	pinShow := &cobra.Command{Use: "show <slug>", Args: cobra.ExactArgs(1), Short: "Read the project's pinned Library copy (never reads the Library)", RunE: func(cmd *cobra.Command, args []string) error {
		return artifactCLIRequest(cmd, "GET", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/library-pin", nil)
	}}
	pinSet := &cobra.Command{Use: "set <slug>", Args: cobra.ExactArgs(1), Short: "Pin, or explicitly update to, the Library's published version", RunE: func(cmd *cobra.Command, args []string) error {
		lib, _ := cmd.Flags().GetString("library")
		if lib == "" {
			return cli.WithExitCode(errors.New("--library is required (slug or @id)"), cli.ExitValidation)
		}
		if !cmd.Flags().Changed("if-pinned-version") {
			return cli.WithExitCode(errors.New("--if-pinned-version is required: the version you reviewed as pinned, 0 when nothing is pinned"), cli.ExitValidation)
		}
		ifPinned, _ := cmd.Flags().GetInt64("if-pinned-version")
		if ifPinned < 0 {
			return cli.WithExitCode(errors.New("--if-pinned-version must be nonnegative"), cli.ExitValidation)
		}
		version, _ := cmd.Flags().GetInt64("version")
		if version < 1 {
			return cli.WithExitCode(errors.New("--version is required: the Library version you reviewed (published_version from pin check or library show)"), cli.ExitValidation)
		}
		pinnedLibrary, _ := cmd.Flags().GetString("if-pinned-library")
		if (ifPinned == 0) != (pinnedLibrary == "") {
			return cli.WithExitCode(errors.New("--if-pinned-library is required exactly when --if-pinned-version is not 0"), cli.ExitValidation)
		}
		// Pinning is a human decision; agent runs are refused by the server, so
		// this always uses the authenticated Page client.
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Put("/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/library-pin", map[string]any{"library": lib, "version": version, "if_pinned_library": pinnedLibrary, "if_pinned_version": ifPinned})
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := pageCheckError(response); err != nil {
			return err
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			return err
		}
		return pageEmitMachine(resolvedFormatter(cmd), raw, "{}")
	}}
	pinSet.Flags().String("library", "", "Library project slug or @id in this workspace")
	pinSet.Flags().Int64("version", 0, "Published Library version you reviewed; a different publication is a conflict")
	pinSet.Flags().String("if-pinned-library", "", "Library currently pinned (slug or @id), empty when nothing is pinned; a mismatch is a conflict")
	pinSet.Flags().Int64("if-pinned-version", 0, "Library version currently pinned (0 for none); a mismatch is a conflict")
	check := &cobra.Command{Use: "check <slug>", Args: cobra.ExactArgs(1), Short: "Review whether the Library published a different version, with a token diff", RunE: func(cmd *cobra.Command, args []string) error {
		return artifactCLIRequest(cmd, "GET", "/api/v1/pages/"+pagePathEscape(args[0])+"/artifact/library-pin/update", nil)
	}}
	pin.AddCommand(pinShow, pinSet, check)
	return []*cobra.Command{library, pin}
}
