//go:build !clionly

package main

// `crewship page enable` / `crewship page disable` — turn Pages Apps on and off
// on the Docker host (#3108, PRD pages-build-worker-install §4.4).
//
// Pages Apps are on exactly when the server's page project storage holds a
// record (pagebuild.BuildImageRecordFile) naming the image `page enable`
// built for this binary's build context fingerprint, and that image still has
// the recorded ID, the exact tag crewship-pages-build:<fp12> and the label.
// The label alone is never trusted: crew images built FROM a labelled base
// inherit it (#3108 security review). Writing the record is the operator's
// opt-in and removing it the opt-out. Neither command changes configuration
// or talks to the Crewship API.
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
	"sort"
	"strings"
	"time"

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
	pageDockerBuild   = pageDockerBuildCLI
	pageDockerRemove  = pageDockerRemoveCLI
	pageLoadConfig    = loadPagesServerConfig
	pageImageFinger   = pageprofile.ImageFingerprint
	pageWriteBuildCtx = pageprofile.WriteBuildContext
	pageDockerHost    = func() string { return os.Getenv("DOCKER_HOST") }
	pageInspectImage  = func(ctx context.Context, ref string) (pagebuild.ImageInfo, error) {
		return pagebuild.InspectImage(ctx, ref)
	}
)

// pageRecordedImage is pagebuild.RecordedCompilerImage through the
// swappable inspect hook.
func pageRecordedImage(ctx context.Context, dir, fingerprint string) (string, error) {
	return pagebuild.RecordedCompilerImageWith(ctx, dir, fingerprint, pageInspectImage)
}

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

// pageDockerBuildCLI builds dir with the profile label and the exact tag and
// writes the image ID to iidfile, streaming docker's own output to w. The
// server requires both the recorded ID and the tag; an untagged image would
// also be "dangling" and deleted by `docker image prune`.
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

