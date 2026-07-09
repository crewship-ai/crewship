package pipeline

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// InboxNotifier is the sink a notify step writes to. Splitting it out from
// *sql.DB keeps runNotifyStep unit-testable (a fake captures the composed
// inbox.Item) and mirrors the store seam wait:approval uses for the same
// inbox table.
type InboxNotifier interface {
	Notify(ctx context.Context, item inbox.Item) error
}

// sqlInboxNotifier is the production InboxNotifier: it forwards to
// inbox.Insert, whose (kind, source_id) unique index makes retries
// idempotent. Constructed from the run's own DB in NewWiredExecutor.
type sqlInboxNotifier struct {
	db     *sql.DB
	logger *slog.Logger
}

func (n *sqlInboxNotifier) Notify(ctx context.Context, item inbox.Item) error {
	return inbox.Insert(ctx, n.db, n.logger, item)
}

// WithInboxNotifier wires the notify-step inbox sink. Without it, notify
// steps degrade to a best-effort no-op (logged) rather than failing the run.
func (e *Executor) WithInboxNotifier(n InboxNotifier) *Executor {
	e.notifier = n
	return e
}

// perRunNotifyCap is the per-recipient soft cap on routine-update notices a
// single run may deliver to one inbox. A DAG that puts a notify in every
// step (progress spam) or targets one user from many branches shouldn't be
// able to dump an unbounded pile of cards on that recipient. It's a SOFT
// cap: once reached, further notices to that recipient are dropped with a
// WARN — never a run failure. The bound is per (run, recipient), so a run
// notifying several people each gets its own budget, and a scheduled routine
// firing repeatedly is unaffected (each run has a fresh, distinct run id).
const perRunNotifyCap = 20

// WithNoticeCounter wires the per-recipient anti-spam counter for notify
// steps. Without it, the soft cap is skipped and delivery is uncapped
// (dev/test/misconfig). Production installs NewRunNoticeCounter(db).
func (e *Executor) WithNoticeCounter(fn func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error)) *Executor {
	e.noticeCounter = fn
	return e
}

// WithMemberChecker wires the workspace-membership guard for notify steps
// that target `user:<id>`. Without it, the guard is skipped and the id is
// trusted as-is.
func (e *Executor) WithMemberChecker(fn func(ctx context.Context, workspaceID, userID string) (bool, error)) *Executor {
	e.memberCheck = fn
	return e
}

