"use client"

import { useEffect, useRef, useState, type FormEvent } from "react"
import { CircleCheck, KeyRound } from "lucide-react"

import { apiFetch } from "@/lib/api-fetch"
import { Checkbox } from "@/components/ui/checkbox"
import {
  CREATE_SURFACE_INPUT,
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
} from "@/components/layout/create-surface"
import { cn } from "@/lib/utils"

/**
 * Settings › Profile › Change password (Harbor 2, a small CreateSurface).
 *
 * A form, so Enter in any field submits it. What the server refuses stays in
 * the dialog, under the field it is about ("current password is incorrect"
 * under Current password); anything else is a notice above the footer. The
 * result is a state of the same dialog — "Password changed · other sessions
 * signed out" and a Done — not a toast that leaves before it is read. On a
 * phone the surface is a bottom sheet whose footer stays above the keyboard.
 */

type Field = "current" | "next" | "confirm"

function fieldOf(message: string): Field | null {
  const m = message.toLowerCase()
  if (m.includes("current password")) return "current"
  if (m.includes("new password")) return "next"
  return null
}

export function ChangePasswordDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [current, setCurrent] = useState("")
  const [next, setNext] = useState("")
  const [confirm, setConfirm] = useState("")
  // Opt-in: also revoke every CLI token. Off by default so a rotation does not
  // silently break a script or a teammate's paired terminal.
  const [revokeTokens, setRevokeTokens] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<{ field: Field | null; message: string } | null>(null)
  const [done, setDone] = useState<{ tokensRevoked: number } | null>(null)

  // Every opening starts clean: no password survives a close.
  useEffect(() => {
    if (open) return
    setCurrent(""); setNext(""); setConfirm(""); setRevokeTokens(false)
    setError(null); setDone(null); setSaving(false)
  }, [open])

  const dirty = !done && (current !== "" || next !== "" || confirm !== "")

  // The footer's primary sits inside the form, so one click is both its
  // onClick and the form's submit event — in the same tick, before `saving`
  // re-renders. The ref makes the second one a no-op.
  const inFlight = useRef(false)

  async function submit(e?: FormEvent) {
    e?.preventDefault()
    if (inFlight.current || saving || done) return
    if (next.length < 8) { setError({ field: "next", message: "New password must be at least 8 characters" }); return }
    if (next !== confirm) { setError({ field: "confirm", message: "New passwords do not match" }); return }
    inFlight.current = true
    setSaving(true)
    setError(null)
    try {
      const res = await apiFetch("/api/v1/users/me/password", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ current_password: current, new_password: next, revoke_cli_tokens: revokeTokens }),
      })
      const body = await res.json().catch(() => null)
      if (!res.ok) {
        const message = typeof body?.error === "string" ? body.error : `Couldn’t change the password (${res.status}).`
        setError({ field: fieldOf(message), message: message.charAt(0).toUpperCase() + message.slice(1) })
        return
      }
      setDone({ tokensRevoked: typeof body?.cli_tokens_revoked === "number" ? body.cli_tokens_revoked : 0 })
    } catch {
      setError({ field: null, message: "Couldn’t reach the server. Your password was not changed." })
    } finally {
      inFlight.current = false
      setSaving(false)
    }
  }

  const fieldError = (f: Field) => (error?.field === f ? error.message : undefined)
  const input = (id: string, f: Field, value: string, set: (v: string) => void, autoComplete: string, autoFocus = false) => (
    <input
      id={id}
      type="password"
      value={value}
      autoComplete={autoComplete}
      autoFocus={autoFocus}
      disabled={saving}
      aria-invalid={error?.field === f || undefined}
      aria-describedby={error?.field === f ? `${id}-error` : undefined}
      onChange={(e) => { set(e.target.value); if (error) setError(null) }}
      className={cn(
        CREATE_SURFACE_INPUT,
        "w-full rounded-md border border-control-border bg-surface-subtle px-2.5 text-foreground outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 aria-[invalid=true]:border-destructive",
      )}
    />
  )

  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" dirty={dirty} onSubmit={() => void submit()} ariaLabel="Change password">
      <CreateSurfaceHeader
        icon={KeyRound}
        title="Change password"
        description={done ? undefined : "At least 8 characters. Your other browser sessions are signed out."}
        onClose={() => onOpenChange(false)}
      />
      {done ? (
        <>
          <CreateSurfaceBody>
            <CreateSurfaceNotice tone="ok" icon={CircleCheck}>
              <span role="status">
                <b className="font-semibold">Password changed · other sessions signed out</b>
                <span className="mt-0.5 block text-muted-foreground">
                  {revokeTokens
                    ? `${done.tokensRevoked} CLI token${done.tokensRevoked === 1 ? "" : "s"} revoked. Terminals and scripts sign in again with a new one.`
                    : "CLI tokens keep working."}
                </span>
              </span>
            </CreateSurfaceNotice>
          </CreateSurfaceBody>
          <CreateSurfaceFooter hint={null} cancelLabel="Close" onCancel={() => onOpenChange(false)} primaryLabel="Done" onPrimary={() => onOpenChange(false)} />
        </>
      ) : (
        <form onSubmit={(e) => void submit(e)} className="flex min-h-0 flex-1 flex-col" noValidate>
          <CreateSurfaceBody className="flex flex-col gap-3">
            <CreateSurfaceField label="Current password" htmlFor="pw-current" error={fieldError("current")}>
              {input("pw-current", "current", current, setCurrent, "current-password", true)}
            </CreateSurfaceField>
            <CreateSurfaceField label="New password" htmlFor="pw-new" error={fieldError("next")}>
              {input("pw-new", "next", next, setNext, "new-password")}
            </CreateSurfaceField>
            <CreateSurfaceField label="Confirm new password" htmlFor="pw-confirm" error={fieldError("confirm")}>
              {input("pw-confirm", "confirm", confirm, setConfirm, "new-password")}
            </CreateSurfaceField>
            <label className="flex items-start gap-2 pt-1 text-xs">
              <Checkbox
                checked={revokeTokens}
                onCheckedChange={(v) => setRevokeTokens(v === true)}
                disabled={saving}
                aria-label="Also revoke all CLI tokens"
                className="mt-0.5"
              />
              <span>
                <span className="font-medium">Also revoke all CLI tokens</span>
                <span className="block text-muted-foreground">Every terminal and script signed in as you stops working until it signs in again.</span>
              </span>
            </label>
            {error && error.field === null && <CreateSurfaceNotice tone="error">{error.message}</CreateSurfaceNotice>}
          </CreateSurfaceBody>
          {/* A real submit button, so Enter in any field sends the form. */}
          <button type="submit" hidden aria-hidden tabIndex={-1} />
          <CreateSurfaceFooter onCancel={() => onOpenChange(false)} primaryLabel="Change password" onPrimary={() => void submit()} busy={saving} />
        </form>
      )}
    </CreateSurface>
  )
}
