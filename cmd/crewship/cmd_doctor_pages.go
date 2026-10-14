//go:build !clionly

package main

// The `pages apps` row of `crewship doctor` (#3108).
//
// Pages Apps are optional, so "not enabled" is INFO, never a warning: an
// installation that never ran `crewship page enable` is healthy. Once an image
// exists, what stands between it and a working Page application — an image
// built by another version, no project storage, no runtime origin — is a WARN
// with the exact remedy. The check only lists images; it never starts a
// container or builds anything.

import (
	"context"
	"strings"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/pagebuild"
)

const pagesCheckName = "pages apps"

func runCheckPagesApps(ctx context.Context) checkResult {
	if err := pageDockerPing(ctx); err != nil {
		return checkResult{name: pagesCheckName, status: "INFO", detail: "not checked: Docker is not reachable (Pages Apps need Docker)"}
	}
	current, err := pageLabeledImages(ctx, pageProfileFinger())
	if err != nil {
		return checkResult{name: pagesCheckName, status: "WARN", detail: "could not list Pages compiler images: " + err.Error()}
	}
	var all []string
	if len(current) == 0 {
		if all, err = pageLabeledImages(ctx, ""); err != nil {
			return checkResult{name: pagesCheckName, status: "WARN", detail: "could not list Pages compiler images: " + err.Error()}
		}
	}
	cfg, cfgErr := pageLoadConfig("")
	return checkPagesApps(current, all, cfg, cfgErr)
}

// checkPagesApps is the pure decision: current are images for this binary's
// profile, all every Pages image (only consulted when current is empty).
func checkPagesApps(current, all []string, cfg *config.Config, cfgErr error) checkResult {
	explicit := ""
	if cfgErr == nil && cfg != nil {
		explicit = cfg.Storage.PageBuildImage
	}
	var detail string
	switch {
	case explicit != "":
		detail = "explicit image " + explicit + " (CREWSHIP_PAGE_BUILD_IMAGE)"
	case len(current) > 0:
		detail = "enabled (" + shortImageID(current[0]) + ")"
	case len(all) > 0:
		return checkResult{
			name:   pagesCheckName,
			status: "WARN",
			detail: "outdated: compiler image built for another crewship version",
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
