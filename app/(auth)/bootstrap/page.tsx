"use client"

import { useState, useEffect, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { motion, useReducedMotion } from "motion/react"
import { Sparkles, ArrowRight } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/hooks/use-auth"

/**
 * Single-form first-run bootstrap with a time-bounded window.
 *
 * Three input fields (name + email + password), one submit, done. No
 * setup token, no placeholder credentials, no separate profile-setup
 * step afterwards. Deploy-race protection is a fixed-duration
 * bootstrap window enforced server-side (default 5 minutes): the
 * /api/v1/bootstrap endpoint accepts requests for that long after
 * `crewship start`, then refuses with 410 until the server is
 * restarted. Headless / CI provisioning uses `crewship init` against
 * the same endpoint and is bound by the same window.
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
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [checking, setChecking] = useState(true)

  useEffect(() => {
    fetch("/api/v1/system/setup-status")
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
    setLoading(true)
    try {
      const res = await fetch("/api/v1/bootstrap", {
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
    <div className="relative flex min-h-screen items-center justify-center bg-background p-4 overflow-hidden">
      <div className="pointer-events-none absolute inset-x-0 top-0 h-[420px] bg-[radial-gradient(ellipse_70%_50%_at_50%_0%,rgba(30,123,254,0.14),transparent_60%)]" />

      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, y: 14 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.55, ease }}
        className="relative w-full max-w-md"
      >
        <Card className="border-border/60 bg-card/95 backdrop-blur-sm rounded-[20px] shadow-2xl shadow-primary/10">
          <CardHeader className="text-center pb-2">
            <div className="flex justify-center mb-4">
              <CrewshipLogoTile size="h-14 w-14" iconSize="h-7 w-7" rounded="rounded-2xl" />
            </div>
            <div className="flex justify-center mb-3">
              <span className="inline-flex items-center gap-1.5 rounded-full bg-primary/10 border border-primary/30 px-3 py-1 text-[11px] font-medium text-primary uppercase tracking-[0.12em]">
                <Sparkles className="h-3 w-3" /> Initial setup
              </span>
            </div>
            <CardTitle className="text-2xl tracking-tight">Create administrator account</CardTitle>
            <CardDescription className="mt-2 text-balance">
              This is the first sign-in for this Crewship instance. The account you create will own the
              workspace and can invite additional members afterwards.
            </CardDescription>
          </CardHeader>
          <CardContent className="pt-4">
            <form onSubmit={handleSubmit} className="space-y-4">
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
          </CardContent>
        </Card>
      </motion.div>
    </div>
  )
}
