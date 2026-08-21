"use client"

import { useState, useEffect, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { motion, useReducedMotion } from "motion/react"
import { Sparkles, ArrowRight } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { AuthSplitShell } from "@/components/branding/auth-split-shell"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
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

const ease = [0.16, 1, 0.3, 1] as const

export default function BootstrapPage() {
  const router = useRouter()
  const reduce = useReducedMotion()
  const { refresh } = useAuth()
  const [fullName, setFullName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
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
    if (fullName.trim().length < 2) {
      setError("Name must be at least 2 characters.")
      return
    }
    if (password.length < 8) {
      setError("Password must be at least 8 characters.")
      return
    }
    if (password !== confirmPassword) {
      setError("Those passwords don't match.")
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
    return <div className="min-h-screen bg-background" />
  }

  return (
    <AuthSplitShell
      eyebrow="First run"
      headline="You're the first aboard."
      blurb="Nothing has been created yet. This account owns the workspace and invites the rest."
    >
      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, y: 14 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.55, ease }}
      >
        <p className="flex items-center gap-1.5 font-mono text-[11px] uppercase tracking-[0.11em] text-primary">
          <Sparkles className="h-3 w-3" /> Initial setup
        </p>
        <h1 className="mt-3 text-balance text-[clamp(24px,2.2vw,30px)] font-extrabold leading-[1.14] tracking-[-0.028em]">
          Create the administrator account
        </h1>
        <p className="mt-2 max-w-[44ch] text-sm text-muted-foreground">
          This is the first sign-in for this Crewship instance. The account you create will own the
          workspace and can invite additional members afterwards.
        </p>
        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
              {error && (
                <div
                  className="rounded-xl border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
                  role="alert"
                  aria-live="assertive"
                >
                  {error}
                </div>
              )}
              <div className="space-y-2">
                <Label htmlFor="full_name">Full name</Label>
                <Input
                  id="full_name"
                  value={fullName}
                  onChange={(e) => setFullName(e.target.value)}
                  placeholder="Alex Johnson"
                  autoFocus
                  required
                  className="h-11"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="email">Email</Label>
                <Input
                  id="email"
                  type="email"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                  placeholder="you@company.com"
                  required
                  className="h-11"
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="password">Password</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="At least 8 characters"
                  required
                  autoComplete="new-password"
                  className="h-11"
                />
              </div>
              {/* Confirmation, same as /signup. This is the one account on the
                  instance that owns the workspace, it is created before any
                  session exists, and a typo here is only discoverable at the
                  next sign-in — by which point the only way back in is a
                  password reset the fresh install may not be able to send. */}
              <div className="space-y-2">
                <Label htmlFor="confirm_password">Confirm password</Label>
                <Input
                  id="confirm_password"
                  type="password"
                  value={confirmPassword}
                  onChange={(e) => setConfirmPassword(e.target.value)}
                  placeholder="Repeat it"
                  required
                  autoComplete="new-password"
                  className="h-11"
                />
              </div>
              <Button type="submit" className="w-full h-11 text-sm font-semibold" disabled={loading}>
                {loading ? (
                  <>
                    <Spinner className="mr-2 h-4 w-4" />
                    Creating account…
                  </>
                ) : (
                  <>
                    Continue to workspace setup
                    <ArrowRight className="ml-2 h-4 w-4" />
                  </>
                )}
              </Button>
        </form>
      </motion.div>
    </AuthSplitShell>
  )
}
