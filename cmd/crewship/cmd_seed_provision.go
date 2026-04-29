package main

// Provisioning helpers used by `crewship seed` to trigger devcontainer
// builds for the seeded crews and wait for them to complete.
// Extracted from cmd_seed.go for readability.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/cli"
)

type provisionTarget struct {
	slug string
	id   string
}

// collectProvisionTargets returns the crews (from seeded data) that have a
// devcontainer_config and therefore need provisioning. Sorted by slug for
// deterministic logs.
func collectProvisionTargets(crewIDs map[string]string) []provisionTarget {
	hasDevcontainer := map[string]bool{}
	for _, c := range seeddata.Crews {
		if c.DevcontainerConfig != "" {
			hasDevcontainer[c.Slug] = true
		}
	}
	var targets []provisionTarget
	for slug, id := range crewIDs {
		if hasDevcontainer[slug] {
			targets = append(targets, provisionTarget{slug: slug, id: id})
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].slug < targets[j].slug })
	return targets
}

// triggerProvisions fires the provision-start request for every target in
// parallel. It does NOT wait for builds to complete — the server handles the
// long-running work asynchronously. This is called early in the seed flow so
// the server can pull images / install features while the seed continues to
// create agents, skills, credentials, and issues via other endpoints.
//
// 429 rate-limit responses are retried with backoff so that a seed of N crews
// can cope with a server-side concurrency cap smaller than N. The per-target
// timeout bounds how long we're willing to keep retrying the trigger alone.
//
// Returns the subset of targets whose triggers actually succeeded and an
// aggregated error summarising the rest (nil if everything started). Callers
// should only poll the returned targets — polling a crew that never started
// would just time out on an idle status.

func triggerProvisions(ctx context.Context, client *cli.Client, targets []provisionTarget, timeout time.Duration) ([]provisionTarget, error) {
	if len(targets) == 0 {
		return nil, nil
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Triggering crew provisioning (runs in background on the server)...")

	type result struct {
		t   provisionTarget
		err error
	}
	results := make(chan result, len(targets))
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t provisionTarget) {
			defer wg.Done()
			err := triggerProvisionOnce(ctx, client, t.id, timeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  X %s: trigger failed: %v\n", t.slug, err)
			} else {
				fmt.Fprintf(os.Stderr, "  + %s: provisioning started\n", t.slug)
			}
			results <- result{t: t, err: err}
		}(t)
	}
	wg.Wait()
	close(results)

	var started []provisionTarget
	var failedSlugs []string
	for r := range results {
		if r.err == nil {
			started = append(started, r.t)
			continue
		}
		failedSlugs = append(failedSlugs, r.t.slug)
	}
	sort.Slice(started, func(i, j int) bool { return started[i].slug < started[j].slug })
	sort.Strings(failedSlugs)

	if len(failedSlugs) == 0 {
		return started, nil
	}
	return started, fmt.Errorf("provisioning trigger failed for %d/%d crews: %s",
		len(failedSlugs), len(targets), strings.Join(failedSlugs, ", "))
}

// waitForProvisions polls each target's status until completion, failure, or
// timeout. Used only when the caller explicitly asked for sync behavior
// (--wait-provision or --smoke-test). Assumes triggerProvisions was already
// called — we only poll here, we don't re-trigger. Returns an aggregated
// error listing the slugs that failed; nil if everything completed.

func waitForProvisions(ctx context.Context, client *cli.Client, targets []provisionTarget, timeout time.Duration) error {
	if len(targets) == 0 {
		return nil
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Waiting for crew provisioning to finish...")

	type result struct {
		slug string
		err  error
	}
	results := make(chan result, len(targets))
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t provisionTarget) {
			defer wg.Done()
			err := pollProvisionStatus(ctx, client, t.id, timeout)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  X %s: %v\n", t.slug, err)
			} else {
				fmt.Fprintf(os.Stderr, "  + %s provisioned\n", t.slug)
			}
			results <- result{slug: t.slug, err: err}
		}(t)
	}
	wg.Wait()
	close(results)

	var failedSlugs []string
	for r := range results {
		if r.err != nil {
			failedSlugs = append(failedSlugs, r.slug)
		}
	}
	sort.Strings(failedSlugs)

	if len(failedSlugs) == 0 {
		return nil
	}
	fmt.Fprintf(os.Stderr,
		"  WARNING: %d/%d crews failed to provision. Agents in those crews will not run.\n",
		len(failedSlugs), len(targets),
	)
	fmt.Fprintf(os.Stderr,
		"           Retry with: crewship crew provision <slug>\n")
	return fmt.Errorf("%d/%d crews failed to provision: %s",
		len(failedSlugs), len(targets), strings.Join(failedSlugs, ", "))
}

// triggerProvisionOnce POSTs the provision-start endpoint once and returns
// when the server has accepted the work. Handles 202/200/409 as success,
// retries 429 with exponential backoff (server caps concurrent provisions so
// firing >N triggers at once can 429 before all slots open). Any other status
// is a hard error. Does NOT poll for completion — that's pollProvisionStatus's
// job.

func triggerProvisionOnce(ctx context.Context, client *cli.Client, crewID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reqClient := client.WithContext(ctx)

	backoff := 3 * time.Second
	for {
		resp, err := reqClient.Post("/api/v1/crews/"+crewID+"/provision", nil)
		if err != nil {
			return fmt.Errorf("trigger provision: %w", err)
		}
		if resp.StatusCode == http.StatusAccepted ||
			resp.StatusCode == http.StatusOK ||
			resp.StatusCode == http.StatusConflict {
			resp.Body.Close()
			return nil
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return fmt.Errorf("timeout waiting for provision slot")
			case <-time.After(backoff):
			}
			// Exponential backoff capped at 30s — slots usually free up in
			// tens of seconds once a peer provision completes. Doubling
			// before the cap check would overshoot (e.g. 24s → 48s stays
			// at 48s), so we compute the next value and clamp.
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return fmt.Errorf("trigger provision: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}
}

// pollProvisionStatus polls the server status endpoint until the provision
// completes or fails. Call this only AFTER triggerProvisionOnce has returned
// nil for the same crewID — this function does not trigger work itself.

func pollProvisionStatus(ctx context.Context, client *cli.Client, crewID string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pollClient := client.WithContext(ctx)

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	var lastStatus string
	for {
		st, err := fetchProvisionStatus(pollClient, crewID)
		if err != nil {
			// Transient 429s from the workspace API rate-limiter shouldn't
			// abort an in-flight wait — the status endpoint gets polled
			// many times and the server's window is short. Treat as
			// "unknown, try again next tick" instead of a hard failure.
			if strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "Too Many Requests") {
				select {
				case <-ctx.Done():
					return fmt.Errorf("timeout after %s (last status: %s)", timeout, lastStatus)
				case <-ticker.C:
					continue
				}
			}
			return fmt.Errorf("poll status: %w", err)
		}
		lastStatus = st.Status
		switch st.Status {
		case "completed":
			return nil
		case "failed":
			if st.Error != "" {
				return fmt.Errorf("provision failed: %s", st.Error)
			}
			return fmt.Errorf("provision failed")
		}
		// pending / running / idle → keep polling
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout after %s (last status: %s)", timeout, lastStatus)
		case <-ticker.C:
		}
	}
}

// ════════════════════════════════════════════════════════════════════════════
// Phase 12: Smoke test
// ════════════════════════════════════════════════════════════════════════════

// smokeTestResult captures the outcome of a single agent smoke test.
