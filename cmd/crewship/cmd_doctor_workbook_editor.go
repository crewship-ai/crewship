//go:build !clionly

package main

// Workbook editor proxy check for `crewship doctor`. In the zero-config
// same-host mode the embedded workbook editor is served under /_editor/ on
// the Studio origin, and its isolation relies on nothing in front of Crewship
// redirecting or rewriting that prefix. The server checks this itself through
// the public origins signed-in browsers use (or the configured one); /healthz
// reports only a coarse state and instance administrators get the detail.
// Doctor runs the same requests from the operator's side, through whatever
// proxy, CDN or load balancer a browser goes through.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/editororigin"
)

const workbookEditorProxyHint = "the proxy must pass /_editor/ to Crewship unchanged and forward the original Host: no redirect, no path decoding or normalisation (nginx: proxy_pass without a URI), no SSO or authentication gate; see docs/configuration/environment#same-host-mode"

const workbookEditorDetailHint = "run 'crewship admin instance workbook-editor' as an instance administrator for the cause"

// errEditorDetailForbidden: the caller is not an instance administrator.
var errEditorDetailForbidden = errors.New("not an instance administrator")

func runCheckWorkbookEditorProxy(ctx context.Context) checkResult {
	var detail func(context.Context) (*editororigin.Detail, error)
	if cli.EnvToken() != "" || (cliCfg != nil && cliCfg.Token != "") {
		detail = func(ctx context.Context) (*editororigin.Detail, error) {
			return fetchWorkbookEditorDetail(ctx, newAPIClient())
		}
	}
	return checkWorkbookEditorProxy(ctx, cli.EffectiveServer(flagServer, flagProfile, cliCfg), detail)
}

