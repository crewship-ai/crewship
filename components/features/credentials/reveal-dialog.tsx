"use client"

/**
 * The reveal ceremony — PRD-CREDENTIALS-V2-2026 §2.6, wireframe screen 9.
 *
 * "Reveal is not a permission, it is a ceremony." The server enforces every
 * layer (workspace switch, capability, crew scope, classification, reason,
 * chained audit as a precondition); this dialog exists so the person asking
 * can see WHICH conditions they are standing on before they spend one, and so
 * the cheaper alternative is on the same screen.
 *
 * Deliberate choices:
 *
 *  · Harbor 2 (S dialog): one reason field and one filled primary, "Reveal
 *    for 30 s". A reason under the floor is refused at the field without a
 *    request; a server refusal (generic reason, stale sign-in) lands at the
 *    field too, and the dialog stays. "Replace value instead" stays on the
 *    same screen as the cheaper path (§2.6 L8).
 *  · The value is rendered exactly once, in the result panel, and there is no
 *    way to ask for it again without starting over. Closing the dialog drops
 *    it from state.
 *  · The reason floor (20 characters, and no all-generic wording) is checked
 *    here only to spare a round trip. The server checks it too and its answer
 *    wins — this copy of the rule is not the one that enforces anything.
 */

import * as React from "react"
import { Check, Copy, Eye, RefreshCw, ShieldAlert } from "lucide-react"

import { Button } from "@/components/ui/button"
import { CreateSurface, CreateSurfaceBody, CreateSurfaceField, CreateSurfaceHeader } from "@/components/layout/create-surface"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { apiFetch } from "@/lib/api-fetch"

/** Mirrors minRevealReasonLen in internal/api/credentials_reveal.go. */
export const MIN_REVEAL_REASON_LENGTH = 20
export const REVEAL_VISIBLE_MS = 30_000

export interface RevealDialogProps {
  workspaceId: string
  credentialId: string
  credentialName: string
  /** Classification, when the payload carried one. See the note in the sheet. */
  sensitivity?: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Takes the user to the rotation flow instead — the offered better path. */
  onRotateInstead?: () => void
}

export function RevealDialog(props: RevealDialogProps) {
  // Unmount the secret-bearing session on close or target change. No value
  // or pending response can follow the user to another credential/workspace.
  return props.open ? <RevealDialogSession key={JSON.stringify([props.workspaceId, props.credentialId])} {...props} /> : null
}

