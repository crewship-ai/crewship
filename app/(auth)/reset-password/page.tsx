"use client"

import { Suspense, useEffect, useState, type FormEvent } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { ArrowLeft, KeyRound } from "lucide-react"
import { AUTH_BUTTON, AuthCard, AuthLink, AuthPasswordInput, AuthReceipt, AuthScreen } from "@/components/branding/auth-card"
import { CreateSurfaceField } from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { serverFetch } from "@/lib/server-base"

const REDIRECT_SECONDS = 5

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

  // A visible countdown to sign-in (#3583) instead of a silent 2 s jump: the
  // person sees where they are going and can go now. The interval is cleared
  // on unmount, so a closed tab never fires router.push().
  const [secondsLeft, setSecondsLeft] = useState(REDIRECT_SECONDS)
  useEffect(() => {
    if (!done) return
    const timer = window.setInterval(() => setSecondsLeft((s) => s - 1), 1000)
    return () => window.clearInterval(timer)
  }, [done])
  useEffect(() => {
    if (done && secondsLeft <= 0) router.push("/login")
  }, [done, secondsLeft, router])

  const passwordError = fieldError?.field === "password" ? fieldError.message : null
  const confirmError = fieldError?.field === "confirm" ? fieldError.message : null

  return (
    <AuthScreen panel="reset">
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
            <Button asChild className={AUTH_BUTTON}>
              <a href="/login">Sign in</a>
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
              All existing sessions have been signed out.
            </AuthReceipt>
            <p className="mt-3 text-label text-muted-foreground">Opening sign-in in {Math.max(secondsLeft, 0)} s…</p>
          </div>
        ) : (
          <>
            <CreateSurfaceField label="New password" htmlFor="password" error={passwordError}>
              <AuthPasswordInput
                id="password"
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
                rule
              />
            </CreateSurfaceField>
            <CreateSurfaceField label="Confirm new password" htmlFor="confirm" error={confirmError}>
              <AuthPasswordInput
                id="confirm"
                autoComplete="new-password"
                value={confirm}
                onChange={(e) => {
                  setConfirm(e.target.value)
                  if (fieldError?.field === "confirm") setFieldError(null)
                }}
                required
                aria-invalid={confirmError ? true : undefined}
                aria-describedby={confirmError ? "confirm-error" : undefined}
              />
            </CreateSurfaceField>
          </>
        )}
      </AuthCard>
    </AuthScreen>
  )
}
