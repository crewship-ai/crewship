"use client"

import * as React from "react"
import { Check, ChevronRight } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import {
  Sheet, SheetContent, SheetHeader, SheetTitle, SheetDescription,
} from "@/components/ui/sheet"
import { cn } from "@/lib/utils"
import { StepProvider } from "./step-provider"
import { StepAuth } from "./step-auth"
import { StepPaste } from "./step-paste"
import { StepIdentity } from "./step-identity"
import type { WizardState, WizardStep } from "./types"
import { INITIAL } from "./types"
import { apiFetch } from "@/lib/api-fetch"

const STEP_LABELS: Record<WizardStep, { title: string; sub: string }> = {
  1: { title: "Provider", sub: "pick a service" },
  2: { title: "Auth method", sub: "how you'll authenticate" },
  3: { title: "Paste & test", sub: "validate the value" },
  4: { title: "Identity & scope", sub: "name, scope, agents" },
}

export interface AddCredentialWizardProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSuccess: () => void
}

export function AddCredentialWizard({ workspaceId, open, onOpenChange, onSuccess }: AddCredentialWizardProps) {
  const [state, setStateFull] = React.useState<WizardState>(INITIAL)

  React.useEffect(() => {
    if (!open) setStateFull(INITIAL)
  }, [open])

  const setState = React.useCallback((patch: Partial<WizardState>) => {
    setStateFull((prev) => ({ ...prev, ...patch }))
  }, [])

  const stepValid = stepIsValid(state)

  // Submit: Cmd+Enter on the last step or click "Create credential"
  const submittingRef = React.useRef(false)

  const submit = async () => {
    if (submittingRef.current || state.submitting) return
    submittingRef.current = true
    setStateFull((s) => ({ ...s, submitting: true, error: null }))
    try {
      const body: Record<string, unknown> = {
        name: state.name.trim(),
        value: state.value.trim(),
        type: state.type,
        provider: state.provider,
        scope: state.scope,
        account_label: state.accountLabel.trim(),
      }
      // USERPASS carries a cleartext username in its own field so the
      // backend stores it in the dedicated `username` column rather
      // than packing it into the encrypted value. PEM types and other
      // single-value credentials send no username.
      if (state.type === "USERPASS" && state.username.trim()) {
        body.username = state.username.trim()
      }
      // PEM bodies must NOT be .trim()'d above — leading whitespace
      // before -----BEGIN is fine but trimming trailing newlines can
      // break some PEM parsers. Send PEM types verbatim.
      if (state.type === "SSH_KEY" || state.type === "CERTIFICATE") {
        body.value = state.value
      }
      if (state.description.trim()) body.description = state.description.trim()
      if (state.expiresAt) body.token_expires_at = new Date(state.expiresAt).toISOString()
      if (state.scope === "CREW") body.crew_ids = state.crewIds

      const res = await apiFetch(`/api/v1/credentials?workspace_id=${workspaceId}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setStateFull((s) => ({ ...s, submitting: false, error: typeof data.error === "string" ? data.error : "Failed to create credential" }))
        return
      }
      onSuccess()
      onOpenChange(false)
    } catch {
      setStateFull((s) => ({ ...s, submitting: false, error: "Network error" }))
    } finally {
      submittingRef.current = false
    }
  }

  const advance = () => {
    if (state.step === 4) { submit(); return }
    setStateFull((s) => ({ ...s, step: (s.step + 1) as WizardStep }))
  }
  const back = () => state.step > 1 && setStateFull((s) => ({ ...s, step: (s.step - 1) as WizardStep }))
  const jumpTo = (n: WizardStep) => n < state.step && setStateFull((s) => ({ ...s, step: n }))

  // Cmd+Enter advances/submits.
  React.useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
        e.preventDefault()
        if (stepValid) advance()
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, state.step, stepValid, state])

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent side="right" className="sm:max-w-[720px] p-0 flex flex-col">
        <SheetHeader className="px-5 pt-4 pb-3 border-b border-white/10">
          <SheetTitle className="text-base">
            New credential
            <span className="ml-2 text-sm text-muted-foreground font-normal">
              {state.step === 4 ? "— ready to save" : `— step ${state.step} of 4`}
            </span>
          </SheetTitle>
          <SheetDescription className="text-[12.5px]">
            {state.step === 1 && "Pick the provider this credential authenticates against."}
            {state.step === 2 && "Choose how you'll authenticate. Provider-driven; we picked the recommended default."}
            {state.step === 3 && (
              state.type === "USERPASS"
                ? "Enter the username and password. Stored encrypted; injected as two env vars."
                : state.type === "SSH_KEY"
                  ? "Paste the PEM-encoded private key. Mounted at mode 0600 inside the container."
                  : state.type === "CERTIFICATE"
                    ? "Paste the PEM-encoded certificate. Mounted at mode 0400 inside the container."
                    : "Paste the value. We auto-test it as soon as you paste."
            )}
            {state.step === 4 && "Name it, scope it, and assign agents."}
          </SheetDescription>
        </SheetHeader>

        <StepStrip step={state.step} onJump={jumpTo} />

        <div className="flex-1 px-5 py-4 overflow-y-auto">
          {state.step === 1 && <StepProvider state={state} setState={setState} />}
          {state.step === 2 && <StepAuth state={state} setState={setState} />}
          {state.step === 3 && <StepPaste state={state} setState={setState} />}
          {state.step === 4 && <StepIdentity state={state} setState={setState} workspaceId={workspaceId} />}
        </div>

        {state.error && (
          <div className="px-5 py-2 text-xs text-red-400 border-t border-white/10">
            {state.error}
          </div>
        )}

        <div className="px-5 py-3 border-t border-white/10 flex items-center gap-2">
          <span className="text-[11.5px] text-muted-foreground mr-auto">
            {state.step === 4 ? "⌘+Enter to save · Esc cancel" : `Step ${state.step} of 4 · ⌘+Enter to continue`}
          </span>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            disabled={state.submitting}
            className="text-sm px-3 py-1.5 rounded text-muted-foreground hover:text-foreground"
          >
            Cancel
          </button>
          {state.step > 1 && (
            <button
              type="button"
              onClick={back}
              disabled={state.submitting}
              className="text-sm px-3 py-1.5 rounded border border-white/10 text-foreground/80 hover:bg-white/5"
            >
              ← Back
            </button>
          )}
          <button
            type="button"
            onClick={advance}
            disabled={!stepValid || state.submitting}
            className="text-sm px-3.5 py-1.5 rounded bg-blue-500 hover:bg-blue-400 text-white disabled:opacity-40 disabled:cursor-not-allowed flex items-center gap-1.5"
          >
            {state.submitting && <Spinner className="h-3 w-3" />}
            {state.step === 4 ? (state.submitting ? "Creating…" : "✓ Create credential") : "Continue"}
            {state.step < 4 && !state.submitting && <ChevronRight className="h-3.5 w-3.5" />}
          </button>
        </div>
      </SheetContent>
    </Sheet>
  )
}

function StepStrip({ step, onJump }: { step: WizardStep; onJump: (s: WizardStep) => void }) {
  return (
    <nav className="px-5 py-3 border-b border-white/10 bg-card/50 flex items-center gap-3">
      {([1, 2, 3, 4] as const).map((n, i) => (
        <React.Fragment key={n}>
          <div className="flex items-center gap-2 text-[12px] shrink-0 min-w-0">
            <button
              type="button"
              disabled={n >= step}
              onClick={() => onJump(n)}
              className={cn(
                "h-6 w-6 rounded-full border text-[11px] font-semibold flex items-center justify-center transition-all shrink-0",
                n < step
                  ? "bg-emerald-500/20 border-emerald-400/50 text-emerald-300 hover:scale-110 cursor-pointer"
                  : n === step
                    ? "bg-blue-500/20 border-blue-400 text-blue-300 ring-2 ring-blue-400/20"
                    : "bg-card border-white/10 text-muted-foreground cursor-default",
              )}
            >
              {n < step ? <Check className="h-3 w-3" strokeWidth={3} /> : n}
            </button>
            <div className={cn("flex flex-col leading-tight min-w-0", n !== step && "opacity-60")}>
              <span className="font-medium truncate">{STEP_LABELS[n].title}</span>
              <span className="text-[10.5px] text-muted-foreground truncate">{STEP_LABELS[n].sub}</span>
            </div>
          </div>
          {i < 3 && (
            <div className={cn("flex-1 h-px min-w-[16px] transition-colors", n < step ? "bg-emerald-400/40" : "bg-white/10")} />
          )}
        </React.Fragment>
      ))}
    </nav>
  )
}

function stepIsValid(s: WizardState): boolean {
  if (s.step === 1) return s.provider !== null
  if (s.step === 2) return s.authMethod !== null
  if (s.step === 3) {
    // USERPASS requires BOTH username and password — the backend
    // rejects USERPASS-without-username with a 400 ("username is
    // required"), surface that as an early step-gate so users don't
    // get rejected at the Identity step's submit.
    if (s.type === "USERPASS") {
      return s.username.trim().length > 0 && s.value.length > 0
    }
    // PEM types: don't trim before checking length — looksWrongShape
    // in step-paste does the structural hint; here we just need any
    // content so Continue activates.
    return s.value.trim().length > 0
  }
  if (s.step === 4) {
    if (s.name.trim().length === 0) return false
    if (s.accountLabel.trim().length === 0) return false
    if (s.scope === "CREW" && s.crewIds.length === 0) return false
    return true
  }
  return false
}
