"use client"

import * as React from "react"

import { Button } from "@/components/ui/button"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { AUTH_BUTTON, AuthLink } from "@/components/branding/auth-card"
import type { SignedOutKind } from "@/lib/signed-out"
import { cn } from "@/lib/utils"

/**
 * The full-screen states between sessions (#3787, wireframe round 64 Spojení
 * §4): a centred card on the app ground with the sail in its blue tile, a
 * title, one sentence and one action. They replace the login form's warn
 * banner ("Your session expired") and the bare form after Log out, and the
 * skeleton-then-login detour when the backend is down while the app opens.
 *
 * None of them is an error: no warning or danger colour anywhere, the tile is
 * the brand's own blue.
 */
function StateCard({
  state,
  title,
  pulse = false,
  children,
}: {
  state: SignedOutKind | "restarting"
  title: string
  pulse?: boolean
  children: React.ReactNode
}) {
  return (
    <main
      data-slot="signed-out-screen"
      data-state={state}
      // touch-form: 44 px controls under a coarse pointer (globals.css).
      className="touch-form flex min-h-dvh flex-col items-center justify-center bg-sidebar px-4 py-8 text-foreground"
    >
      <section className="flex w-full max-w-[400px] flex-col items-center gap-3 rounded-xl border border-border bg-card px-6 py-8 text-center">
        <span data-slot="signed-out-mark" className={cn("rounded-xl", pulse && "signed-out-pulse")} aria-hidden>
          <CrewshipLogoTile size="size-11" rounded="rounded-xl" iconSize="size-6" />
        </span>
        <h1 className="mt-1 text-heading font-semibold text-balance">{title}</h1>
        {children}
      </section>
    </main>
  )
}

const COPY: Record<SignedOutKind, { title: string; body: string; action: string }> = {
  expired: { title: "Your session expired", body: "Sign in again to pick up where you left off.", action: "Sign in" },
  revoked: {
    title: "Signed out on this device",
    body: "Someone signed you out from another device or an admin ended your session.",
    action: "Sign in",
  },
  "signed-out": { title: "You're signed out", body: "See you next time.", action: "Sign in again" },
}

export function SignedOutScreen({
  kind,
  email = null,
  draftAgent = null,
  onSignIn,
  onUseAnotherAccount,
}: {
  kind: SignedOutKind
  /** Who was signed in (expired only). */
  email?: string | null
  /** The agent an unsent chat message is for; "" when it is not known. Expired only. */
  draftAgent?: string | null
  onSignIn: () => void
  onUseAnotherAccount?: () => void
}) {
  const copy = COPY[kind]
  const draft =
    kind === "expired" && draftAgent != null
      ? draftAgent
        ? ` Your unsent message to ${draftAgent} is kept on this device.`
        : " Your unsent message is kept on this device."
      : ""
  return (
    <StateCard state={kind} title={copy.title}>
      <p className="max-w-[36ch] text-body text-muted-foreground">
        {copy.body}
        {draft}
      </p>
      <Button type="button" onClick={onSignIn} className={cn("mt-2 min-w-32", AUTH_BUTTON)}>
        {copy.action}
      </Button>
      {kind === "expired" && onUseAnotherAccount && (
        <p className="text-label text-muted-foreground">
          {email && <>Signed in as {email} · </>}
          <button
            type="button"
            onClick={onUseAnotherAccount}
            className="rounded-sm text-primary-hover underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:min-h-[44px]"
          >
            Use another account
          </button>
        </p>
      )}
      {kind === "signed-out" && <AuthLink href="https://crewship.ai">Back to crewship.ai</AuthLink>}
    </StateCard>
  )
}

/** Whole seconds until `at`, never below zero. */
function secondsUntil(at: number): number {
  return Math.max(0, Math.ceil((at - Date.now()) / 1000))
}

/**
 * The backend cannot be reached while the app is opening, before there is a
 * shell to put a banner in. The auth provider keeps trying on its own
 * (hooks/use-auth.tsx, `unreachable`); this says when, and Try now does it at
 * once. Crewship being down and Crewship restarting look the same from here,
 * and a restart is by far the common case, so this says restarting.
 */
export function RestartingScreen({ retryAt, onRetry }: { retryAt: number; onRetry: () => void }) {
  const [seconds, setSeconds] = React.useState(() => secondsUntil(retryAt))
  React.useEffect(() => {
    setSeconds(secondsUntil(retryAt))
    const timer = window.setInterval(() => setSeconds(secondsUntil(retryAt)), 1000)
    return () => window.clearInterval(timer)
  }, [retryAt])
  return (
    <StateCard state="restarting" title="Crewship is restarting" pulse>
      <p className="max-w-[36ch] text-body text-muted-foreground">
        This usually takes under a minute. The page reconnects on its own.
      </p>
      <p className="text-label text-muted-foreground" role="status" aria-live="off">
        {seconds > 0 ? <>Next try in {seconds} s</> : <>Trying now…</>}
        {" · "}
        <button
          type="button"
          onClick={onRetry}
          className="rounded-sm text-primary-hover underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50 coarse:min-h-[44px]"
        >
          Try now
        </button>
      </p>
    </StateCard>
  )
}