// pageImageTag is one tag per profile, so images of different versions sit
// side by side for rollback instead of stealing each other's tag.
func pageImageTag(fingerprint string) string {
	return pagebuild.ImageTag(fingerprint)
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
		Long: `Build the isolated compiler image Pages Apps use for this crewship
version on the local Docker daemon, tag it crewship-pages-build:<profile> and
record its exact image ID in the server's page project storage
(<CREWSHIP_PAGE_PROJECTS_PATH>/` + pagebuild.BuildImageRecordFile + `, mode 0600). The server uses
only the recorded image, and only while it keeps that ID, tag and label; it
picks the record up within about 10 seconds, with no restart and no
configuration change.

Run it on the Docker host as the account the server runs as (or with sudo:
the record is then given to the owner of the storage directory), with the same
crewship version and configuration as the server (--config).

Idempotent: when the recorded image for this version is in place, nothing is
built. stdout carries only the image ID, so scripts can do:

  image=$(crewship page enable)

Docker Compose and other setups where the server cannot read the host's
storage directory: run ` + "`crewship page enable --image-only`" + ` on the host and set
CREWSHIP_PAGE_BUILD_IMAGE to the printed ID.

The build needs network access once (base image and pinned npm packages); Page
builds themselves always run with --network=none.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			configFile, _ := cmd.Flags().GetString("config")
			imageOnly, _ := cmd.Flags().GetBool("image-only")
			if imageOnly {
				return runPageEnableImageOnly(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr())
			}
			return runPageEnable(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), configFile)
		},
	}
	cmd.Flags().String("config", "", "Server configuration file (YAML) to read the Pages settings from, as passed to `crewship start --config`")
	cmd.Flags().Bool("image-only", false, "Build and print the image ID without recording it; pin it with CREWSHIP_PAGE_BUILD_IMAGE (Docker Compose)")
	return cmd
}

func runPageEnable(ctx context.Context, stdout, stderr io.Writer, configFile string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := pageDockerPing(ctx); err != nil {
		return fmt.Errorf("cannot enable Pages Apps: %w", err)
	}
	cfg, err := pageLoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("cannot enable Pages Apps: the server configuration is unreadable (%v); pass --config <file> as given to `crewship start`, or build with --image-only and pin CREWSHIP_PAGE_BUILD_IMAGE", err)
	}
	if cfg == nil {
		return errors.New("cannot enable Pages Apps: no server configuration")
	}
	dir := cfg.Storage.PageProjectsPath
	if dir == "" {
		where := "outside storage.base_path"
		if cfg.Storage.BasePath != "" {
			where = "outside storage.base_path (" + cfg.Storage.BasePath + ")"
		}
		return fmt.Errorf("cannot enable Pages Apps: page project storage is not configured. Set CREWSHIP_PAGE_PROJECTS_PATH (or storage.page_projects_path) to an absolute directory %s, restart the server and re-run; the compiler image is recorded there. Docker Compose: run `crewship page enable --image-only` on the host and set CREWSHIP_PAGE_BUILD_IMAGE to the printed ID", where)
	}
	fingerprint := pageImageFinger()
	id, err := pageRecordedImage(ctx, dir, fingerprint)
	switch {
	case err == nil:
		fmt.Fprintf(stderr, "Pages Apps already enabled: compiler image %s is recorded for this version.\n", shortImageID(id))
	case errors.Is(err, pagebuild.ErrNotRecorded):
		if id, err = buildPageToolsImage(ctx, stderr, fingerprint); err != nil {
			return err
		}
		if err := recordPageToolsImage(dir, fingerprint, id); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Pages Apps enabled: compiler image %s, recorded in %s.\n", shortImageID(id), pagebuild.BuildImageRecordPath(dir))
		fmt.Fprintln(stderr, "No restart needed — a running server picks the record up within about 10 seconds.")
	default:
		// A record exists but does not verify: the image was removed,
		// retagged or relabelled, or the record file is unusable. Rebuild
		// and re-record only when the record itself is readable, so an
		// unreadable or tampered file is shown to the operator, not replaced.
		if _, readErr := pagebuild.ReadBuildImageRecord(dir); readErr != nil {
			return fmt.Errorf("cannot enable Pages Apps: %v; inspect or remove %s and re-run", readErr, pagebuild.BuildImageRecordPath(dir))
		}
		fmt.Fprintf(stderr, "The recorded compiler image is not usable (%v); building it again.\n", err)
		if id, err = buildPageToolsImage(ctx, stderr, fingerprint); err != nil {
			return err
		}
		if err := recordPageToolsImage(dir, fingerprint, id); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "Pages Apps enabled: compiler image %s, recorded in %s.\n", shortImageID(id), pagebuild.BuildImageRecordPath(dir))
	}
	fmt.Fprintln(stdout, id)
	reportPageEnableFollowUps(ctx, stderr, id, cfg, configFile)
	return nil
}

// runPageEnableImageOnly builds (or reuses through Docker's cache) the image
// and prints its ID without recording it: the route for a server that cannot
// read the host's storage, which pins the ID with CREWSHIP_PAGE_BUILD_IMAGE.
func runPageEnableImageOnly(ctx context.Context, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := pageDockerPing(ctx); err != nil {
		return fmt.Errorf("cannot build the Pages compiler image: %w", err)
	}
	id, err := buildPageToolsImage(ctx, stderr, pageImageFinger())
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Compiler image %s built and not recorded. Set CREWSHIP_PAGE_BUILD_IMAGE=%s for the server and restart it.\n", shortImageID(id), id)
	fmt.Fprintln(stdout, id)
	return nil
}

// recordPageToolsImage adds id for fingerprint to the record, keeping the
// entries of other versions (rollback).
func recordPageToolsImage(dir, fingerprint, id string) error {
	rec, err := pagebuild.ReadBuildImageRecord(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		rec = &pagebuild.BuildImageRecord{}
	case err != nil:
		return fmt.Errorf("cannot record the compiler image: %v; inspect or remove %s and re-run", err, pagebuild.BuildImageRecordPath(dir))
	}
	if rec.Images == nil {
		rec.Images = map[string]pagebuild.RecordedImage{}
	}
	rec.Images[fingerprint] = pagebuild.RecordedImage{ID: id, Tag: pageImageTag(fingerprint), RecordedAt: time.Now().UTC()}
	if err := pagebuild.WriteBuildImageRecord(dir, rec); err != nil {
		return fmt.Errorf("cannot record the compiler image in %s: %w (run as the account that owns it, or with sudo)", pagebuild.BuildImageRecordPath(dir), err)
	}
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
			return "", fmt.Errorf("docker build was refused by the Docker endpoint (%v). A socket proxy that forbids builds — the Docker Compose deployment's docker-socket-proxy runs with BUILD=0 — cannot build images: run `crewship page enable --image-only` on the Docker host itself with the same crewship version (%s) and set CREWSHIP_PAGE_BUILD_IMAGE to the printed ID", err, version)
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
	// The server checks the recorded ID, exact tag and label; confirm all
	// three landed rather than recording an image nothing will ever use.
	info, err := pageInspectImage(ctx, id)
	if err != nil {
		return "", fmt.Errorf("cannot inspect the built image: %w", err)
	}
	if err := pagebuild.VerifyCompilerImage(info, id, fingerprint); err != nil {
		return "", fmt.Errorf("built image does not verify: %w", err)
	}
	return id, nil
}

// reportPageEnableFollowUps tells the operator what else stands between the
// image and a working Pages App. Advisory only: none of it changes the exit
// code, since the image — the part this command owns — is in place.
func reportPageEnableFollowUps(_ context.Context, stderr io.Writer, id string, cfg *config.Config, configFile string) {
	if rec, err := pagebuild.ReadBuildImageRecord(cfg.Storage.PageProjectsPath); err == nil {
		var older []string
		for _, entry := range rec.Images {
			if entry.ID != id {
				older = append(older, entry.ID)
			}
		}
		if len(older) > 0 {
			fmt.Fprintf(stderr, "\n%d compiler image(s) of other versions kept for rollback; `crewship page disable --all` removes them.\n", len(older))
		}
	}
	fmt.Fprintln(stderr)
	if cfg.Storage.PageBuildImage != "" {
		fmt.Fprintf(stderr, "Note: CREWSHIP_PAGE_BUILD_IMAGE is set (%s); the server uses that image instead of the recorded one until it is unset.\n", cfg.Storage.PageBuildImage)
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
		Short: "Remove this version's recorded Pages Apps compiler image (turns Pages Apps off)",
		Long: `Remove this crewship version from the compiler image record in the
server's page project storage, then delete the image it named — only after
checking that the image still has the recorded ID, the exact tag
crewship-pages-build:<profile> and the Pages label. An image that merely carries
the label (crew images built FROM a labelled base inherit it) is never removed.
The server stops finding a compiler within about 10 seconds and refuses new
Page application builds with page_apps_not_enabled. Already published
applications keep serving: they are static artifacts and need no compiler.

Recorded images of other crewship versions are kept for rollback; --all
removes every recorded version.

Turn Pages Apps back on with ` + "`crewship page enable`" + `.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPageDisable(cmd, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")
	cmd.Flags().Bool("all", false, "Also remove the recorded compiler images of other crewship versions")
	cmd.Flags().String("config", "", "Server configuration file (YAML) to read the Pages settings from, as passed to `crewship start --config`")
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
	configFile, _ := cmd.Flags().GetString("config")
	cfg, err := pageLoadConfig(configFile)
	if err != nil {
		return fmt.Errorf("cannot disable Pages Apps: the server configuration is unreadable (%v); pass --config <file>", err)
	}
	if cfg == nil {
		return errors.New("cannot disable Pages Apps: no server configuration")
	}
	dir := cfg.Storage.PageProjectsPath
	if dir == "" {
		fmt.Fprintln(stderr, "Pages Apps are not enabled: page project storage is not configured, so no compiler image is recorded.")
		return nil
	}
	rec, err := pagebuild.ReadBuildImageRecord(dir)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "Pages Apps are not enabled: no compiler image is recorded in %s.\n", pagebuild.BuildImageRecordPath(dir))
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot disable Pages Apps: %v; inspect or remove %s", err, pagebuild.BuildImageRecordPath(dir))
	}
	fingerprint := pageImageFinger()
	var targets []string
	for fp := range rec.Images {
		if all || fp == fingerprint {
			targets = append(targets, fp)
		}
	}
	sort.Strings(targets)
	if len(targets) == 0 {
		fmt.Fprintln(stderr, "Pages Apps are not enabled for this version: no compiler image is recorded for it.")
		return nil
	}
	if err := pageConfirm(cmd, fmt.Sprintf("Remove %d recorded Pages compiler image(s) and turn Pages Apps off?", len(targets))); err != nil {
		return err
	}
	// Forget the images first: the server fails closed at once, whatever
	// happens to the images below.
	entries := make(map[string]pagebuild.RecordedImage, len(targets))
	for _, fp := range targets {
		entries[fp] = rec.Images[fp]
		delete(rec.Images, fp)
	}
	if err := pagebuild.WriteBuildImageRecord(dir, rec); err != nil {
		return fmt.Errorf("cannot update %s: %w", pagebuild.BuildImageRecordPath(dir), err)
	}
	var failed int
	for _, fp := range targets {
		entry := entries[fp]
		info, err := pageInspectImage(ctx, entry.ID)
		if err != nil {
			fmt.Fprintf(stderr, "  %s is already gone (%v)\n", shortImageID(entry.ID), err)
			continue
		}
		if err := pagebuild.VerifyCompilerImage(info, entry.ID, fp); err != nil || entry.Tag != pageImageTag(fp) {
			if err == nil {
				err = fmt.Errorf("recorded tag %q is not %s", entry.Tag, pageImageTag(fp))
			}
			fmt.Fprintf(stderr, "  not removing %s: %v — it is not the image `crewship page enable` built\n", shortImageID(entry.ID), err)
			continue
		}
		if err := pageDockerRemove(ctx, entry.ID, stderr); err != nil {
			fmt.Fprintf(stderr, "  could not remove %s: %v\n", entry.ID, err)
			failed++
			continue
		}
		fmt.Fprintln(stdout, entry.ID)
		fmt.Fprintf(stderr, "  removed %s\n", shortImageID(entry.ID))
	}
	fmt.Fprintln(stderr, "Pages Apps are off: new Page application builds are refused within about 10 seconds; published applications keep serving.")
	fmt.Fprintln(stderr, "Turn them back on with `crewship page enable`.")
	if failed > 0 {
		return fmt.Errorf("%d of %d Pages compiler image(s) were not removed (an image used by a running build cannot be removed; remove it with docker image rm when it finishes)", failed, len(targets))
	}
	return nil
}

func init() {
	pageCmd.AddCommand(newPageEnableCommand(), newPageDisableCommand())
}
