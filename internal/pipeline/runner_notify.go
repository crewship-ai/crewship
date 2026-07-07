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
	toRaw := strings.TrimSpace(Render(step.Notify.To, parentRender))
	targetUserID, targetRole, terr := resolveNotifyTarget(toRaw, in.InvokingUserID)
	if terr != nil {
		slog.Default().Warn("notify step: unresolvable target — falling back to workspace notice",
			"run", runID, "step", step.ID, "to", toRaw, "error", terr)
		targetUserID, targetRole = "", ""
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
			targetUserID = ""
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
