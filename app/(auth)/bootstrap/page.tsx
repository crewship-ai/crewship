"use client"

import { useState, useEffect, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { Rocket } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { AUTH_BUTTON, AUTH_INPUT, AuthCard, AuthCardSkeleton, AuthPasswordInput, AuthScreen, AuthSteps } from "@/components/branding/auth-card"
import { CreateSurfaceField } from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { useAuth } from "@/hooks/use-auth"
import { serverFetch } from "@/lib/server-base"

/**
 * Single-form first-run bootstrap, open until the first admin exists.
 *
 * Four input fields (name + email + password + confirmation), one
 * submit, done. No
 * setup token, no placeholder credentials, no separate profile-setup
 * step afterwards. By default there is no timer: the server keeps
 * /api/v1/bootstrap open until the users table is non-empty — the
 * empty table is the gate (the GitLab/Grafana first-run pattern) — and
 * once an admin exists it refuses with 410. Deploy-race protection is
 * opt-in: setting CREWSHIP_BOOTSTRAP_WINDOW=<duration> (e.g. "5m")
 * arms a finite window after startup so an internet-reachable instance
 * nobody bootstrapped doesn't sit open to whichever scanner finds the
 * URL first; after it elapses the endpoint refuses until the server is
 * restarted. Headless / CI provisioning uses `crewship init` against
 * the same endpoint and is bound by the same gates.
 *
 * Flow:
 *   /login  → setup-status check finds needs_bootstrap=true → /bootstrap
 *   /bootstrap → submit form → POST /api/v1/bootstrap → session set
 *                inline + redirect to /onboarding wizard.
 *
 * If the user races back here on an already-initialised server (e.g.
 * a stale bookmark) we replace to /login.
 */

type BootstrapField = "full_name" | "password" | "confirm_password"

/** First run is one of four setup steps; the other three live in /onboarding. */
const SETUP_STEPS = ["Account", "Workspace", "Model", "Crew"] as const

export default function BootstrapPage() {
  const router = useRouter()
  const { refresh } = useAuth()
  const [fullName, setFullName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  // A wrong value sits under its field; `error` is what the server refused.
  const [fieldError, setFieldError] = useState<{ field: BootstrapField; message: string } | null>(null)
  const [loading, setLoading] = useState(false)
  const [checking, setChecking] = useState(true)

  useEffect(() => {
     
    serverFetch("/api/v1/system/setup-status")
      .then((r) => (r.ok ? r.json() : { needs_bootstrap: true }))
      .then((d) => {
        if (!d.needs_bootstrap) {
          router.replace("/login")
          return
        }
        setChecking(false)
      })
      .catch(() => setChecking(false))
  }, [router])

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setError(null)
    setFieldError(null)
    if (fullName.trim().length < 2) {
      setFieldError({ field: "full_name", message: "Name must be at least 2 characters." })
      return
    }
    if (password.length < 8) {
      setFieldError({ field: "password", message: "Password must be at least 8 characters." })
      return
    }
    if (password !== confirmPassword) {
      setFieldError({ field: "confirm_password", message: "Those passwords don't match." })
      return
    }
    setLoading(true)
    try {
       
      const res = await serverFetch("/api/v1/bootstrap", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ full_name: fullName, email, password }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error ?? `Bootstrap failed (HTTP ${res.status}).`)
        return
      }
      const data = await res.json().catch(() => ({}))
      if (data?.session_pending) {
        router.replace("/login?registered=true")
        return
      }
      await refresh()
      router.replace("/onboarding")
    } catch (e) {
      setError(
        e instanceof Error && e.message
          ? `Couldn't reach the server: ${e.message}. Check your connection and try again.`
          : "Couldn't reach the server. Check your connection and try again.",
      )
    } finally {
      setLoading(false)
    }
  }

  if (checking) {
    return (
      <AuthScreen panel="bootstrap">
        <AuthCardSkeleton />
      </AuthScreen>
    )
  }

  const errorFor = (field: BootstrapField) => (fieldError?.field === field ? fieldError.message : null)
  const invalid = (field: BootstrapField) =>
    errorFor(field) ? { "aria-invalid": true, "aria-describedby": `${field}-error` } : {}
  const clearFor = (field: BootstrapField) => {
    if (fieldError?.field === field) setFieldError(null)
  }

  return (
    <AuthScreen panel="bootstrap">
      <AuthCard
        icon={Rocket}
        title="Create the administrator account"
        description="This is the first sign-in for this Crewship instance. The account you create will own the workspace and can invite additional members afterwards."
        // The account is step one; /onboarding asks for the other three.
        steps={<AuthSteps steps={SETUP_STEPS} current={0} />}
        onSubmit={handleSubmit}
        refusal={error}
        actions={
          <Button type="submit" disabled={loading} className={AUTH_BUTTON}>
            {loading && <Spinner className="size-3.5" />}
            {loading ? "Creating account…" : "Continue"}
          </Button>
        }
      >
        <CreateSurfaceField label="Full name" htmlFor="full_name" error={errorFor("full_name")}>
          <Input
            id="full_name"
            autoComplete="name"
            value={fullName}
            onChange={(e) => {
              setFullName(e.target.value)
              clearFor("full_name")
            }}
            placeholder="Alex Johnson"
            autoFocus
            required
            className={AUTH_INPUT}
            {...invalid("full_name")}
          />
        </CreateSurfaceField>
        <CreateSurfaceField label="Email" htmlFor="email">
          <Input
            id="email"
            type="email"
            autoComplete="email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            placeholder="you@company.com"
            required
            className={AUTH_INPUT}
          />
        </CreateSurfaceField>
        <CreateSurfaceField label="Password" htmlFor="password" error={errorFor("password")}>
          <AuthPasswordInput
            id="password"
            value={password}
            onChange={(e) => {
              setPassword(e.target.value)
              clearFor("password")
            }}
            placeholder="At least 8 characters"
            required
            autoComplete="new-password"
            rule
            {...invalid("password")}
          />
        </CreateSurfaceField>
        {/* Confirmation, same as /signup. This is the one account on the
            instance that owns the workspace, it is created before any
            session exists, and a typo here is only discoverable at the
            next sign-in — by which point the only way back in is a
            password reset the fresh install may not be able to send. */}
        <CreateSurfaceField label="Confirm password" htmlFor="confirm_password" error={errorFor("confirm_password")}>
          <AuthPasswordInput
            id="confirm_password"
            value={confirmPassword}
            onChange={(e) => {
              setConfirmPassword(e.target.value)
              clearFor("confirm_password")
            }}
            placeholder="Repeat it"
            required
            autoComplete="new-password"
            {...invalid("confirm_password")}
          />
        </CreateSurfaceField>
      </AuthCard>
    </AuthScreen>
  )
}