function RevealDialogSession({
  workspaceId,
  credentialId,
  credentialName,
  sensitivity,
  open,
  onOpenChange,
  onRotateInstead,
}: RevealDialogProps) {
  const [reason, setReason] = React.useState("")
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState<string | null>(null)
  const [value, setValue] = React.useState<string | null>(null)
  const [copied, setCopied] = React.useState(false)
  const [copying, setCopying] = React.useState(false)
  const copyAttempt = React.useRef(0)
  const request = React.useRef<AbortController | null>(null)
  React.useEffect(() => () => { request.current?.abort(); copyAttempt.current++ }, [])

  React.useEffect(() => {
    if (value === null) return
    const hide = () => { copyAttempt.current++; setValue(null); setReason(""); setCopied(false); setCopying(false); setError(null) }
    const timer = window.setTimeout(hide, REVEAL_VISIBLE_MS)
    const visibility = () => { if (document.hidden) hide() }
    document.addEventListener("visibilitychange", visibility)
    return () => { window.clearTimeout(timer); document.removeEventListener("visibilitychange", visibility) }
  }, [value])

  async function copyValue(revealedValue: string) {
    const attempt = ++copyAttempt.current
    setCopying(true)
    setError(null)
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable")
      await navigator.clipboard.writeText(revealedValue)
      if (attempt === copyAttempt.current) setCopied(true)
    } catch {
      if (attempt === copyAttempt.current) setError("Could not copy the value. Check your browser’s clipboard permissions.")
    } finally {
      if (attempt === copyAttempt.current) setCopying(false)
    }
  }

  const reasonTooShort = reason.trim().length < MIN_REVEAL_REASON_LENGTH
  const [reasonError, setReasonError] = React.useState<string | null>(null)

  async function submit() {
    // The floor is checked here only to spare a round trip; the server checks it too.
    if (reasonTooShort) {
      setReasonError(`Write at least ${MIN_REVEAL_REASON_LENGTH} characters — ${MIN_REVEAL_REASON_LENGTH - reason.trim().length} more.`)
      return
    }
    setReasonError(null)
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/credentials/${encodeURIComponent(credentialId)}/reveal` +
          `?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          signal: controller.signal,
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ reason: reason.trim() }),
        },
      )
      const data = (await res.json().catch(() => ({}))) as { value?: string; error?: string }
      if (controller.signal.aborted) return
      if (document.hidden) {
        setReason("")
        setError("The tab was hidden. Request a new reveal to display the value.")
        return
      }
      if (!res.ok) {
        // The server's refusal messages name the layer that refused and what
        // to do about it ("an OWNER must enable it in Settings…"). Replacing
        // them with our own would send people to Slack instead of Settings.
        setError(typeof data.error === "string" ? data.error : `Reveal refused (HTTP ${res.status}).`)
        return
      }
      if (typeof data.value !== "string") {
        setError("Invalid reveal response — no value was displayed.")
        return
      }
      setValue(data.value)
    } catch {
      if (!controller.signal.aborted) setError("Network error — nothing was revealed.")
    } finally {
      if (!controller.signal.aborted) setSubmitting(false)
    }
  }

  const sealed = sensitivity === "SEALED"
  const fieldError = reasonError ?? error

  return (
    // No onSubmit on purpose: a reveal must never fire from Cmd+Enter.
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm">
      <CreateSurfaceHeader
        icon={Eye}
        accent="amber"
        context={credentialName}
        title="Reveal the value?"
        description="The reveal and your reason go to the audit log first. If the record cannot be written, nothing is shown."
        onClose={() => onOpenChange(false)}
      />
      <CreateSurfaceBody>

        {value === null ? (
          <div className="space-y-3">
            {sealed ? (
              <p role="alert" className="rounded-md border border-destructive/30 bg-chip-danger-bg px-3 py-2 text-label text-foreground">
                <ShieldAlert className="mr-1 inline size-3.5 text-destructive" aria-hidden />
                This secret is Sealed: it can never be revealed. Replace it instead.
              </p>
            ) : (
              <CreateSurfaceField
                label={`Reason (required, ${MIN_REVEAL_REASON_LENGTH} characters or more)`}
                htmlFor="reveal-reason"
                error={fieldError}
                hint="Generic reasons (“test”, “debug”) are rejected. Whoever reads this in six months is trying to reconstruct an incident."
              >
                <Textarea
                  id="reveal-reason"
                  rows={3}
                  autoFocus
                  value={reason}
                  onChange={(e) => { setReason(e.target.value); setError(null); setReasonError(null) }}
                  placeholder="What do you need the value for?"
                  aria-invalid={fieldError ? true : undefined}
                  aria-describedby={fieldError ? "reveal-reason-error" : undefined}
                  className="text-control"
                />
              </CreateSurfaceField>
            )}
            <p className="text-label text-muted-foreground">
              The value hides after 30 seconds or when you leave the tab. Sealed secrets cannot be revealed.
            </p>
            {onRotateInstead && (
              <p className="text-label text-muted-foreground">
                Already have a new value?{" "}
                <button type="button" className="text-primary hover:underline" onClick={onRotateInstead}>
                  <RefreshCw className="mr-0.5 inline size-3" aria-hidden />Replace value instead
                </button>
              </p>
            )}

            <div className="flex items-center justify-end gap-2 pt-1">
              <Button variant="ghost" size="sm" onClick={() => onOpenChange(false)} disabled={submitting}>
                Cancel
              </Button>
              <Button
                size="sm"
                onClick={submit}
                disabled={submitting || reason.trim() === "" || sealed}
              >
                {submitting ? <Spinner className="h-3 w-3" /> : <Eye className="h-3 w-3" />}
                Reveal for 30 s
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="rounded-md border border-warn/40 bg-warn/[0.06] px-3 py-2 text-label">
              Hidden automatically after 30 seconds, when you leave this tab, or when you close this dialog. A second look needs a second reveal,
              with its own reason and its own audit entry.
            </div>
            <div className="rounded-md border border-foreground/10 bg-background px-3 py-2">
              <code className="block break-all font-mono text-xs" data-testid="revealed-value">
                {value}
              </code>
            </div>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => copyValue(value)}
                disabled={copying}
              >
                {copied ? <Check className="mr-1.5 h-3 w-3" /> : <Copy className="mr-1.5 h-3 w-3" />}
                {copied ? "Copied" : "Copy"}
              </Button>
              <Button size="sm" className="ml-auto" onClick={() => onOpenChange(false)}>
                Done
              </Button>
            </div>
            {error && <p role="alert" className="text-xs text-destructive">{error}</p>}
          </div>
        )}
      </CreateSurfaceBody>
    </CreateSurface>
  )
}
