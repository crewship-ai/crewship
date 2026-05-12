"use client"

import { Suspense, useState, useEffect, type FormEvent } from "react"
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

  useEffect(() => {
    let cancelled = false

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
  }, [])

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

  return (
    <div className="flex min-h-screen items-center justify-center bg-background p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <CrewshipLogoTile />
          </div>
          <CardTitle className="text-xl">Welcome to Crewship</CardTitle>
          <CardDescription>Sign in to manage your AI workforce</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            {registered && (
              <p className="text-sm text-center text-emerald-600 dark:text-emerald-400">
                Account created! Please sign in.
              </p>
            )}
            {expired && !error && (
              <p className="text-sm text-center text-amber-600 dark:text-amber-400" role="status" aria-live="polite">
                Your session expired. Please sign in again.
              </p>
            )}
            {error && (
              <p className="text-sm text-destructive text-center">{error}</p>
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
              <Label htmlFor="password">Password</Label>
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
            <p className="text-center text-xs text-muted-foreground">
              Don&apos;t have an account?{" "}
              <a href="/signup" className="text-primary hover:underline">
                Sign up
              </a>
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
