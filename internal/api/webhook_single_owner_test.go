package api

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// One delivery must not be able to travel two paths.
//
// Review finding R3: acceptance committed durable work and then started an
// agent directly, without claiming that work. Adding a dispatcher while leaving
// that call in place would have been worse than either alone — two owners of
// one piece of work is how the same delivery runs twice, and the second owner
// is invisible until it happens.
//
// This is a source-level assertion for the same reason the repo's other
// invariant guards are: the failure it prevents is a call site existing at all,
// and a behavioural test can only observe the paths somebody remembered to
// exercise. It fails the build the moment acceptance regains a way to start an
// agent, which is the only outcome that matters.
func TestWebhookAcceptanceCannotStartAnAgent(t *testing.T) {
	t.Parallel()
	src, err := os.ReadFile("webhook.go")
	if err != nil {
		t.Fatalf("read webhook.go: %v", err)
	}
	text := string(src)

	// acceptDelivery is the function the HTTP handler calls to take a delivery.
	// Everything from it to the end of the accepting path must be database work
	// and a best-effort hint — nothing that creates a runtime.
	start := strings.Index(text, "func (h *WebhookHandler) acceptDelivery(")
	if start < 0 {
		t.Fatal("acceptDelivery is gone; this guard no longer guards anything and must be rewritten")
	}
	end := strings.Index(text[start:], "\n// runWebhookAgent")
	if end < 0 {
		t.Fatal("could not find the end of the acceptance path; rewrite this guard rather than deleting it")
	}
	acceptance := text[start : start+end]

	banned := []struct {
		pattern *regexp.Regexp
		why     string
	}{
		{
			regexp.MustCompile(`h\.orch\.RunAgent\(`),
			"acceptance would start an agent directly, which is the second owner R3 removed",
		},
		{
			regexp.MustCompile(`crewstart\.New\(`),
			"acceptance would warm a container inside the response path — finding W2, and a provider's " +
				"10-second deadline does not care that we meant well",
		},
		{
			regexp.MustCompile(`h\.runWebhookAgent\(`),
			"acceptance would run the agent itself instead of leaving it to the dispatcher",
		},
		{
			regexp.MustCompile(`beginBackgroundWork\(`),
			"acceptance would launch detached work the ledger does not own",
		},
	}
	for _, b := range banned {
		if b.pattern.MatchString(acceptance) {
			t.Errorf("the acceptance path matches %v: %s", b.pattern, b.why)
		}
	}

	// And the positive half: the hint has to stay best-effort. A hint that
	// could fail the request, or that acceptance waited on, would turn an
	// optimisation into a precondition — and then a crash between the commit
	// and the nudge would strand work the ledger exists to guarantee.
	if !strings.Contains(acceptance, "h.dispatchHint") {
		t.Error("acceptance no longer hints the dispatcher; work would wait for a poll every time")
	}
	if regexp.MustCompile(`if err := h\.dispatchHint`).MatchString(acceptance) {
		t.Error("the dispatch hint is being error-checked, which makes execution depend on it")
	}
}

// The runtime adapter is the only bridge from the ledger to an agent run, so
// the dispatcher is the only thing that can cross it.
func TestWebhookRuntimeIsTheOnlyCallerOfTheAgentRun(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	callers := map[string]int{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Count call sites, not the declaration.
		for _, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, ".runWebhookAgent(") && !strings.Contains(line, "func (h *WebhookHandler)") {
				callers[name]++
			}
		}
	}
	total := 0
	for _, n := range callers {
		total += n
	}
	if total != 1 || callers["webhook_runtime.go"] != 1 {
		t.Errorf("runWebhookAgent is called from %v; it must have exactly one caller, the dispatcher's "+
			"runtime adapter in webhook_runtime.go", callers)
	}
}
