//go:build !clionly

package main

// `crewship page enable` / `crewship page disable` — turn Pages Apps on and off
// on the Docker host (#3108, PRD pages-build-worker-install §4.4).
//
// Pages Apps are on exactly when a local image labelled
// ai.crewship.pages.profile=<this binary's profile fingerprint> exists: the
// server resolves its build image by that label (internal/pagebuild
// ImageResolver), so building it is the operator's opt-in and removing it is
// the opt-out. Neither command writes configuration — Docker access is all
// they need — and neither talks to the Crewship API.
//
// stdout carries ONLY the image ID (`image=$(crewship page enable)`); progress,
// the docker build log and every hint go to stderr.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/pagebuild"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
	"github.com/spf13/cobra"
)

// The Docker-facing operations, as variables so unit tests run without a
// daemon. Production values shell out to the docker CLI — the same binary the
// server uses for Page builds, so "reachable here" means "usable there".
var (
	pageDockerPing    = pageDockerPingCLI
	pageLabeledImages = pagebuild.LabeledImages
	pageDockerBuild   = pageDockerBuildCLI
	pageDockerRemove  = pageDockerRemoveCLI
	pageLoadConfig    = loadPagesServerConfig
	pageImageFinger   = pageprofile.ImageFingerprint
	pageWriteBuildCtx = pageprofile.WriteBuildContext
	pageDockerHost    = func() string { return os.Getenv("DOCKER_HOST") }
)

// loadPagesServerConfig reads the server configuration (defaults → YAML →
// env) for its Pages settings only. config.Load also insists on finding the
// crewship-sidecar binary, which says nothing about Pages and is absent
// wherever the CLI runs apart from the server, so that check is skipped for
// the duration of this read.
func loadPagesServerConfig(path string) (*config.Config, error) {
	prev, had := os.LookupEnv("CREWSHIP_SKIP_SIDECAR")
	_ = os.Setenv("CREWSHIP_SKIP_SIDECAR", "1")
	defer func() {
		if had {
			_ = os.Setenv("CREWSHIP_SKIP_SIDECAR", prev)
		} else {
			_ = os.Unsetenv("CREWSHIP_SKIP_SIDECAR")
		}
	}()
	return config.Load(path)
}

// pageDockerPingCLI checks that the docker CLI exists and its daemon answers.
func pageDockerPingCLI(ctx context.Context) error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("the docker CLI was not found in PATH — Pages Apps compile in a local Docker image, so install Docker Engine (https://docs.docker.com/engine/install/) and re-run")
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && err != nil {
			msg = err.Error()
		}
		return fmt.Errorf("the Docker daemon is not reachable (%s) — start Docker and re-run", msg)
	}
	return nil
}

// pageDockerBuildCLI builds dir with the profile label and writes the image ID
// to iidfile, streaming docker's own output to w.
//
// The tag is not how the server finds the image — the label is — but an
// untagged image is "dangling": `docker image prune` deletes it, and with the
// containerd image store `docker image ls` (no -a) does not list it at all, so
// the label lookup would never see what was just built.
func pageDockerBuildCLI(ctx context.Context, dir, label, tag, iidfile string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", "build", "--label", label, "--tag", tag, "--iidfile", iidfile, dir)
	cmd.Stdout = w
	cmd.Stderr = w
	return cmd.Run()
}

