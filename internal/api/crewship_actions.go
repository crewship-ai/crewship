package api

import (
	"bytes"
	"context"
	"database/sql"
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
	// db serves the volume bounds this door owns — the ones the per-call
	// autonomy matrix cannot express. Today that is the escalation backlog cap
	// (crewship_escalation_cap.go), counted from server-side rows.
	db     *sql.DB
	logger *slog.Logger
	client *http.Client
}

// newCrewshipActions builds the dispatcher. baseURL is the daemon's own
// loopback address (WithInternalLoopbackURL); an empty one disables the
// capability, and crewship steps then fail closed with the executor's wiring
// hint rather than silently doing nothing.
func newCrewshipActions(baseURL, internalToken string, pol *policy.Resolver, db *sql.DB, logger *slog.Logger) pipeline.CrewshipActions {
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
		db:            db,
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
//
// The last two are the ACTING AGENT under the two names the internal routes
// spell it, and both are here for the same reason the first four are — the
// routine author does not get to say who acted:
//
//   - agent_id is what the issue routes read (issues_internal.go's comment
//     handler requires it, update/relations attribute the audit row to it), so
//     without injection issue.comment 400s and issue.update files its change
//     under "system".
//   - actor_agent_id is what POST /internal/assignments reads, and it is the id
//     the DELEGATION CAP measures depth from (delegation_limits.go: "depth
//     comes from assignments.depth of the row the CALLER is executing, found by
//     the acting agent id"). An author-supplied value there is depth
//     laundering — name a shallow agent, get a shallow position — which is the
//     /query failure that file exists in order not to be. Stripping it is not
//     optional.
//
// Both are set from the run's author agent, which may be empty for a routine a
// human authored. Empty is the same as absent to every route that reads them:
// assignments falls back to the chat's agent, issue.update attributes to
// "system", issue.comment refuses with "agent_id is required" — the honest
// answer, since a comment needs an author it can name.
var crewshipInjected = []string{
	"workspace_id", "crew_id", "author_agent_id", "author_run_id",
	"agent_id", "actor_agent_id",
}

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

	// Volume bounds this door owns. The autonomy matrix decides per call and
	// cannot express a rate, so an action whose real risk is "how many" needs a
	// number counted from server state — the same split mission_limits.go makes
	// against the mission_create cell. Runs BEFORE the request is built: a
	// refused escalation must cost nothing but the count.
	if err := c.enforceVolumeBounds(ctx, req); err != nil {
		return "", fmt.Errorf("crewship %s: %w", req.Verb, err)
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

// enforceVolumeBounds applies the per-verb quantity limits that the autonomy
// matrix structurally cannot. One verb has one today; the switch is here so the
// next one lands next to it rather than inline in Do.
func (c *crewshipActions) enforceVolumeBounds(ctx context.Context, req pipeline.CrewshipRequest) error {
	if req.Verb == "escalation.create" {
		return enforceRoutineEscalationCap(ctx, c.db, req.CrewID)
	}
	return nil
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
	// The same acting agent under the two names the internal routes read it by.
	// Set unconditionally, including when empty: a conditional here would let an
	// authored value survive for a routine with no author agent, which is
	// exactly the case where forging one is worth something.
	body["agent_id"] = req.AgentID
	body["actor_agent_id"] = req.AgentID
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
