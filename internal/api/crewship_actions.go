package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/policy"
)

// crewshipActions satisfies pipeline.CrewshipActions: it turns one `crewship`
// routine step into a call on the daemon's OWN internal API, over loopback,
// with the master X-Internal-Token.
//
// Why loopback rather than calling the handler in-process: every guard those
// routes carry — workspace binding, crew binding, capability checks, rate
// limits, the audit rows they write — is written INTO the handlers. Going
// through them inherits all of it for free. An in-process path would be a
// second door that has to re-implement each guard, which is the "two files
// each claiming the other enforces it" failure this repo already had once
// (#1791). The accepted costs are a localhost round-trip, and that a
// master-token caller gets no server-side workspace injection and must supply
// and self-police workspace_id — which is why identity fields are injected
// here from the RUN and always win over anything the routine author wrote.
type crewshipActions struct {
	baseURL       string
	internalToken string
	policy        *policy.Resolver
	logger        *slog.Logger
	client        *http.Client
}

// newCrewshipActions builds the dispatcher. baseURL is the daemon's own
// loopback address (WithInternalLoopbackURL); an empty one disables the
// capability, and crewship steps then fail closed with the executor's wiring
// hint rather than silently doing nothing.
func newCrewshipActions(baseURL, internalToken string, pol *policy.Resolver, logger *slog.Logger) pipeline.CrewshipActions {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(internalToken) == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &crewshipActions{
		baseURL:       strings.TrimRight(baseURL, "/"),
		internalToken: internalToken,
		policy:        pol,
		logger:        logger,
		// A routine step must not be able to wedge a run against our own
		// daemon. The internal routes are local writes; 30s is generous.
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// crewshipInjected are the body fields the DISPATCHER owns. They are written
// after the author's args, so an authored value can never replace them: a
// routine naming a foreign workspace_id or a sibling crew_id would be the
// whole cross-tenant hole the master token's lack of injection opens.
var crewshipInjected = []string{"workspace_id", "crew_id", "author_agent_id", "author_run_id"}

// Do gates the verb on the author crew's autonomy level, then performs the
// call.
func (c *crewshipActions) Do(ctx context.Context, req pipeline.CrewshipRequest) (string, error) {
	method, pathTmpl, ok := pipeline.CrewshipVerbRoute(req.Verb)
	if !ok {
		return "", fmt.Errorf("crewship: unknown action %q", req.Verb)
	}
	actionName := pipeline.CrewshipVerbPolicyAction(req.Verb)
	if actionName == "" {
		// Save-time validation refuses these; this is the belt for a
		// definition saved by an older build.
		return "", fmt.Errorf("crewship: action %q has no policy action and cannot be dispatched", req.Verb)
	}

	// Autonomy gate, through the same decision function every internal
	// creation route uses. A routine-fired call carries no caller user id, so
	// it lands on the autonomous arm: the crew's autonomy_level is what
	// bounds it, exactly as it bounds an agent doing this by hand.
	d, err := decideInternalAction(ctx, c.policy, c.logger, req.CrewID, policy.Action(actionName))
	if err != nil {
		return "", fmt.Errorf("crewship %s: policy unavailable: %w", req.Verb, err)
	}
	switch d.Decision {
	case policy.DecisionAutoJournal, policy.DecisionAutoLogJournal, policy.DecisionAutoLogInbox:
		// Proceed. The route itself writes the audit/inbox rows.
	default:
		// Rejected, blocked, or held for approval. A routine step cannot wait
		// on an inbox approval — there is nobody attached to the run to
		// approve it — so a held decision is a refusal here, named as one so
		// the operator can act on it rather than wondering why the routine
		// "does nothing". Raising the crew's autonomy level is the fix, and
		// the message says so.
		return "", fmt.Errorf("crewship %s: refused by policy (autonomy_level=%s, action=%s, crew=%s, decision=%s) — "+
			"a routine cannot wait for an approval; raise the crew's autonomy level with `crewship policy set` to allow it unattended",
			req.Verb, d.Level, actionName, d.CrewID, d.Decision)
	}

	path, err := crewshipRoutePath(pathTmpl, req.Args)
	if err != nil {
		return "", fmt.Errorf("crewship %s: %w", req.Verb, err)
	}
	body := crewshipBody(req)

	raw, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("crewship %s: encode body: %w", req.Verb, err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("crewship %s: build request: %w", req.Verb, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", c.internalToken)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("crewship %s: %s %s: %w", req.Verb, method, path, err)
	}
	defer resp.Body.Close()
	// Bounded read: the response becomes a step output that lands in the run
	// row and every downstream render context.
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("crewship %s: read response: %w", req.Verb, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("crewship %s: %s %s returned %d: %s",
			req.Verb, method, path, resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return string(respBody), nil
}

// crewshipBody assembles the request body: the author's args first, then the
// dispatcher-owned identity/provenance fields on top.
//
// author_run_id is what makes chain depth survive the journal hop. The row
// this creates records the run that created it, so an automation reacting to
// the resulting event can resolve that run, read its chain_depth, and inherit
// depth+1 — instead of treating itself as a fresh chain root, which is what
// would make the cap unenforceable across process boundaries.
func crewshipBody(req pipeline.CrewshipRequest) map[string]any {
	body := make(map[string]any, len(req.Args)+len(crewshipInjected))
	for k, v := range req.Args {
		body[k] = v
	}
	// Strip first, then set. Overwriting would be enough today, but the strip
	// states the rule where the next field is added: these keys are not the
	// author's to supply, and a conditional anywhere in this loop would turn
	// "the run's identity wins" into "the author's does".
	for _, k := range crewshipInjected {
		delete(body, k)
	}
	body["workspace_id"] = req.WorkspaceID
	body["crew_id"] = req.CrewID
	body["author_agent_id"] = req.AgentID
	body["author_run_id"] = req.RunID
	return body
}

// crewshipRoutePath fills the single {name} placeholder a verb's route may
// carry from the matching arg, path-escaped. A missing or empty value is an
// error, not an empty segment: `/issues//comments` would 404 in a way nobody
// could read.
func crewshipRoutePath(tmpl string, args map[string]any) (string, error) {
	open := strings.Index(tmpl, "{")
	if open < 0 {
		return tmpl, nil
	}
	closeIdx := strings.Index(tmpl[open:], "}")
	if closeIdx < 0 {
		return "", fmt.Errorf("malformed route template %q", tmpl)
	}
	closeIdx += open
	name := tmpl[open+1 : closeIdx]
	val, _ := args[name].(string)
	if strings.TrimSpace(val) == "" {
		return "", fmt.Errorf("arg %q is required and must render to a non-empty value", name)
	}
	return tmpl[:open] + url.PathEscape(val) + tmpl[closeIdx+1:], nil
}