// fetchWorkbookEditorDetail reads GET /api/v1/admin/instance/workbook-editor.
func fetchWorkbookEditorDetail(ctx context.Context, client *cli.Client) (*editororigin.Detail, error) {
	resp, err := client.WithContext(ctx).Get("/api/v1/admin/instance/workbook-editor")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, errEditorDetailForbidden
	default:
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var d editororigin.Detail
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

// checkWorkbookEditorProxy reads the coarse editor state from
// <serverURL>/healthz and, with detail (an instance administrator's login),
// runs editororigin.CheckProxy against every public Studio origin the server
// checks. PASS needs both this machine's check and the server's own check
// to pass. An editor that is off or on a dedicated origin is INFO.
func checkWorkbookEditorProxy(ctx context.Context, serverURL string, detail func(context.Context) (*editororigin.Detail, error)) checkResult {
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
		Editor json.RawMessage `json:"workbook_editor"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return checkResult{name: name, status: "INFO", detail: "server does not report the workbook editor — skipped"}
	}
	if len(body.Editor) == 0 {
		return checkResult{name: name, status: "INFO", detail: "workbook editor is off"}
	}
	var health string
	if err := json.Unmarshal(body.Editor, &health); err != nil {
		return checkResult{name: name, status: "INFO", detail: "server reports the workbook editor in an older format — skipped"}
	}

	var d *editororigin.Detail
	var detailErr error
	if detail == nil {
		detailErr = errEditorDetailForbidden
	} else {
		d, detailErr = detail(ctx)
	}
	if detailErr != nil {
		// Only the coarse state: say what it means and how to see more.
		why := "log in as an instance administrator to check the public path from here"
		if detail != nil && !errors.Is(detailErr, errEditorDetailForbidden) {
			why = "editor detail unavailable (" + detailErr.Error() + ")"
		}
		switch health {
		case editororigin.HealthDisabled:
			return checkResult{name: name, status: "FAIL", detail: "the server disabled the workbook editor: a proxy in front of Crewship redirects or rewrites /_editor/; " + workbookEditorDetailHint, hint: workbookEditorProxyHint}
		case editororigin.HealthFailed:
			return checkResult{name: name, status: "WARN", detail: "the server's own check of /_editor/ through the public URL failed; " + workbookEditorDetailHint, hint: workbookEditorProxyHint}
		case editororigin.HealthPending:
			return checkResult{name: name, status: "WARN", detail: "server self-check pending (it starts once a browser has used Studio); " + why}
		default:
			return checkResult{name: name, status: "INFO", detail: "server self-check: " + health + "; " + why}
		}
	}
	switch d.Mode {
	case "same-host":
	case "dedicated":
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("workbook editor served from a dedicated origin (%s); no same-host proxy rules apply", d.Origin)}
	default:
		return checkResult{name: name, status: "INFO", detail: "workbook editor is off"}
	}
	if len(d.Origins) == 0 {
		return checkResult{name: name, status: "WARN", detail: "server self-check pending: no browser has used Studio since the server started, so the public URL is not known yet; open Studio in a browser, then run doctor again"}
	}

	// Learned origins come from headers any signed-in user controls. Only
	// those on the host doctor was pointed at are probed from this machine
	// (and so from the operator's network); the rest are listed for the
	// operator to recognise.
	serverHost := originHostKey(serverURL)
	var fails, warns, passes, skipped []string
	for _, o := range d.Origins {
		server := o.ProxyCheck
		if o.Source != "configured" && originHostKey(o.Origin) != serverHost {
			who := ""
			if o.LearnedFrom != "" {
				who = " by user " + o.LearnedFrom
			}
			skipped = append(skipped, fmt.Sprintf("%s (learned%s; server self-check: %s)", o.Origin, who, server))
			continue
		}
		if o.Disabled {
			fails = append(fails, fmt.Sprintf("%s: the server disabled the editor (%s: %s)", o.Origin, o.Cause, strings.Join(o.Problems, "; ")))
			continue
		}
		if o.Cause == editororigin.CauseHostRewritten {
			warns = append(warns, fmt.Sprintf("%s: %s", o.Origin, strings.Join(o.Problems, "; ")))
			continue
		}
		c := editororigin.CheckProxy(ctx, &http.Client{Timeout: 10 * time.Second}, o.Origin)
		switch c.Status {
		case editororigin.ProxyCheckFailed:
			kind := "/_editor/ does not work through the public URL"
			switch {
			case c.Cause == editororigin.CauseNoMarker:
				kind = "something in front of Crewship removes the " + editororigin.RouteHeader + " response header, so the editor isolation cannot be verified (the server disables the editor)"
			case c.Isolation:
				kind = "a redirect or path rewrite in front of Crewship defeats the editor isolation"
			}
			fails = append(fails, fmt.Sprintf("%s: %s: %s", o.Origin, kind, strings.Join(c.Problems, "; ")))
			continue
		case editororigin.ProxyCheckUnreachable:
			warns = append(warns, fmt.Sprintf("%s is not reachable from here (server self-check: %s); run doctor where a browser would reach it", o.Origin, server))
			continue
		}
		if server != editororigin.ProxyCheckPassed {
			// N5: passing from here does not make up for the server's own
			// check, which is what keeps the editor enabled.
			msg := fmt.Sprintf("%s passes from here, but the server self-check is %s", o.Origin, server)
			if len(o.Problems) > 0 {
				msg += " (" + strings.Join(o.Problems, "; ") + ")"
			}
			warns = append(warns, msg)
			continue
		}
		if len(c.Warnings) > 0 {
			warns = append(warns, o.Origin+": "+strings.Join(c.Warnings, "; "))
			continue
		}
		passes = append(passes, o.Origin)
	}
	note := ""
	if len(skipped) > 0 {
		note = "; not probed from here, learned from signed-in users on another host than --server (verify they are yours, then run doctor with --server set to one): " + strings.Join(skipped, ", ")
	}
	switch {
	case len(fails) > 0:
		return checkResult{name: name, status: "FAIL", detail: strings.Join(append(fails, warns...), "; ") + note, hint: workbookEditorProxyHint}
	case len(warns) > 0:
		return checkResult{name: name, status: "WARN", detail: strings.Join(warns, "; ") + note, hint: workbookEditorProxyHint}
	case len(passes) == 0:
		return checkResult{name: name, status: "WARN", detail: "no Studio origin the server checks is on " + serverHost + note}
	}
	return checkResult{name: name, status: "PASS", detail: strings.Join(passes, ", ") + ": /_editor/ reaches the editor unchanged, nothing in front redirects it or routes a path under it elsewhere in Crewship, and the server self-check passed" + note}
}

// originHostKey is the host[:port] of an origin or URL, lowercased and
// without the scheme's default port; "" when it is not one.
func originHostKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	_, host, _ := strings.Cut(editororigin.CanonicalOrigin(u.Scheme+"://"+u.Host), "://")
	return host
}
