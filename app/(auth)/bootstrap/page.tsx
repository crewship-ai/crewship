"use client"

import { useState, useEffect, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { motion, useReducedMotion } from "motion/react"
import { Sparkles, ArrowRight, Loader2 } from "lucide-react"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { useAuth } from "@/hooks/use-auth"

/**
 * First-run bootstrap — shown on a Crewship instance that has never
 * had a user. Visual language matches /onboarding so the two screens
 * feel like consecutive frames of the same flow: gradient logo tile,
 * Apple-tight typography, motion/react staggered reveals, hero glow.
 *
 * Distinct from /signup because:
 *   - copy frames it as "set up your Crewship instance" rather than
 *     "create an account on someone else's server"
 *   - hits POST /api/v1/bootstrap (only works while users table is
 *     empty) instead of /api/v1/auth/signup
 *   - guards against being shown after bootstrap completes — if a
 *     user races back to this URL on an initialised server we
 *     redirect to /login.
 *
 * The /login page checks /system/setup-status on mount and routes
 * visitors here automatically; this page is also linkable
 * directly for operators following install docs.
 */

const ease = [0.16, 1, 0.3, 1] as const

export default function BootstrapPage() {
  const router = useRouter()
  const reduce = useReducedMotion()
  // refresh() asks AuthProvider to re-fetch /api/auth/session. The
  // provider only fetches once on mount; after bootstrap writes
  // session cookies inline we need to nudge it so the (onboarding)
  // layout's useSession sees `authenticated` instead of the stale
  // `unauthenticated` from page load — without this nudge the
  // layout fires its unauth redirect and the freshly-bootstrapped
  // user lands back on /login.
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
      // Bootstrap sets session cookies inline now (since 2026-05-13)
      // so there's no follow-up auth chain to await — the cookie
      // jar is already populated by the time this response lands.
      // If the server couldn't establish the session for any reason
      // it returns session_pending=true; we fall through to /login
      // so the user can sign in with the password they just typed.
      const data = await res.json().catch(() => ({}))
      if (data?.session_pending) {
        router.replace("/login?registered=true")
        return
      }
      // Pull the new session into AuthContext BEFORE navigating so the
      // (onboarding) layout's useSession sees authenticated state.
      // Without this the layout's unauth-redirect fires before the
      // re-fetch can update the context.
      await refresh()
      router.replace("/onboarding")
    } catch (e) {
      setError(
        e instanceof Error && e.message
          ? `Couldn't reach the server: ${e.message}. Check your connection and try again.`
          : "Couldn't reach the server. Check your connection and try again.",
      )
    } finally {
      // finally so the form re-enables on every exit path — an
      // interrupted navigation (back button mid-redirect, browser
      // throttling, etc.) used to leave the submit button stuck
      // disabled forever.
      setLoading(false)
    }
  }

  if (checking) {
    return <div className="min-h-screen bg-background" />
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-background p-4 overflow-hidden">
      {/* Hero glow — same radial pattern as onboarding so the two
          screens feel like a continuous flow. */}
      <div className="pointer-events-none absolute inset-x-0 top-0 h-[420px] bg-[radial-gradient(ellipse_70%_50%_at_50%_0%,rgba(30,123,254,0.14),transparent_60%)]" />

      <motion.div
        initial={reduce ? { opacity: 0 } : { opacity: 0, y: 14 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.55, ease }}
        className="relative w-full max-w-md"
      >
        <Card className="border-border/60 bg-card/95 backdrop-blur-sm rounded-[20px] shadow-2xl shadow-primary/10">
          <CardHeader className="text-center pb-2">
            <motion.div
              initial={reduce ? { opacity: 0 } : { opacity: 0, scale: 0.85 }}
              animate={{ opacity: 1, scale: 1 }}
              transition={{ duration: 0.5, ease, delay: 0.1 }}
              className="flex justify-center mb-4"
            >
              <CrewshipLogoTile size="h-14 w-14" iconSize="h-7 w-7" rounded="rounded-2xl" />
            </motion.div>
            <motion.div
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.4, ease, delay: 0.2 }}
              className="flex justify-center mb-3"
            >
              <span className="inline-flex items-center gap-1.5 rounded-full bg-primary/10 border border-primary/30 px-3 py-1 text-[11px] font-medium text-primary uppercase tracking-[0.12em]">
                <Sparkles className="h-3 w-3" /> Initial setup
              </span>
            </motion.div>
            <motion.div
              initial={{ opacity: 0, y: 6 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, ease, delay: 0.25 }}
            >
              <CardTitle className="text-2xl tracking-tight">Create administrator account</CardTitle>
              <CardDescription className="mt-2 text-balance">
                This is the first sign-in for this Crewship instance. The account you create will own the
                workspace and can invite additional members afterwards.
              </CardDescription>
            </motion.div>
          </CardHeader>
          <CardContent className="pt-4">
            <motion.form
              onSubmit={handleSubmit}
              initial="hidden"
              animate="show"
              variants={{
                hidden: {},
                show: { transition: { staggerChildren: 0.06, delayChildren: 0.32 } },
              }}
              className="space-y-4"
            >
              {error && (
                <motion.div
                  initial={{ opacity: 0, y: 6 }}
                  animate={{ opacity: 1, y: 0 }}
                  className="rounded-xl border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
                  role="alert"
                  aria-live="assertive"
                >
                  {error}
                </motion.div>
              )}
              <motion.div
                variants={{ hidden: { opacity: 0, y: 6 }, show: { opacity: 1, y: 0 } }}
                transition={{ duration: 0.35, ease }}
                className="space-y-2"
              >
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
              </motion.div>
              <motion.div
                variants={{ hidden: { opacity: 0, y: 6 }, show: { opacity: 1, y: 0 } }}
                transition={{ duration: 0.35, ease }}
                className="space-y-2"
              >
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
              </motion.div>
              <motion.div
                variants={{ hidden: { opacity: 0, y: 6 }, show: { opacity: 1, y: 0 } }}
                transition={{ duration: 0.35, ease }}
                className="space-y-2"
              >
                <Label htmlFor="password">Password</Label>
                <Input
                  id="password"
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  placeholder="At least 8 characters"
                  required
                  className="h-11"
                />
                <p className="text-[11px] text-muted-foreground">
                  Can be reset later via{" "}
                  <code className="font-mono text-foreground/80 rounded bg-muted/60 px-1 py-0.5">
                    crewship admin reset-password
                  </code>{" "}
                  on the host.
                </p>
              </motion.div>
              <motion.div
                variants={{ hidden: { opacity: 0, y: 6 }, show: { opacity: 1, y: 0 } }}
                transition={{ duration: 0.35, ease }}
              >
                <Button type="submit" className="w-full h-11 text-sm font-semibold" disabled={loading}>
                  {loading ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Creating account…
                    </>
                  ) : (
                    <>
                      Continue to workspace setup
                      <ArrowRight className="ml-2 h-4 w-4" />
                    </>
                  )}
                </Button>
              </motion.div>
            </motion.form>
          </CardContent>
        </Card>
      </motion.div>
    </div>
  )
}