// runNotifyStep handles StepNotify: a non-blocking push of a rendered
// message to a recipient's inbox mid-run. It renders title/body from the
// run context, scrubs secrets, resolves the target, and emits a `message`
// inbox item keyed on run:step (idempotent on retry), then returns so the
// DAG continues. Delivery is best-effort and NEVER fails the run — the
// routine's real work is already done. A literal malformed `to`/`priority`
// is caught at author time (offline Validate / draft dry-run save gate);
// at run time we degrade rather than crash, so a templated target that
// renders empty (e.g. `user:{{ inputs.assignee }}` → `user:`) or a typo'd
// member id can never take the run down with it.
func (e *Executor) runNotifyStep(ctx context.Context, step Step, parentRender RenderContext, in RunInput, runID string) (string, float64, int64, error) {
	stepStart := time.Now()
	if step.Notify == nil {
		return "", 0, 0, fmt.Errorf("notify step %q missing notify body", step.ID)
	}

	// Resolve the target. A run-time resolution failure (templated `to`
	// rendered empty, malformed input value) must NOT fail the run — the
	// non-blocking contract. Author mistakes with LITERAL targets are
	// already rejected by validateNotifyTarget at save/dry-run; here we
	// degrade to a workspace-wide notice so the update still lands.
	// degraded tracks whether the author's intended recipient could NOT be
	// honoured and the notice fell back to a workspace-wide notice. It changes
	// the return marker (notified:degraded) and surfaces a run warning, so a
	// silent fallback isn't indistinguishable from a targeted delivery — in
	// particular a templated `to` that renders to an unsupported `crew:<slug>`
	// (which can only be caught here at run time, not at save) is visible on
	// the run rather than a server-log-only footnote.
	degraded := false
	toRaw := strings.TrimSpace(Render(step.Notify.To, parentRender))
	targetUserID, targetRole, terr := resolveNotifyTarget(toRaw, in.InvokingUserID)
	if terr != nil {
		slog.Default().Warn("notify step: unresolvable target — falling back to workspace notice",
			"run", runID, "step", step.ID, "to", toRaw, "error", terr)
		e.recordRunWarning(ctx, runID, "notify:"+step.ID,
			fmt.Errorf("target %q not delivered as addressed — sent as a workspace notice instead: %w", toRaw, terr))
		targetUserID, targetRole = "", ""
		degraded = true
	}

	// dry_run preview: render but never write to the inbox.
	if in.Mode == ModeDryRun {
		return "notified:preview", 0, time.Since(stepStart).Milliseconds(), nil
	}

	// Membership guard: a `user:` target that isn't in the workspace would
	// be a silent black hole — the row inserts, but nobody can ever see it.
	// Degrade to a workspace notice + WARN so a typo'd id doesn't swallow
	// the message. (A DB error checking membership fails open to the intended
	// target rather than dropping it.)
	if targetUserID != "" && e.memberCheck != nil {
		switch member, merr := e.memberCheck(ctx, in.WorkspaceID, targetUserID); {
		case merr != nil:
			slog.Default().Warn("notify step: membership check failed — targeting user anyway",
				"run", runID, "step", step.ID, "user", targetUserID, "error", merr)
		case !member:
			slog.Default().Warn("notify step: target user not in workspace — falling back to workspace notice",
				"run", runID, "step", step.ID, "user", targetUserID)
			e.recordRunWarning(ctx, runID, "notify:"+step.ID,
				fmt.Errorf("target user %q is not a workspace member — sent as a workspace notice instead", targetUserID))
			targetUserID = ""
			degraded = true
		}
	}

	// Per-recipient anti-spam soft cap: if this run has already delivered
	// perRunNotifyCap notices to the resolved recipient, drop this one. The
	// cap is enforced at the notify chokepoint (not the inbox writer) so it
	// counts only routine notices for THIS run, keyed on the same target the
	// item will carry. Best-effort: a counter error fails OPEN (deliver) —
	// anti-spam is a courtesy, the update landing is the priority.
	if e.noticeCounter != nil {
		switch prior, cerr := e.noticeCounter(ctx, in.WorkspaceID, runID, targetUserID, targetRole); {
		case cerr != nil:
			slog.Default().Warn("notify step: notice count failed — delivering uncapped",
				"run", runID, "step", step.ID, "error", cerr)
		case prior >= perRunNotifyCap:
			slog.Default().Warn("notify step: per-recipient soft cap reached — dropping notice",
				"run", runID, "step", step.ID, "user", targetUserID, "role", targetRole, "cap", perRunNotifyCap)
			return "notified:capped", 0, time.Since(stepStart).Milliseconds(), nil
		}
	}

	senderName := "routine"
	if in.dsl != nil && in.dsl.Name != "" {
		senderName = in.dsl.Name
	}
	title := inbox.CleanTitle(inbox.RedactSecrets(Render(step.Notify.Title, parentRender)), 120, senderName)
	body := inbox.RedactSecrets(Render(step.Notify.Body, parentRender))

	item := inbox.Item{
		WorkspaceID:  in.WorkspaceID,
		Kind:         inbox.KindMessage,
		SourceID:     runID + ":" + step.ID, // idempotency: one item per (run, step)
		TargetUserID: targetUserID,
		TargetRole:   targetRole,
		Title:        title,
		BodyMD:       body,
		SenderType:   "pipeline",
		SenderName:   senderName,
		Priority:     normalizeNotifyPriority(step.Notify.Priority),
		Blocking:     false, // update, not a decision request
		Payload: map[string]interface{}{
			// subkind keeps routine updates in their own filterable lane
			// so they don't drown approvals/escalations in the inbox.
			"subkind":         "routine_update",
			"pipeline_run_id": runID,
			"step_id":         step.ID,
		},
	}

	if e.notifier == nil {
		// Production always wires a notifier (NewWiredExecutor with a DB);
		// nil is dev/test/misconfig. Non-blocking philosophy: don't fail
		// the run because there's nowhere to post.
		slog.Default().Warn("notify step skipped: no inbox notifier wired", "run", runID, "step", step.ID)
		return "notified:skipped", 0, time.Since(stepStart).Milliseconds(), nil
	}
	// Bound the delivery so a stalled inbox write can't hold the run open:
	// this is a fire-and-forget update, not part of the run's critical
	// path. A timeout collapses into the same non-fatal WARN as any other
	// delivery failure.
	notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := e.notifier.Notify(notifyCtx, item); err != nil {
		// A delivery failure is non-fatal: the routine did its work, the
		// bell just didn't ring. Log at WARN and carry on.
		slog.Default().Warn("notify step delivery failed", "run", runID, "step", step.ID, "error", err)
		return "notified:error", 0, time.Since(stepStart).Milliseconds(), nil
	}
	// Push it live. inbox.Insert only writes the row; the "inbox.updated"
	// WS fan-out lives in the API handler layer, so without this a routine
	// notify would only surface on the next poll. Broadcasting here makes
	// open inboxes + bell badges repaint immediately (use-inbox.ts
	// invalidates on any inbox.updated). Best-effort: e.ws is nil in tests.
	if e.ws != nil {
		e.ws.BroadcastWorkspace(in.WorkspaceID, "inbox.updated", map[string]string{
			"source":  "routine_notify",
			"run_id":  runID,
			"step_id": step.ID,
		})
	}
	// A degraded delivery landed (as a workspace notice), but NOT to the
	// recipient the author addressed — mark it distinctly so the step output
	// and run detail don't read as a clean targeted send.
	if degraded {
		return "notified:degraded:" + item.SourceID, 0, time.Since(stepStart).Milliseconds(), nil
	}
	return "notified:" + item.SourceID, 0, time.Since(stepStart).Milliseconds(), nil
}