func pageDockerRemoveCLI(ctx context.Context, id string, w io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", "image", "rm", id)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(out.String()); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

// tailBuffer keeps the last max bytes written, so a long build log can be
// inspected for the failure reason without holding all of it.
type tailBuffer struct {
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

// pageBuildRefusedByProxy recognises a Docker endpoint that refuses builds —
// the Compose deployment's docker-socket-proxy runs with BUILD=0 and answers
// 403 — as opposed to a build that ran and failed.
func pageBuildRefusedByProxy(output, dockerHost string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "403 forbidden") || strings.Contains(lower, "forbidden by administrative rules") {
		return true
	}
	return strings.Contains(lower, "forbidden") && strings.Contains(strings.ToLower(dockerHost), "proxy")
}

// pageImageRepository names the local tag `page enable` gives its image.
const pageImageRepository = "crewship-pages-build"

// pageImageTag is one tag per profile, so images of different versions sit
// side by side for rollback instead of stealing each other's tag.
func pageImageTag(fingerprint string) string {
	return pageImageRepository + ":" + shortImageID(fingerprint)
}

func shortImageID(id string) string {
	trimmed := strings.TrimPrefix(id, "sha256:")
	if len(trimmed) > 12 {
		trimmed = trimmed[:12]
	}
	return trimmed
}

// pageSetupGap is one thing the server still needs before Pages Apps work
// end to end, beyond the image.
type pageSetupGap struct {
	problem string
	remedy  string
}

// pageSetupGaps reports what `page enable` cannot fix itself: the project
// storage directory and the separate-site runtime origin are server
// configuration, which these commands deliberately never write.
func pageSetupGaps(cfg *config.Config) []pageSetupGap {
	var gaps []pageSetupGap
	if cfg.Storage.PageProjectsPath == "" {
		where := "outside storage.base_path"
		if cfg.Storage.BasePath != "" {
			where = "outside storage.base_path (" + cfg.Storage.BasePath + ")"
		}
		gaps = append(gaps, pageSetupGap{
			problem: "page project storage is not configured",
			remedy:  "set CREWSHIP_PAGE_PROJECTS_PATH (or storage.page_projects_path) to an absolute directory " + where + ", then restart the server",
		})
	}
	if cfg.PageRuntimeOrigin() == "" && !cfg.Storage.PageRuntimeDevelopmentSameOrigin {
		gaps = append(gaps, pageSetupGap{
			problem: "no Page runtime origin",
			remedy:  "set CREWSHIP_PAGE_RUNTIME_ORIGIN to a separate site that serves this server (for example https://pages.example.com), then restart the server",
		})
	}
	return gaps
}

func newPageEnableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enable",
		Short: "Build the Pages Apps compiler image on this Docker host (turns Pages Apps on)",
		Long: `Build the isolated compiler image Pages Apps use, labelled for this
crewship version, on the local Docker daemon. The server finds the image by its
label within about 10 seconds; no restart and no configuration change are needed.

Idempotent: when an image for this version already exists, nothing is built.
stdout carries only the image ID, so scripts can do:

  image=$(crewship page enable)

The build needs network access once (base image and pinned npm packages); Page
builds themselves always run with --network=none. Run it on the Docker host
with the same crewship version as the server.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, _ := cmd.Flags().GetString("config")
			return runPageEnable(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), configFile)
		},
	}
	cmd.Flags().String("config", "", "Server configuration file (YAML) to read the Pages settings from, as passed to `crewship start --config`")
	return cmd
}

func runPageEnable(ctx context.Context, stdout, stderr io.Writer, configFile string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := pageDockerPing(ctx); err != nil {
		return fmt.Errorf("cannot enable Pages Apps: %w", err)
	}
	fingerprint := pageImageFinger()
	current, err := pageLabeledImages(ctx, fingerprint)
	if err != nil {
		return err
	}
	var id string
	if len(current) > 0 {
		id = current[0]
		fmt.Fprintf(stderr, "Pages Apps already enabled: compiler image %s exists for this version.\n", shortImageID(id))
	} else {
		id, err = buildPageToolsImage(ctx, stderr, fingerprint)
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Pages Apps enabled: compiler image %s.\n", shortImageID(id))
		fmt.Fprintln(stderr, "No restart needed — a running server picks the image up within about 10 seconds.")
	}
	fmt.Fprintln(stdout, id)
	reportPageEnableFollowUps(ctx, stderr, id, configFile)
	return nil
}

func buildPageToolsImage(ctx context.Context, stderr io.Writer, fingerprint string) (string, error) {
	tmp, err := os.MkdirTemp("", "crewship-pages-build-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	dir := filepath.Join(tmp, "context")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	if err := pageWriteBuildCtx(dir); err != nil {
		return "", fmt.Errorf("write the Pages build context: %w", err)
	}
	iidfile := filepath.Join(tmp, "image-id")
	fmt.Fprintf(stderr, "Building the Pages Apps compiler image (profile %s)...\n", shortImageID(fingerprint))
	tail := &tailBuffer{max: 16 << 10}
	if err := pageDockerBuild(ctx, dir, pageprofile.ImageLabel+"="+fingerprint, pageImageTag(fingerprint), iidfile, io.MultiWriter(stderr, tail)); err != nil {
		if pageBuildRefusedByProxy(string(tail.buf), pageDockerHost()) {
			return "", fmt.Errorf("docker build was refused by the Docker endpoint (%v). A socket proxy that forbids builds — the Docker Compose deployment's docker-socket-proxy runs with BUILD=0 — cannot build images: run `crewship page enable` on the Docker host itself with the same crewship version (%s); the server finds the labelled image without a restart", err, version)
		}
		return "", fmt.Errorf("docker build failed (%v) — see the build output above; the build needs network access to fetch the base image and pinned npm packages", err)
	}
	raw, err := os.ReadFile(iidfile)
	if err != nil {
		return "", fmt.Errorf("docker build did not report an image ID: %w", err)
	}
	id := strings.TrimSpace(string(raw))
	if err := pagebuild.ValidateImage(id); err != nil {
		return "", fmt.Errorf("docker build reported %q: %w", id, err)
	}
	// The server finds the image by label, so confirm the label landed rather
	// than reporting success for an image nothing will ever use.
	labelled, err := pageLabeledImages(ctx, fingerprint)
	if err != nil {
		return "", err
	}
	for _, candidate := range labelled {
		if candidate == id {
			return id, nil
		}
	}
	return "", fmt.Errorf("built image %s does not carry the %s label for this version", shortImageID(id), pageprofile.ImageLabel)
}

// reportPageEnableFollowUps tells the operator what else stands between the
// image and a working Pages App. Advisory only: none of it changes the exit
// code, since the image — the part this command owns — is in place.
func reportPageEnableFollowUps(ctx context.Context, stderr io.Writer, id, configFile string) {
	if all, err := pageLabeledImages(ctx, ""); err == nil {
		var older []string
		for _, other := range all {
			if other != id {
				older = append(older, other)
			}
		}
		if len(older) > 0 {
			fmt.Fprintf(stderr, "\n%d older Pages compiler image(s) kept for rollback:\n", len(older))
			for _, other := range older {
				fmt.Fprintf(stderr, "  %s\n", other)
			}
			fmt.Fprintf(stderr, "Remove them when no longer needed: docker image rm %s\n", strings.Join(older, " "))
		}
	}

	cfg, err := pageLoadConfig(configFile)
	if err != nil {
		fmt.Fprintf(stderr, "\nCould not read the server configuration to check the rest of the setup: %v\n", err)
		fmt.Fprintln(stderr, "Pages Apps also need CREWSHIP_PAGE_PROJECTS_PATH and, outside http://localhost, CREWSHIP_PAGE_RUNTIME_ORIGIN; `crewship doctor` reports both.")
		return
	}
	fmt.Fprintln(stderr)
	if cfg.Storage.PageBuildImage != "" {
		fmt.Fprintf(stderr, "Note: CREWSHIP_PAGE_BUILD_IMAGE is set (%s); the server uses that image instead of the labelled one until it is unset.\n", cfg.Storage.PageBuildImage)
	}
	if cfg.Storage.PageProjectsPath != "" {
		fmt.Fprintf(stderr, "Page project storage: %s\n", cfg.Storage.PageProjectsPath)
	}
	switch origin := cfg.PageRuntimeOrigin(); {
	case origin != "":
		fmt.Fprintf(stderr, "Page runtime origin:  %s\n", origin)
	case cfg.Storage.PageRuntimeDevelopmentSameOrigin:
		fmt.Fprintln(stderr, "Page runtime origin:  same-origin development mode (reviewed demos only, not for production)")
	}
	gaps := pageSetupGaps(cfg)
	if len(gaps) == 0 {
		return
	}
	fmt.Fprintln(stderr, "Still needed before Pages Apps can be built and published:")
	for _, gap := range gaps {
		fmt.Fprintf(stderr, "  - %s: %s\n", gap.problem, gap.remedy)
	}
	if configFile == "" {
		fmt.Fprintln(stderr, "(Checked the environment only; pass --config <file> if the server reads a YAML configuration.)")
	}
}

func newPageDisableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable",
		Short: "Remove this version's Pages Apps compiler image from this Docker host (turns Pages Apps off)",
		Long: `Remove the local image labelled ` + pageprofile.ImageLabel + ` for this
crewship version. The server stops finding a compiler within about 10 seconds
and refuses new Page application builds with page_apps_not_enabled. Already
published applications keep serving: they are static artifacts and need no
compiler.

Images of other crewship versions are kept: on a Docker host shared by several
installations, or kept for rollback, they belong to another server. Pass --all
to remove every version's image.

Turn Pages Apps back on with ` + "`crewship page enable`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPageDisable(cmd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")
	cmd.Flags().Bool("all", false, "Also remove compiler images of other crewship versions on this Docker host")
	return cmd
}

// pageConfirm is confirmAction, swappable in tests.
var pageConfirm = confirmAction

func runPageDisable(cmd *cobra.Command, stdout, stderr io.Writer) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := pageDockerPing(ctx); err != nil {
		return fmt.Errorf("cannot disable Pages Apps: %w", err)
	}
	all, _ := cmd.Flags().GetBool("all")
	fingerprint := pageImageFinger()
	if all {
		fingerprint = ""
	}
	images, err := pageLabeledImages(ctx, fingerprint)
	if err != nil {
		return err
	}
	if len(images) == 0 {
		if all {
			fmt.Fprintln(stderr, "Pages Apps are not enabled: no compiler images on this Docker host.")
		} else {
			fmt.Fprintln(stderr, "Pages Apps are not enabled for this version: no compiler image for it on this Docker host.")
		}
		return nil
	}
	if err := pageConfirm(cmd, fmt.Sprintf("Remove %d Pages compiler image(s) and turn Pages Apps off?", len(images))); err != nil {
		return err
	}
	var failed []string
	for _, id := range images {
		if err := pageDockerRemove(ctx, id, stderr); err != nil {
			fmt.Fprintf(stderr, "  could not remove %s: %v\n", id, err)
			failed = append(failed, id)
			continue
		}
		fmt.Fprintln(stdout, id)
		fmt.Fprintf(stderr, "  removed %s\n", shortImageID(id))
	}
	if len(failed) > 0 {
		return fmt.Errorf("%d of %d Pages compiler image(s) were not removed (an image used by a running build cannot be removed; retry when it finishes)", len(failed), len(images))
	}
	fmt.Fprintln(stderr, "Pages Apps are off: new Page application builds are refused within about 10 seconds; published applications keep serving.")
	fmt.Fprintln(stderr, "Turn them back on with `crewship page enable`.")
	return nil
}

func init() {
	pageCmd.AddCommand(newPageEnableCommand(), newPageDisableCommand())
}
