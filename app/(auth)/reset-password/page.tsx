"use client"

import { Suspense, useEffect, useState, type FormEvent } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { ArrowLeft, KeyRound } from "lucide-react"
import { AUTH_BUTTON, AUTH_INPUT, AuthCard, AuthLink, AuthReceipt, AuthScreen } from "@/components/branding/auth-card"
import { CreateSurfaceField } from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { serverFetch } from "@/lib/server-base"

export default function ResetPasswordPage() {
  return (
    <Suspense>
      <ResetForm />
    </Suspense>
  )
}

function ResetForm() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const token = searchParams.get("token") ?? ""

  const [password, setPassword] = useState("")
  const [confirm, setConfirm] = useState("")
  const [error, setError] = useState<string | null>(null)
  // A wrong value belongs under its field; `error` is the link or the server.
  const [fieldError, setFieldError] = useState<{ field: "password" | "confirm"; message: string } | null>(null)
  const [loading, setLoading] = useState(false)
  const [done, setDone] = useState(false)

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setError(null)
    setFieldError(null)
    if (!token) {
      setError("Missing or invalid reset link. Request a new one from the login page.")
      return
    }
    if (password.length < 8) {
      setFieldError({ field: "password", message: "Password must be at least 8 characters." })
      return
    }
    if (password !== confirm) {
      setFieldError({ field: "confirm", message: "Passwords don't match." })
      return
    }
    setLoading(true)
    try {
       
      const res = await serverFetch("/api/v1/auth/reset", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, new_password: password }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) {
        setError(data?.error ?? "Reset failed. The link may have expired.")
        setLoading(false)
        return
      }
      setDone(true)
      setLoading(false)
    } catch {
      setError("Network error. Please try again.")
      setLoading(false)
    }
  }

  // Delayed redirect with cleanup — if the user closes the tab or
  // navigates away before the 2s elapses, the timer is cleared so we
  // don't fire router.push() on an unmounted component.
  useEffect(() => {
    if (!done) return
    const timer = window.setTimeout(() => router.push("/login"), 2000)
    return () => window.clearTimeout(timer)
  }, [done, router])

  const passwordError = fieldError?.field === "password" ? fieldError.message : null
  const confirmError = fieldError?.field === "confirm" ? fieldError.message : null

  return (
    <AuthScreen>
      <AuthCard
        icon={KeyRound}
        title="Choose a new password"
        description="Pick something strong — at least 8 characters."
        onSubmit={done ? undefined : handleSubmit}
        refusal={done ? null : error}
        links={
          done ? undefined : (
            <AuthLink href="/login" className="gap-1.5">
              <ArrowLeft aria-hidden className="size-3.5" />
              Back to sign in
            </AuthLink>
          )
        }
        actions={
          done ? (
            // The redirect still fires after 2 s; this is for whoever does not
            // want to wait for it.
            <Button asChild className={AUTH_BUTTON}>
              <a href="/login">Sign in now</a>
            </Button>
          ) : (
            <Button type="submit" disabled={loading} className={AUTH_BUTTON}>
              {loading && <Spinner className="size-3.5" />}
              {loading ? "Updating…" : "Update password"}
            </Button>
          )
        }
      >
        {done ? (
          <div role="status" aria-live="polite">
            <AuthReceipt title="Password updated">
              All existing sessions have been signed out. Taking you to sign in…
            </AuthReceipt>
          </div>
        ) : (
          <>
            <CreateSurfaceField label="New password" htmlFor="password" error={passwordError}>
              <Input
                id="password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => {
                  setPassword(e.target.value)
                  if (fieldError?.field === "password") setFieldError(null)
                }}
                required
                autoFocus
                aria-invalid={passwordError ? true : undefined}
                aria-describedby={passwordError ? "password-error" : undefined}
                className={AUTH_INPUT}
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Confirm new password" htmlFor="confirm" error={confirmError}>
              <Input
                id="confirm"
                type="password"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => {
                  setConfirm(e.target.value)
                  if (fieldError?.field === "confirm") setFieldError(null)
                }}
                required
                aria-invalid={confirmError ? true : undefined}
                aria-describedby={confirmError ? "confirm-error" : undefined}
                className={AUTH_INPUT}
              />
            </CreateSurfaceField>
          </>
        )}
      </AuthCard>
    </AuthScreen>
  )
}
