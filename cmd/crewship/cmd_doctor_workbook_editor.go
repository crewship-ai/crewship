//go:build !clionly

package main

// Workbook editor proxy check for `crewship doctor`. In the zero-config
// same-host mode the embedded workbook editor is served under /_editor/ on
// the Studio origin, and its isolation relies on nothing in front of Crewship
// redirecting or normalising that prefix. The server checks this itself
// where it can reach its own public URL (reported on /healthz); doctor runs
// the same requests from the operator's side, through whatever proxy, CDN or
// load balancer a browser goes through.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

const workbookEditorProxyHint = "the proxy must pass /_editor/ to Crewship unchanged: no redirect, no path decoding or normalisation (nginx: proxy_pass without a URI), no SSO or authentication gate; see docs/configuration/environment#same-host-mode, then restart Crewship"

func runCheckWorkbookEditorProxy(ctx context.Context) checkResult {
	return checkWorkbookEditorProxy(ctx, cli.EffectiveServer(flagServer, flagProfile, cliCfg))
}

// checkWorkbookEditorProxy reads the editor status from <serverURL>/healthz
// and, for a same-host editor, runs editororigin.CheckProxy against the
// public Studio URL that status names. Anything that is not a same-host
// editor is INFO, as is a server or public URL this machine cannot reach.
func checkWorkbookEditorProxy(ctx context.Context, serverURL string) checkResult {
	const name = "workbook editor proxy"
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(serverURL, "/")+"/healthz", nil)
	if err != nil {
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("invalid server URL %q: %v", serverURL, err)}
	}
	resp, err := client.Do(req)
	if err != nil {
		return checkResult{name: name, status: "INFO", detail: "server not reachable — skipped (see 'server reachable' check)"}
	}
	defer resp.Body.Close()
	var body struct {
		Editor *editororigin.Status `json:"workbook_editor"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return checkResult{name: name, status: "INFO", detail: "server does not report the workbook editor — skipped"}
	}
	if body.Editor == nil || body.Editor.Mode == "" {
		return checkResult{name: name, status: "INFO", detail: "workbook editor is off"}
	}
	if body.Editor.Mode != "same-host" {
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("workbook editor served from a dedicated origin (%s); no same-host proxy rules apply", body.Editor.URL)}
	}
	if body.Editor.Disabled {
		return checkResult{name: name, status: "FAIL", detail: "the server disabled the workbook editor: " + body.Editor.Detail, hint: workbookEditorProxyHint}
	}
	studio, ok := strings.CutSuffix(body.Editor.URL, editororigin.SameHostPrefix+editororigin.BundlePrefix)
	if !ok || editororigin.CanonicalOrigin(studio) == "" {
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("unexpected editor URL %q — skipped", body.Editor.URL)}
	}
	c := editororigin.CheckProxy(ctx, &http.Client{Timeout: 10 * time.Second}, studio)
	switch c.Status {
	case editororigin.ProxyCheckFailed:
		kind := "/_editor/ does not reach the editor"
		if c.Isolation {
			kind = "a redirect or path rewrite in front of Crewship defeats the editor isolation"
		}
		return checkResult{name: name, status: "FAIL", detail: kind + ": " + strings.Join(c.Problems, "; "), hint: workbookEditorProxyHint}
	case editororigin.ProxyCheckUnreachable:
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("%s not reachable from here (server self-check: %s) — run doctor where a browser would reach it", studio, body.Editor.ProxyCheck)}
	}
	detail := fmt.Sprintf("%s/_editor/ passes unchanged (server self-check: %s)", studio, body.Editor.ProxyCheck)
	if len(c.Warnings) > 0 {
		return checkResult{name: name, status: "WARN", detail: detail + "; " + strings.Join(c.Warnings, "; ")}
	}
	return checkResult{name: name, status: "PASS", detail: detail}
}
