"use client"

import { useState } from "react"
import { toast } from "sonner"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"
import { devWarn } from "@/lib/client-log"
import type { SlashActionSchema, SlashFormField } from "@/hooks/use-slash-commands"
import {
  RoutineInputError,
  isMissingRequired,
  routineInputsFromValues,
  routineSlugFromSlashId,
} from "@/lib/routine-inputs"
import { FormField } from "../asks/form-field"

/**
 * Generic action modal driven by a slash command's form_schema.
 *
 * One modal handles every slash action — the form_schema field
 * types map onto the same primitives (text, textarea, cron, slug,
 * secret, ...). Unknown types fall back to text so the server can
 * introduce new field types without coordinated frontend rollout.
 *
 * The field renderer itself now lives in `../asks/form-field.tsx`,
 * shared with the ask sheet: the sheet does the same job from the
 * same kind of schema, and a second switch statement would drift
 * from this one the first time either side gained a type. The
 * modal's own behaviour is unchanged by the move and is pinned by
 * __tests__/slash-action-modal.test.tsx, which was written against
 * the pre-extraction component.
 *
 * On submit the modal POSTs to the matching public capability-
 * gated endpoint (NOT the internal sidecar — chat-bridge handles
 * the sidecar path; this modal is rendered in the dashboard and
 * talks to the API directly with the user's JWT). The capability
 * recheck is server-side; client-side filter (palette show/hide)
 * is UX, not security.
 */
interface SlashActionModalProps {
  /** The slash command the user picked. null = modal closed. */
  command: SlashActionSchema | null
  /** Active workspace id; required to address the right endpoint. */
  workspaceId: string
  /** Conversation context — optional pre-fill source for fields like
   *  `name` (chat title) or `content` (last message). */
  contextPreFill?: Partial<Record<string, string>>
  onClose: () => void
  /** Called on a successful submit so the parent can clear the slash
   *  input, scroll to the new artifact, fire its own analytics, etc. */
  onSuccess?: (command: SlashActionSchema, result: unknown) => void
}

export function SlashActionModal({
  command,
  workspaceId,
  contextPreFill,
  onClose,
  onSuccess,
}: SlashActionModalProps) {
  // Form state is rebuilt every time the modal opens with a different
  // command — we key the inner Form component on command.id so React
  // remounts and the field defaults from form_schema apply cleanly.
  if (!command) return null

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{command.label}</DialogTitle>
          {command.label_cs && command.label_cs !== command.label && (
            <DialogDescription>{command.label_cs}</DialogDescription>
          )}
        </DialogHeader>
        <Form
          key={command.id}
          command={command}
          workspaceId={workspaceId}
          contextPreFill={contextPreFill}
          onClose={onClose}
          onSuccess={onSuccess}
        />
      </DialogContent>
    </Dialog>
  )
}

interface FormProps extends Omit<SlashActionModalProps, "command"> {
  command: SlashActionSchema
}

