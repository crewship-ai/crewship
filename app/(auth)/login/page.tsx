"use client"

import { Suspense, useState, useEffect, type FormEvent } from "react"
import Link from "next/link"
import { useRouter, useSearchParams } from "next/navigation"
import { AuthSplitShell } from "@/components/branding/auth-split-shell"
import { useAuth } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { serverFetch } from "@/lib/server-base"

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
export function safeRedirectPath(raw: string | null): string {
  if (!raw) return "/"
  if (!raw.startsWith("/") || raw.startsWith("//")) return "/"
  // Mirror the server-side isSafeRedirect (internal/api/helpers.go):
  // reject the protocol-relative `/\` bypass and any backslash anywhere.
  // Browsers normalize "\" → "/", so "\\evil.com" or "/\evil.com" would
  // become protocol-relative URLs if this ever feeds window.location.
  if (raw.startsWith("/\\") || raw.includes("\\")) return "/"
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
  // Signup can't confirm that an account was created — the endpoint
  // answers the same way for an address that already has one, so it
  // doesn't leak who is registered. The banner has to stay as vague as
  // the API response is.
  const signupSubmitted = searchParams.get("signup") === "submitted"
  const expired = searchParams.get("reason") === "expired"
  const redirectTarget = safeRedirectPath(searchParams.get("redirect"))
  const { signIn } = useAuth()
  // First-run gate: on an empty Crewship install the visitor should
  // never see the login form — they should land on /bootstrap to
  // create the initial admin. `gateChecked` lets us render nothing
  // until /system/setup-status resolves so the form doesn't flash on
  // every page load.
  const [gateChecked, setGateChecked] = useState(false)
  const [signupAllowed, setSignupAllowed] = useState(true)

  useEffect(() => {
    let cancelled = false

     
    void serverFetch("/api/v1/system/setup-status")
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

    // The /auth/google/status probe went with the button. The endpoint
    // still answers false for older frontend builds, but this one has
    // nothing to render from it.

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
    <AuthSplitShell
      eyebrow="Self-hosted"
      headline="Command the whole fleet."
      blurb="Every agent in its own container, on hardware you own."
    >
      <div>
        <p className="font-mono text-[11px] uppercase tracking-[0.11em] text-muted-foreground">
          Welcome back
        </p>
        <h1 className="mt-3 text-balance text-[clamp(26px,2.4vw,32px)] font-extrabold leading-[1.12] tracking-[-0.028em]">
          Sign in to Crewship
        </h1>
        <p className="mt-2 max-w-[34ch] text-sm text-muted-foreground">
          Sign in to manage your AI workforce.
        </p>
        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
            {registered && (
              <div
                className="rounded-md border border-success/40 bg-success/10 p-3 text-sm text-success"
                role="status"
                aria-live="polite"
              >
                Account created! Please sign in.
              </div>
            )}
            {signupSubmitted && !registered && (
              <div
                className="rounded-md border border-success/40 bg-success/10 p-3 text-sm text-success"
                role="status"
                aria-live="polite"
              >
                Thanks! If that email address wasn&apos;t already registered,
                your account is ready — sign in below.
              </div>
            )}
            {expired && !error && (
              <div
                className="rounded-md border border-warn/40 bg-warn/10 p-3 text-sm text-warn"
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
            {/* "or continue with" + the Google button lived here. Google
                sign-in is switched off (2026-07-27) and its routes are no
                longer registered, so a divider promising an alternative and
                a button that cannot work are both worse than nothing. */}
            {signupAllowed && (
              <p className="text-center text-xs text-muted-foreground">
                Don&apos;t have an account?{" "}
                <a href="/signup" className="text-primary hover:underline">
                  Sign up
                </a>
              </p>
            )}
        </form>
      </div>
    </AuthSplitShell>
  )
}
