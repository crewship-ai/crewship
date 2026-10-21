"use client"

import { useState, type FormEvent } from "react"
import { ArrowLeft, KeyRound, Terminal } from "lucide-react"
import { AUTH_BUTTON, AUTH_INPUT, AuthCard, AuthLink, AuthReceipt, AuthScreen } from "@/components/branding/auth-card"
import { CreateSurfaceField, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { serverFetch } from "@/lib/server-base"

export default function ForgotPasswordPage() {
  const [email, setEmail] = useState("")
  const [loading, setLoading] = useState(false)
  const [submitted, setSubmitted] = useState(false)

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setLoading(true)
    try {
       
      await serverFetch("/api/v1/auth/forgot", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email }),
      })
    } catch {
      // Swallow network errors — the no-enumeration response shape
      // means a real failure looks identical to a real success from
      // the user's POV, and a visible error would itself be a side
      // channel ("oh, it WOULD have worked but I'm offline").
    } finally {
      // finally so the spinner can't get wedged if fetch() throws
      // synchronously (e.g. a malformed URL) — try/catch above only
      // handles the awaited rejection.
      setLoading(false)
      setSubmitted(true)
    }
  }

  return (
    <AuthScreen panel="recovery">
      <AuthCard
        icon={KeyRound}
        title="Reset your password"
        description="Enter the email on your account and we'll send you a reset link."
        onSubmit={submitted ? undefined : handleSubmit}
        links={
          <AuthLink href="/login" className="gap-1.5">
            <ArrowLeft aria-hidden className="size-3.5" />
            Back to sign in
          </AuthLink>
        }
        actions={
          submitted ? undefined : (
            <Button type="submit" disabled={loading || !email} className={AUTH_BUTTON}>
              {loading && <Spinner className="size-3.5" />}
              {loading ? "Sending…" : "Send reset link"}
            </Button>
          )
        }
      >
        {submitted ? (
          // One answer for every address and every failure: the response
          // shape is non-enumerating, so this receipt must be too.
          <div role="status" aria-live="polite" className="flex flex-col gap-3">
            <AuthReceipt title="Check your inbox">
              If an account exists for that email and email is configured on this server, a reset
              link has been sent.
            </AuthReceipt>
            <CreateSurfaceNotice tone="info" icon={Terminal}>
              Self-hosted administrators without email configured should run{" "}
              <code className="font-mono text-foreground">crewship admin reset-password</code> on the
              server.
            </CreateSurfaceNotice>
          </div>
        ) : (
          <CreateSurfaceField label="Email" htmlFor="email">
            <Input
              id="email"
              type="email"
              autoComplete="username"
              placeholder="you@company.com"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              required
              autoFocus
              className={AUTH_INPUT}
            />
          </CreateSurfaceField>
        )}
      </AuthCard>
    </AuthScreen>
  )
}