function Form({
  command,
  workspaceId,
  contextPreFill,
  onClose,
  onSuccess,
}: FormProps) {
  const fields = command.form_schema ?? []
  // The transcript pre-fill belongs to the four "…from this conversation"
  // actions and to nothing else. A routine's inputs are its own: the host
  // seeds `prompt` and `description` with up to 4000 characters of chat
  // (chat-panel.tsx), and a routine that happens to declare an input by
  // either of those very ordinary names would have silently had the last
  // six turns pasted into it, over the top of the default its author
  // declared. Running a routine is not authoring something from the
  // conversation, so it does not take the conversation.
  const isRoutineRun = Boolean(routineSlugFromSlashId(command.id))
  // The four platform labels are verb phrases — "Add credential", "Create
  // issue from this conversation" — so using the label as the button reads
  // as an instruction. A routine's label is whatever its author called the
  // routine, which is a noun: a button reading "Účetní podklady za měsíc"
  // names the thing and never says what pressing it does. Routines get the
  // verb; everything else keeps the label it has always had.
  const submitLabel = isRoutineRun ? "Run" : command.label
  const submittingLabel = isRoutineRun ? "Running…" : "Submitting…"
  const preFill = isRoutineRun ? undefined : contextPreFill
  const [values, setValues] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {}
    for (const f of fields) {
      if (preFill && preFill[f.name]) {
        seed[f.name] = preFill[f.name]!
      } else if (f.default) {
        seed[f.name] = f.default
      } else {
        seed[f.name] = ""
      }
    }
    return seed
  })
  const [submitting, setSubmitting] = useState(false)

  const setField = (name: string) => (e: { target: { value: string } }) => {
    setValues((prev) => ({ ...prev, [name]: e.target.value }))
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    // Required-field check is the only client validation we do —
    // server validates the rest (cron parse, slug shape, ...) and
    // surfaces the error message back via toast.
    for (const f of fields) {
      if (isMissingRequired(f, values[f.name])) {
        toast.error(`${f.name} is required`)
        return
      }
    }
    // Build the body BEFORE anything is sent. A routine input that can't
    // be restored to its declared type is the user's to fix with the form
    // still in front of them, so it must not become a request — and must
    // not leave the modal in its submitting state either.
    let payload: unknown
    try {
      payload = buildPayload(command.id, values, fields)
    } catch (err) {
      if (err instanceof RoutineInputError) {
        toast.error(err.message)
        return
      }
      throw err
    }
    setSubmitting(true)
    try {
      const url = endpointForCommand(command.id, workspaceId)
      const res = await apiFetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      })
      if (!res.ok) {
        // Log raw body dev-only for operator debugging; surface
        // only a status-mapped sanitized message to the user.
        // Credential endpoint can return plaintext secret material
        // in validation errors, so the body MUST NOT reach the DOM.
        const body = await res.text().catch(() => "")
        if (body) {
          devWarn(`[slash ${command.id}] server error:`, body)
        }
        toast.error(humanizeError(res.status, body, Boolean(routineSlugFromSlashId(command.id))))
        return
      }
      const result = await res.json().catch(() => null)
      toast.success(`${command.label} — done`)
      onSuccess?.(command, result)
      onClose()
    } catch (err) {
      toast.error(`Failed: ${(err as Error).message}`)
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <form onSubmit={handleSubmit} className="space-y-4">
      {fields.map((f) => (
        <FormField
          key={f.name}
          field={f}
          value={values[f.name] ?? ""}
          onChange={setField(f.name)}
        />
      ))}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose} disabled={submitting}>
          Cancel
        </Button>
        <Button type="submit" disabled={submitting}>
          {submitting ? submittingLabel : submitLabel}
        </Button>
      </DialogFooter>
    </form>
  )
}

/**
 * Map slash command ids to the matching public API endpoint.
 *
 * Server-side these are the same routes the CLI hits — parity is
 * the whole point of PRD-SLASH-CAPABILITIES-2026. The capability
 * recheck fires on the server regardless of which transport the
 * user took (palette / CLI repl / sidecar slash).
 */
function endpointForCommand(id: string, workspaceId: string): string {
  const ws = encodeURIComponent(workspaceId)
  // Per-routine entries first: their id carries the routine's slug, so
  // they cannot be a case in the switch below. The `routine.run:` prefix
  // is what identifies them — the server put it there for this, and
  // reading it beats inferring an endpoint from a routine name.
  const slug = routineSlugFromSlashId(id)
  if (slug) {
    return `/api/v1/workspaces/${ws}/pipelines/${encodeURIComponent(slug)}/run`
  }
  switch (id) {
    case "routine":
      // Reachable only if someone re-enables the action: this endpoint
      // SCHEDULES an existing pipeline and rejects any body without
      // target_pipeline_id/target_pipeline_slug, which a conversation does
      // not have. The palette classifies "routine" as disabled for exactly
      // that reason (SERVER_ACTION_CONTRACT in slash-palette.tsx); the branch
      // stays because the body shape is still the right one for the day a
      // transcript→routine step exists.
      return `/api/v1/workspaces/${ws}/pipeline-schedules`
    case "skill":
      return `/api/v1/workspaces/${ws}/skills/generate`
    case "credential":
      return `/api/v1/credentials?workspace_id=${ws}`
    // "issue" intentionally absent. This used to return
    // `/api/v1/issues?workspace_id=…`, which is not a route: POST issues is
    // registered ONLY as /api/v1/crews/{crewId}/issues (router_orchestration
    // .go), so every submit was a 404 dressed up as a form. Chat has no crew
    // id to put in that path, so the action is disabled in the palette rather
    // than pointed at a URL that cannot work.
    // "remember" intentionally absent — see catalog note in
    // internal/api/slash_commands_handler.go. The backend route
    // doesn't exist yet; the server-side catalog omits the entry
    // so this branch is unreachable from the live UI.
    default:
      // Defence: never POST to an unknown endpoint. A new slash
      // action from the server we don't know how to dispatch
      // should fail loudly rather than guess.
      throw new Error(`unknown slash command id: ${id}`)
  }
}