// NewWorkspaceMemberChecker returns a membership predicate backed by the
// workspace_members table — used by notify steps to avoid posting to a
// user id that isn't in the workspace (a silent black hole). Nil db or
// empty ids report "not a member" without a query.
func NewWorkspaceMemberChecker(db *sql.DB) func(ctx context.Context, workspaceID, userID string) (bool, error) {
	return func(ctx context.Context, workspaceID, userID string) (bool, error) {
		if db == nil || workspaceID == "" || userID == "" {
			return false, nil
		}
		var exists int
		err := db.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM workspace_members WHERE workspace_id = ? AND user_id = ?)`,
			workspaceID, userID).Scan(&exists)
		if err != nil {
			return false, err
		}
		return exists == 1, nil
	}
}

// NewRunNoticeCounter returns the production per-recipient notice counter
// backing perRunNotifyCap. It counts routine-update `message` items already
// written for this run (SourceID = "<run>:<step>", so the "<run>:" prefix
// selects exactly this run's notices) to the same recipient the pending
// item will target. Recipient match mirrors resolveNotifyTarget's output:
//   - user:  target_user_id = <id>
//   - role:  target_role = <ROLE> with no user (a role notice)
//   - workspace: neither set
//
// Nil db reports 0 (cap disabled) so callers never fail the run over it.
func NewRunNoticeCounter(db *sql.DB) func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error) {
	return func(ctx context.Context, workspaceID, runID, targetUserID, targetRole string) (int, error) {
		if db == nil || workspaceID == "" || runID == "" {
			return 0, nil
		}
		// The stored id is "ibx_message_<run>:<step>"; match on source_id,
		// which the writer sets to "<run>:<step>", via the "<run>:" prefix.
		// LIKE metacharacters (%, _) can't appear in a cuid run id, so no
		// escaping is needed.
		prefix := runID + ":%"
		var n int
		err := db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM inbox_items
			WHERE workspace_id = ?
			  AND kind = ?
			  AND source_id LIKE ?
			  AND target_user_id IS ?
			  AND target_role IS ?`,
			workspaceID, inbox.KindMessage, prefix,
			nullableStr(targetUserID), nullableStr(targetRole),
		).Scan(&n)
		if err != nil {
			return 0, err
		}
		return n, nil
	}
}

// resolveNotifyTarget maps a notify `to` selector to inbox targeting
// (TargetUserID / TargetRole). Empty of both = workspace-wide.
func resolveNotifyTarget(to, invokingUserID string) (userID, role string, err error) {
	switch {
	case to == "", to == "workspace":
		return "", "", nil
	case to == "trigger":
		// Server-side the executor holds the triggering user. When it's
		// absent (scheduled run, nested call_pipeline), fall back to a
		// workspace notice so the update still lands.
		return invokingUserID, "", nil
	case strings.HasPrefix(to, "user:"):
		id := strings.TrimSpace(strings.TrimPrefix(to, "user:"))
		if id == "" {
			return "", "", fmt.Errorf("to: user: missing id")
		}
		return id, "", nil
	case strings.HasPrefix(to, "role:"):
		r := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(to, "role:")))
		switch r {
		case "OWNER", "MANAGER":
			return "", r, nil
		default:
			return "", "", fmt.Errorf("to: role:%s not targetable (allowed: OWNER, MANAGER)", r)
		}
	case strings.HasPrefix(to, "crew:"):
		// crew:<slug> targeting is deferred to #842 Phase 2: crews are groups
		// of agents, the inbox targets users, and there is no crew→user
		// ("human audience of a crew") mapping in the schema yet. Reject it
		// loudly here — at save via validateNotifyTarget, and at run time for a
		// templated target that renders to crew:<slug> — so the author knows
		// it's coming rather than being surprised by a silent no-op.
		return "", "", fmt.Errorf("to %q not yet supported — crew targeting lands in Phase 2 (issue #842); for now target trigger, user:<id>, role:OWNER/MANAGER, or workspace", to)
	default:
		return "", "", fmt.Errorf("to %q unsupported (allowed: workspace, trigger, user:<id>, role:OWNER, role:MANAGER)", to)
	}
}

// validateNotifyTarget is the author-time shape check for `to`. Templated
// targets (containing {{ }}) are resolved at run time, so their literal
// shape is only checked once rendered.
func validateNotifyTarget(to string) error {
	if strings.Contains(to, "{{") {
		return nil
	}
	_, _, err := resolveNotifyTarget(to, "placeholder")
	return err
}

// isValidNotifyPriority reports whether p is one of the inbox priorities.
func isValidNotifyPriority(p string) bool {
	switch p {
	case "urgent", "high", "medium", "low":
		return true
	default:
		return false
	}
}

// normalizeNotifyPriority passes through a valid priority; anything else
// (including empty) defers to the inbox writer's medium default.
func normalizeNotifyPriority(p string) string {
	if isValidNotifyPriority(p) {
		return p
	}
	return ""
}
