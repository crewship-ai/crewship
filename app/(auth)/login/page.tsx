"use client"

import { Suspense, useState, useEffect, type FormEvent } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { useAuth } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export default function LoginPage() {
  return (
    <Suspense>
      <LoginForm />
    </Suspense>
  )
}

/** Whitelist for the post-login redirect target. Only allow same-origin
 *  relative paths — block protocol-relative (`//evil`), absolute URLs,
 *  and `/login` itself (which would just bounce back here). */
function safeRedirectPath(raw: string | null): string {
  if (!raw) return "/"
  if (!raw.startsWith("/") || raw.startsWith("//")) return "/"
  // Block every shape that would bounce the user back to /login —
  // bare /login, /login?…, /login/…, AND /login#hash. The fragment
  // form was the missing branch: a fragment-only redirect would
  // otherwise satisfy the !startsWith("/login?") test.
  if (
    raw === "/login" ||
    raw.startsWith("/login?") ||
    raw.startsWith("/login/") ||
    raw.startsWith("/login#")
  ) {
    return "/"
  }
  return raw
}

function LoginForm() {
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const router = useRouter()
  const searchParams = useSearchParams()
  const registered = searchParams.get("registered") === "true"
  const expired = searchParams.get("reason") === "expired"
  const redirectTarget = safeRedirectPath(searchParams.get("redirect"))
  const { signIn } = useAuth()
  // Track the status discovery as its own state so we can distinguish
  // "configured and disabled" from "network hiccup" — the previous boolean
  // collapsed both into "disabled" and surfaced the "set GOOGLE_CLIENT_ID"
  // copy during transient outages.
  const [googleStatus, setGoogleStatus] = useState<"loading" | "enabled" | "disabled" | "error">("loading")
  // First-run gate: on an empty Crewship install the visitor should
  // never see the login form — they should land on /bootstrap to
  // create the initial admin. `gateChecked` lets us render nothing
  // until /system/setup-status resolves so the form doesn't flash on
  // every page load.
  const [gateChecked, setGateChecked] = useState(false)
  const [signupAllowed, setSignupAllowed] = useState(true)

  useEffect(() => {
    let cancelled = false

    void fetch("/api/v1/system/setup-status")
      .then(async (r) => (r.ok ? r.json() : { needs_bootstrap: false, allow_signup: true }))
      .then((data: { needs_bootstrap?: boolean; allow_signup?: boolean }) => {
        if (cancelled) return
        if (data.needs_bootstrap) {
          // Preserve any redirect target through the bootstrap flow
          // so a session-expired user who clicked a deep link still
          // lands where they meant to go after onboarding finishes.
          const next = redirectTarget && redirectTarget !== "/" ? `?next=${encodeURIComponent(redirectTarget)}` : ""
          router.replace(`/bootstrap${next}`)
          return
        }
        setSignupAllowed(data.allow_signup !== false)
        setGateChecked(true)
      })
      .catch(() => {
        if (!cancelled) setGateChecked(true)
      })

    void fetch("/api/v1/auth/google/status")
      .then(async (r) => {
        if (!r.ok) throw new Error("google status check failed")
        const data: { enabled?: boolean } = await r.json()
        if (!cancelled) {
          setGoogleStatus(data.enabled === true ? "enabled" : "disabled")
        }
      })
      .catch(() => {
        if (!cancelled) setGoogleStatus("error")
      })

    return () => {
      cancelled = true
    }
  }, [router, redirectTarget])

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setError(null)
    setLoading(true)

    const result = await signIn(email, password)

    setLoading(false)

    if (!result.ok) {
      setError(result.error ?? "Invalid email or password")
      return
    }

    router.push(redirectTarget)
  }

  // Block the entire form until the first-run gate resolves —
  // otherwise a fresh install would briefly flash the login UI before
  // redirecting to /bootstrap.
  if (!gateChecked) {
    return <div className="min-h-screen bg-gradient-to-b from-background to-muted/30" />
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-gradient-to-b from-background to-muted/30 p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <CrewshipLogoTile />
          </div>
          <CardTitle className="text-2xl">Welcome to Crewship</CardTitle>
          <CardDescription>Sign in to manage your AI workforce</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            {registered && (
              <div
                className="rounded-md border border-emerald-200/40 bg-emerald-500/10 p-3 text-sm text-emerald-700 dark:text-emerald-400"
                role="status"
                aria-live="polite"
              >
                Account created! Please sign in.
              </div>
            )}
            {expired && !error && (
              <div
                className="rounded-md border border-amber-200/40 bg-amber-500/10 p-3 text-sm text-amber-700 dark:text-amber-400"
                role="status"
                aria-live="polite"
              >
                Your session expired. Please sign in again.
              </div>
            )}
            {error && (
              <div
                className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
                role="alert"
                aria-live="assertive"
              >
                {error}
              </div>
            )}
            <div className="space-y-2">
              <Label htmlFor="email">Email</Label>
              <Input
                id="email"
                type="email"
                placeholder="you@company.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                required
              />
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label htmlFor="password">Password</Label>
                <Link
                  href="/forgot-password"
                  className="text-xs text-muted-foreground hover:text-foreground hover:underline"
                >
                  Forgot password?
                </Link>
              </div>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? "Signing in..." : "Sign In"}
            </Button>
            <div className="relative">
              <div className="absolute inset-0 flex items-center">
                <span className="w-full border-t" />
              </div>
              <div className="relative flex justify-center text-xs uppercase">
                <span className="bg-card px-2 text-muted-foreground">or continue with</span>
              </div>
            </div>
            <Button
              type="button"
              variant="outline"
              className="w-full"
              disabled={googleStatus !== "enabled"}
              title={
                googleStatus === "enabled"
                  ? "Sign in with your Google account"
                  : googleStatus === "disabled"
                    ? "Google sign-in not configured"
                    : googleStatus === "loading"
                      ? "Checking Google sign-in availability…"
                      : "Google sign-in is temporarily unavailable"
              }
              onClick={() => {
                // Carry the sanitized redirect through Google sign-in so a
                // session-expired user lands back on the page they were on
                // (matching the credentials flow). CodeRabbit flagged the
                // missing case on PR #233.
                const target = redirectTarget && redirectTarget !== "/"
                  ? `/api/v1/auth/google/redirect?redirect=${encodeURIComponent(redirectTarget)}`
                  : "/api/v1/auth/google/redirect"
                window.location.href = target
              }}
            >
              Sign in with Google
            </Button>
            {signupAllowed && (
              <p className="text-center text-xs text-muted-foreground">
                Don&apos;t have an account?{" "}
                <a href="/signup" className="text-primary hover:underline">
                  Sign up
                </a>
              </p>
            )}
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
