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
 *  · The offered way out — "Rotate instead" — is a primary button here, not a
 *    footnote. §2.6 L8: most legitimate reasons to reveal are really reasons
 *    to rotate, and a control used rarely is a control that keeps working.
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
import {
  Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle,
} from "@/components/ui/dialog"
import { Label } from "@/components/ui/label"
import { Spinner } from "@/components/ui/spinner"
import { Textarea } from "@/components/ui/textarea"
import { apiFetch } from "@/lib/api-fetch"
import { cn } from "@/lib/utils"

/** Mirrors minRevealReasonLen in internal/api/credentials_reveal.go. */
export const MIN_REVEAL_REASON_LENGTH = 20

export interface RevealDialogProps {
  workspaceId: string
  credentialId: string
  credentialName: string
  /** Classification, when the payload carried one. See the note in the sheet. */
  sensitivity?: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Takes the user to the rotation flow instead — the offered better path. */
  onRotateInstead: () => void
}

export function RevealDialog({
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

  // Nothing about a completed reveal survives the dialog closing — not the
  // value, not the reason that justified it.
  React.useEffect(() => {
    if (open) return
    setReason("")
    setError(null)
    setValue(null)
    setCopied(false)
    setSubmitting(false)
  }, [open])

  const reasonTooShort = reason.trim().length < MIN_REVEAL_REASON_LENGTH

  async function submit() {
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch(
        `/api/v1/credentials/${encodeURIComponent(credentialId)}/reveal` +
          `?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ reason: reason.trim() }),
        },
      )
      const data = (await res.json().catch(() => ({}))) as { value?: string; error?: string }
      if (!res.ok) {
        // The server's refusal messages name the layer that refused and what
        // to do about it ("an OWNER must enable it in Settings…"). Replacing
        // them with our own would send people to Slack instead of Settings.
        setError(typeof data.error === "string" ? data.error : `Reveal refused (HTTP ${res.status}).`)
        return
      }
      setValue(typeof data.value === "string" ? data.value : "")
    } catch {
      setError("Network error — nothing was revealed.")
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-[520px]">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-base">
            <ShieldAlert className="h-4 w-4 text-warn" />
            Reveal <span className="font-mono">{credentialName}</span>
          </DialogTitle>
          <DialogDescription className="text-xs">
            Every reveal is written to the tamper-evident journal before the value is returned. If
            the record cannot be written, the value is not returned.
          </DialogDescription>
        </DialogHeader>

        {value === null ? (
          <div className="space-y-4">
            <ul className="space-y-1.5 text-[11px] text-muted-foreground">
              <Condition ok>Reveal is enabled for this workspace</Condition>
              <Condition ok>
                You hold <span className="font-mono">credentials:reveal</span>
              </Condition>
              <Condition ok={sensitivity ? sensitivity !== "SEALED" : undefined}>
                {sensitivity
                  ? `Classification ${sensitivity}${sensitivity === "SEALED" ? " — never revealable" : " allows it"}`
                  : "Classification is checked by the server when you submit"}
              </Condition>
              <Condition ok={!reasonTooShort}>
                A reason of at least {MIN_REVEAL_REASON_LENGTH} characters
              </Condition>
            </ul>

            <div className="space-y-1.5">
              <Label htmlFor="reveal-reason" className="text-xs">Reason</Label>
              <Textarea
                id="reveal-reason"
                rows={3}
                value={reason}
                onChange={(e) => { setReason(e.target.value); setError(null) }}
                placeholder="What do you need the value for? This is recorded in the audit log."
                className="text-sm"
              />
              <p className="text-[11px] text-muted-foreground">
                Generic reasons (&ldquo;test&rdquo;, &ldquo;debug&rdquo;) are rejected. Whoever reads
                this in six months is trying to reconstruct an incident.
              </p>
            </div>

            <div className="rounded-md border border-primary/25 bg-primary/[0.06] px-3 py-2.5 text-xs">
              <div className="font-medium">Have you considered rotating?</div>
              <p className="mt-1 text-[11px] text-muted-foreground">
                If you need the value in order to paste it somewhere, rotation is the safer route:
                the new value is shown once at creation and the old one drains through the grace
                window, so nothing existing is ever disclosed.
              </p>
              <Button size="sm" className="mt-2" onClick={onRotateInstead}>
                <RefreshCw className="mr-1.5 h-3 w-3" />
                Rotate instead
              </Button>
            </div>

            {error && (
              <div
                role="alert"
                className="rounded-md border border-destructive/30 bg-destructive/[0.05] px-3 py-2 text-xs text-destructive"
              >
                {error}
              </div>
            )}

            <div className="flex items-center gap-2">
              <Button variant="outline" size="sm" onClick={() => onOpenChange(false)} disabled={submitting}>
                Cancel
              </Button>
              <Button
                variant="outline"
                size="sm"
                className="ml-auto"
                onClick={submit}
                disabled={submitting || reasonTooShort || sensitivity === "SEALED"}
              >
                {submitting ? <Spinner className="mr-1.5 h-3 w-3" /> : <Eye className="mr-1.5 h-3 w-3" />}
                Reveal the existing value
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-3">
            <div className="rounded-md border border-warn/40 bg-warn/[0.06] px-3 py-2 text-[11px]">
              Shown once. Close this dialog and it is gone — a second look needs a second reveal,
              with its own reason and its own audit entry.
            </div>
            <div className="rounded-md border border-white/10 bg-background px-3 py-2">
              <code className="block break-all font-mono text-xs" data-testid="revealed-value">
                {value}
              </code>
            </div>
            <div className="flex items-center gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={() => {
                  void navigator.clipboard?.writeText(value)
                  setCopied(true)
                }}
              >
                {copied ? <Check className="mr-1.5 h-3 w-3" /> : <Copy className="mr-1.5 h-3 w-3" />}
                {copied ? "Copied" : "Copy"}
              </Button>
              <Button size="sm" className="ml-auto" onClick={() => onOpenChange(false)}>
                Done
              </Button>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Condition({ ok, children }: { ok?: boolean; children: React.ReactNode }) {
  return (
    <li className="flex items-start gap-2">
      <span
        aria-hidden="true"
        className={cn(
          "mt-[3px] inline-block h-1.5 w-1.5 shrink-0 rounded-full",
          ok === true ? "bg-success" : ok === false ? "bg-destructive" : "bg-muted-foreground/40",
        )}
      />
      <span>{children}</span>
    </li>
  )
}
