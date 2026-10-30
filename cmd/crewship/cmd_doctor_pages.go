//go:build !clionly

package main

// The `pages apps` row of `crewship doctor` (#3108).
//
// Pages Apps are optional, so "not enabled" is INFO, never a warning: an
// installation that never ran `crewship page enable` is healthy. Once a
// compiler image is recorded, what stands between it and a working Page
// application — a record that no longer verifies, only another version
// recorded, no project storage, no runtime origin — is a WARN with the exact
// remedy. The check reads the record and inspects one image; it never starts
// a container or builds anything.

import (
	"context"
	"errors"
	"strings"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/pagebuild"
)

const pagesCheckName = "pages apps"

// pagesImageState is what the record says about this version's compiler.
type pagesImageState struct {
	recorded string // verified image ID for this version
	problem  string // a record exists for this version but does not verify
	outdated bool   // only other versions are recorded
}

func runCheckPagesApps(ctx context.Context) checkResult {
	if err := pageDockerPing(ctx); err != nil {
		return checkResult{name: pagesCheckName, status: "INFO", detail: "not checked: Docker is not reachable (Pages Apps need Docker)"}
	}
	cfg, cfgErr := pageLoadConfig("")
	var state pagesImageState
	if cfgErr == nil && cfg != nil && cfg.Storage.PageProjectsPath != "" {
		state = pagesRecordState(ctx, cfg.Storage.PageProjectsPath)
	}
	return checkPagesApps(state, cfg, cfgErr)
}

func pagesRecordState(ctx context.Context, dir string) pagesImageState {
	fingerprint := pageImageFinger()
	id, err := pageRecordedImage(ctx, dir, fingerprint)
	switch {
	case err == nil:
		return pagesImageState{recorded: id}
	case errors.Is(err, pagebuild.ErrNotRecorded):
		rec, readErr := pagebuild.ReadBuildImageRecord(dir)
		return pagesImageState{outdated: readErr == nil && len(rec.Images) > 0}
	default:
		return pagesImageState{problem: strings.TrimPrefix(err.Error(), pagebuild.ErrNotEnabled.Error()+" ")}
	}
}

// checkPagesApps is the pure decision.
func checkPagesApps(state pagesImageState, cfg *config.Config, cfgErr error) checkResult {
	explicit := ""
	if cfgErr == nil && cfg != nil {
		explicit = cfg.Storage.PageBuildImage
	}
	var detail string
	switch {
	case explicit != "":
		detail = "explicit image " + explicit + " (CREWSHIP_PAGE_BUILD_IMAGE)"
	case state.recorded != "":
		detail = "enabled (" + shortImageID(state.recorded) + ")"
	case state.problem != "":
		return checkResult{
			name:   pagesCheckName,
			status: "WARN",
			detail: "not enabled: the recorded compiler image does not verify " + state.problem,
			hint:   "run `" + pagebuild.EnableCommand + "` on the Docker host as the server's account",
		}
	case state.outdated:
		return checkResult{
			name:   pagesCheckName,
			status: "WARN",
			detail: "outdated: only compiler images of other crewship versions are recorded",
			hint:   "run `" + pagebuild.EnableCommand + "` with this version; the old image is kept for rollback",
		}
	default:
		return checkResult{
			name:   pagesCheckName,
			status: "INFO",
			detail: "not enabled (optional)",
			hint:   "run `" + pagebuild.EnableCommand + "` on the Docker host to turn Pages Apps on",
		}
	}
	if cfgErr != nil || cfg == nil {
		return checkResult{name: pagesCheckName, status: "WARN", detail: detail + "; server configuration unreadable", hint: pagesErrText(cfgErr)}
	}
	gaps := pageSetupGaps(cfg)
	if len(gaps) == 0 {
		return checkResult{name: pagesCheckName, status: "PASS", detail: detail + ", runtime origin " + pagesOriginLabel(cfg)}
	}
	problems := make([]string, 0, len(gaps))
	remedies := make([]string, 0, len(gaps))
	for _, gap := range gaps {
		problems = append(problems, gap.problem)
		remedies = append(remedies, gap.remedy)
	}
	return checkResult{
		name:   pagesCheckName,
		status: "WARN",
		detail: detail + "; " + strings.Join(problems, "; "),
		// Doctor reads the environment only; a YAML-configured server may
		// already have these, which the hint says rather than hides.
		hint: strings.Join(remedies, "; ") + " (checked the environment; a YAML server config is not read here)",
	}
}

func pagesOriginLabel(cfg *config.Config) string {
	if origin := cfg.PageRuntimeOrigin(); origin != "" {
		return origin
	}
	return "same-origin development mode"
}

func pagesErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
