"use client"

/**
 * Test in Add AI provider › Connect (VLT-01, vault.md Q05; v67 `vpadd` step 2
 * "Test"). Sends the pasted key to the existing pre-save probe,
 * POST /api/v1/credentials/test with the PROVIDER_LOGIN shape — the server's
 * probeProviderLogin turns an API key into the provider's key probe and a
 * Claude setup token into its OAuth probe (credentials_test_endpoint.go).
 *
 * The value only travels in the request body. It is never rendered, logged or
 * kept here; a result is dropped as soon as the value, provider or mode
 * changes, so it can never describe a key that is no longer in the field.
 * `supported: false` is the server saying it has no probe — that is not shown
 * as a pass.
 */

import * as React from "react"
import { CheckCircle2, CircleSlash, Play, TriangleAlert } from "lucide-react"

import { Button } from "@/components/ui/button"
import { apiFetch } from "@/lib/api-fetch"

export type KeyTestState =
  | { kind: "idle" }
  | { kind: "busy" }
  | { kind: "ok" }
  | { kind: "rejected"; reason: string }
  | { kind: "unsupported" }
  | { kind: "failed" }

export function ProviderKeyTest({ provider, providerLabel, mode, value, onResult }: {
  provider: string
  providerLabel: string
  mode: "api_key" | "subscription"
  value: string
  /** True only while the value in the field has passed. */
  onResult?: (passed: boolean) => void
}) {
  const [state, setState] = React.useState<KeyTestState>({ kind: "idle" })
  const run = React.useRef(0)

  // A result belongs to one value: any change drops it (and any answer still in flight).
  React.useEffect(() => {
    run.current += 1
    setState({ kind: "idle" })
    onResult?.(false)
    // onResult is a setter from the wizard; the value is what matters.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [provider, mode, value])

  const test = async () => {
    const mine = ++run.current
    setState({ kind: "busy" })
    let next: KeyTestState
    try {
      const res = await apiFetch("/api/v1/credentials/test", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ provider, type: "PROVIDER_LOGIN", mode, value }),
      })
      const data = res.ok ? ((await res.json().catch(() => null)) as { valid?: boolean; supported?: boolean; error?: string } | null) : null
      if (!data) next = { kind: "failed" }
      else if (data.supported !== true) next = { kind: "unsupported" }
      else if (data.valid === true) next = { kind: "ok" }
      else next = { kind: "rejected", reason: data.error || `${providerLabel} did not accept it.` }
    } catch {
      next = { kind: "failed" }
    }
    if (mine !== run.current) return
    setState(next)
    onResult?.(next.kind === "ok")
  }

  const what = mode === "api_key" ? "key" : "token"
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5" data-slot="provider-key-test">
      <Button type="button" variant="outline" size="sm" disabled={!value.trim() || state.kind === "busy"} onClick={() => void test()}>
        <Play className="size-3.5" aria-hidden /> {state.kind === "busy" ? "Testing…" : "Test"}
      </Button>
      {state.kind === "idle" && <span className="type-meta text-muted-foreground">Checks the {what} with {providerLabel} before you save. Nothing is stored.</span>}
      {state.kind === "busy" && <span role="status" className="type-meta text-muted-foreground">Asking {providerLabel}…</span>}
      {state.kind === "ok" && (
        <span role="status" className="inline-flex items-center gap-1.5 type-meta text-success">
          <CheckCircle2 className="size-3.5" aria-hidden /> The {what} works
        </span>
      )}
      {state.kind === "rejected" && (
        <span role="alert" className="inline-flex items-center gap-1.5 type-meta text-destructive">
          <TriangleAlert className="size-3.5" aria-hidden /> {state.reason}
        </span>
      )}
      {state.kind === "unsupported" && (
        <span role="status" className="inline-flex items-center gap-1.5 type-meta text-muted-foreground">
          <CircleSlash className="size-3.5" aria-hidden /> {providerLabel} {what}s can’t be checked before saving. The first run will tell.
        </span>
      )}
      {state.kind === "failed" && (
        <span role="alert" className="inline-flex items-center gap-1.5 type-meta text-warn">
          <TriangleAlert className="size-3.5" aria-hidden /> The test could not run. Try again.
        </span>
      )}
    </div>
  )
}
