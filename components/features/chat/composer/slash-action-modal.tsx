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
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { apiFetch } from "@/lib/api-fetch"
import type { SlashActionSchema, SlashFormField } from "@/hooks/use-slash-commands"

/**
 * Generic action modal driven by a slash command's form_schema.
 *
 * One modal handles every slash action — the form_schema field
 * types map onto the same primitives (text, textarea, cron, slug,
 * secret, ...). Unknown types fall back to text so the server can
 * introduce new field types without coordinated frontend rollout.
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
  const [values, setValues] = useState<Record<string, string>>(() => {
    const seed: Record<string, string> = {}
    for (const f of fields) {
      if (contextPreFill && contextPreFill[f.name]) {
        seed[f.name] = contextPreFill[f.name]!
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
      if (f.required && !values[f.name]?.trim()) {
        toast.error(`${f.name} is required`)
        return
      }
    }
    setSubmitting(true)
    try {
      const url = endpointForCommand(command.id, workspaceId)
      const res = await apiFetch(url, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(buildPayload(command.id, values)),
      })
      if (!res.ok) {
        // Log raw body to console for operator debugging; surface
        // only a status-mapped sanitized message to the user.
        // Credential endpoint can return plaintext secret material
        // in validation errors, so the body MUST NOT reach the DOM.
        const body = await res.text().catch(() => "")
        if (body) {
          console.warn(`[slash ${command.id}] server error:`, body)
        }
        toast.error(humanizeError(res.status, body))
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
        <Field
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
          {submitting ? "Submitting…" : command.label}
        </Button>
      </DialogFooter>
    </form>
  )
}

function Field({
  field,
  value,
  onChange,
}: {
  field: SlashFormField
  value: string
  onChange: (e: { target: { value: string } }) => void
}) {
  const label = (
    <Label htmlFor={field.name} className="capitalize">
      {field.name.replace(/_/g, " ")}
      {field.required && <span className="ml-1 text-destructive">*</span>}
    </Label>
  )

  switch (field.type) {
    case "textarea":
      return (
        <div className="space-y-1">
          {label}
          <Textarea
            id={field.name}
            value={value}
            onChange={onChange}
            rows={4}
          />
        </div>
      )

    case "cron":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={field.name}
            value={value}
            onChange={onChange}
            className="font-mono text-sm"
            placeholder="0 7 * * MON"
          />
          <p className="text-xs text-muted-foreground">
            Standard cron expression (5 fields). Server validates parse + timezone.
          </p>
        </div>
      )

    case "timezone":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "UTC"} onValueChange={(v) => onChange({ target: { value: v } })}>
            <SelectTrigger id={field.name}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {/* Minimal initial set; expand based on usage telemetry. */}
              <SelectItem value="UTC">UTC</SelectItem>
              <SelectItem value="Europe/Prague">Europe/Prague</SelectItem>
              <SelectItem value="Europe/London">Europe/London</SelectItem>
              <SelectItem value="America/New_York">America/New_York</SelectItem>
              <SelectItem value="America/Los_Angeles">America/Los_Angeles</SelectItem>
              <SelectItem value="Asia/Tokyo">Asia/Tokyo</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )

    case "priority":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "none"} onValueChange={(v) => onChange({ target: { value: v } })}>
            <SelectTrigger id={field.name}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="none">None</SelectItem>
              <SelectItem value="low">Low</SelectItem>
              <SelectItem value="medium">Medium</SelectItem>
              <SelectItem value="high">High</SelectItem>
              <SelectItem value="urgent">Urgent</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )

    case "memory_scope":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "agent"} onValueChange={(v) => onChange({ target: { value: v } })}>
            <SelectTrigger id={field.name}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="agent">Agent — only this agent remembers</SelectItem>
              <SelectItem value="crew">Crew — shared across crew agents</SelectItem>
              <SelectItem value="workspace">Workspace — visible to every crew</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )

    case "credential_type":
      return (
        <div className="space-y-1">
          {label}
          <Select value={value || "SECRET"} onValueChange={(v) => onChange({ target: { value: v } })}>
            <SelectTrigger id={field.name}>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="SECRET">Secret</SelectItem>
              <SelectItem value="USERPASS">Username + password</SelectItem>
              <SelectItem value="OAUTH2">OAuth2 (pending grant)</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )

    case "secret":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={field.name}
            type="password"
            value={value}
            onChange={onChange}
            autoComplete="off"
          />
        </div>
      )

    case "slug":
      return (
        <div className="space-y-1">
          {label}
          <Input
            id={field.name}
            value={value}
            onChange={onChange}
            placeholder="kebab-case-slug"
            className="font-mono text-sm"
          />
        </div>
      )

    case "text":
    default:
      // Unknown types fall back to text — server controls the
      // catalog, so an unrecognised type means the dashboard is
      // older than the server. Showing a text input + letting the
      // server validate beats rendering nothing.
      return (
        <div className="space-y-1">
          {label}
          <Input id={field.name} value={value} onChange={onChange} />
        </div>
      )
  }
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
  switch (id) {
    case "routine":
      return `/api/v1/workspaces/${ws}/pipeline-schedules`
    case "skill":
      return `/api/v1/workspaces/${ws}/skills/generate`
    case "credential":
      return `/api/v1/credentials?workspace_id=${ws}`
    case "issue":
      // Issue create is crew-scoped — we don't have crew_id in
      // this surface yet. Wire through ChatPanel context in a
      // follow-up; for now the modal pre-validates by hitting the
      // workspace-default crew via a helper.
      return `/api/v1/issues?workspace_id=${ws}`
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
 *  one switch to keep the modal generic. */
function buildPayload(id: string, values: Record<string, string>): unknown {
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
    case "issue":
      return {
        title: values.title,
        description: values.description,
        priority: values.priority || "none",
      }
    default:
      return values
  }
}

// humanizeError maps an HTTP status from a slash-action POST onto a
// UI-safe message. Server response bodies are intentionally NOT
// echoed — the credential endpoint can include plaintext secret
// material in validation errors, and the routine / issue endpoints
// can include SQL fragments / stack traces in their 500 paths. The
// raw text goes to console.warn for operator debugging; the toast
// gets the status-mapped human message only.
//
// `body` is no longer consumed — kept in the signature for caller
// compatibility but the modal now logs it before calling this fn.
function humanizeError(status: number, _body: string): string {
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