/** Transform the flat form-values map into the body shape the
 *  matching backend handler expects. Per-command shaping kept in
 *  one switch to keep the modal generic.
 *
 *  Throws RoutineInputError when a routine input cannot be restored to
 *  its declared JSON type (an unparseable object, a word in an integer
 *  box). The caller catches it and keeps the form open with the offending
 *  field named — posting the string and letting the server 400 would say
 *  less, later. */
function buildPayload(
  id: string,
  values: Record<string, string>,
  fields: SlashFormField[],
): unknown {
  if (routineSlugFromSlashId(id)) {
    return { inputs: routineInputsFromValues(fields, values) }
  }
  switch (id) {
    case "routine":
      return {
        name: values.name,
        cron_expr: values.cron,
        timezone: values.timezone || "UTC",
      }
    case "skill":
      return { slug: values.slug, prompt: values.prompt }
    case "credential":
      return {
        name: values.name,
        type: values.type || "SECRET",
        value: values.value,
      }
    // No "issue" case: endpointForCommand has no mapping for it, so the
    // request is refused before a body is ever built.
    default:
      return values
  }
}

// humanizeError maps an HTTP status from a slash-action POST onto a
// UI-safe message. Server response bodies are intentionally NOT
// echoed — the credential endpoint can include plaintext secret
// material in validation errors, and the routine / issue endpoints
// can include SQL fragments / stack traces in their 500 paths. The
// raw text goes to a dev-only devWarn for operator debugging; the
// toast gets the status-mapped human message only.
//
// `body` is no longer consumed — kept in the signature for caller
// compatibility but the modal now logs it before calling this fn.
//
// `isRoutineRun` adds the two statuses the run endpoint answers with
// that no other slash action produces. Without them, the commonest way
// a routine run is refused — the author crew has not connected an
// integration it declares — reached the user as "Request failed (HTTP
// 422)", while the identical refusal on the routines detail page says
// which integration and offers a link to connect it. Same refusal, two
// surfaces; the quiet one was the new one.
//
// The specific missing integration/credential is deliberately NOT named
// here even though the body carries it: the rule at the top of this
// function is that response bodies do not reach the DOM on this path,
// and a slash action is not worth making an exception to it. The
// message says which KIND of thing is missing and where to look.
function humanizeError(status: number, _body: string, isRoutineRun = false): string {
  if (isRoutineRun) {
    switch (status) {
      case 409:
        return "This routine isn't runnable right now — it's awaiting approval or has been disabled."
      case 422:
        return "This routine needs an integration or credential its crew doesn't have. Open the routine to see which."
    }
  }
  switch (status) {
    case 400:
      return "The form values were rejected by the server."
    case 401:
      return "Your session expired. Reload and sign in again."
    case 403:
      return "You don't have permission for this action. An admin may have revoked it."
    case 404:
      return "The target resource no longer exists."
    case 413:
      return "Request too large."
    case 500:
      return "Server error. See the operator log for details."
    default:
      return `Request failed (HTTP ${status}).`
  }
}
