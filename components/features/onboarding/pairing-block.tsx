"use client"

import { Check, Copy, KeyRound, RefreshCw } from "lucide-react"

import { Button } from "@/components/ui/button"
import { StatusPill } from "@/components/ui/status-pill"
import { SetupBanner, SetupNote } from "./setup-card"

export type PairStatus = "idle" | "starting" | "pending" | "consumed" | "expired" | "failed"

/**
 * The CLI pairing block of the setup's Model step — the command to paste, then
 * one line for each state the code goes through. It is rendered in the step,
 * not in a dialog: pairing is optional there and sits next to the token field
 * it may make unnecessary.
 *
 * State and requests stay in the page (the start / poll / countdown effects
 * and the guard against minting two codes); this only draws them.
 */
export function PairingBlock({
  status,
  command,
  remainingSec,
  tokenDelivered,
  copied,
  onCopy,
  onNewCode,
  onRetry,
}: {
  status: PairStatus
  command: string
  remainingSec: number | null
  tokenDelivered: boolean
  copied: boolean
  onCopy: () => void
  onNewCode: () => void
  onRetry: () => void
}) {
  return (
    <div className="space-y-2" data-testid="onboarding-pairing">
      <p className="text-label leading-relaxed text-muted-foreground">
        Don&apos;t have the Crewship CLI yet? Download it from{" "}
        <a
          href="https://github.com/crewship-ai/crewship/releases"
          target="_blank"
          rel="noopener noreferrer"
          className="font-medium text-primary underline-offset-2 hover:underline"
        >
          GitHub releases
        </a>
        , then run this on the machine where it lives:
      </p>

      {command ? (
        <>
          <div className="flex min-w-0 items-center gap-2 rounded-md border border-line-strong bg-muted py-1.5 pl-3 pr-1.5">
            <code className="min-w-0 flex-1 select-all break-all font-mono text-label text-foreground">{command}</code>
            <Button type="button" variant="outline" size="sm" onClick={onCopy} aria-label="Copy command">
              {copied ? <Check className="size-3.5 text-success" aria-hidden /> : <Copy className="size-3.5" aria-hidden />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>

          {status === "pending" && (
            <SetupNote>
              Waiting for the CLI…
              {remainingSec !== null && (
                <>
                  {" "}
                  code expires in{" "}
                  <span className={remainingSec < 60 ? "font-mono tabular-nums text-chip-warn-fg" : "font-mono tabular-nums"}>
                    {formatCountdown(remainingSec)}
                  </span>
                </>
              )}
            </SetupNote>
          )}

          {status === "consumed" && (
            <SetupNote icon={KeyRound} role="status">
              <span className="flex flex-wrap items-center gap-2">
                <StatusPill tone="success" label="Paired" />
                <span className="min-w-0">
                  {tokenDelivered ? (
                    <>
                      <strong className="font-semibold text-foreground">CLI paired and model token received.</strong>{" "}
                      Nothing to paste.
                    </>
                  ) : (
                    <>
                      <strong className="font-semibold text-foreground">CLI paired.</strong> Your terminal is signed in;
                      the agents still need their own model token.
                    </>
                  )}
                </span>
              </span>
            </SetupNote>
          )}

          {status === "expired" && (
            <SetupNote tone="warn">
              <span className="flex flex-wrap items-center justify-between gap-2">
                <span>The code expired.</span>
                <Button type="button" variant="outline" size="sm" onClick={onNewCode}>
                  <RefreshCw className="size-3.5" aria-hidden />
                  New code
                </Button>
              </span>
            </SetupNote>
          )}
        </>
      ) : status === "failed" ? (
        <SetupBanner
          role="alert"
          action={
            <Button type="button" variant="outline" size="sm" onClick={onRetry}>
              Retry
            </Button>
          }
        >
          Couldn&apos;t start pairing. Check your connection.
        </SetupBanner>
      ) : (
        <SetupNote role="status">Generating a code…</SetupNote>
      )}
    </div>
  )
}

/** "m:ss" for the code's remaining life. */
export function formatCountdown(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${s.toString().padStart(2, "0")}`
}
