"use client"

import { useState, useEffect, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { Sparkles } from "lucide-react"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

/**
 * First-run bootstrap page — shown on a Crewship instance that has
 * never had a user. Distinct from /signup because:
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
export default function BootstrapPage() {
  const router = useRouter()
  const [fullName, setFullName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [checking, setChecking] = useState(true)

  // Re-check setup status on mount so a user who tabs back here
  // after bootstrap completes lands on /login instead of seeing a
  // form that will 403.
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
        setError(data.error ?? "Bootstrap failed.")
        setLoading(false)
        return
      }
      // Bootstrap mints the session cookies as a side-effect of the
      // matching credentials sign-in we run next. The bootstrap
      // endpoint itself returns the user+workspace identifiers but
      // not a session, so chain a /api/auth/callback/credentials
      // call to put the cookies in place, then route to onboarding.
      const signin = await fetch("/api/auth/callback/credentials", {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({ email, password, redirect: "false" }).toString(),
      })
      if (!signin.ok) {
        // Cookie write failed but the admin row exists. Send the user
        // to /login so they can sign in manually — anything else
        // would hide a real auth problem.
        router.replace("/login?registered=true")
        return
      }
      router.replace("/onboarding")
    } catch {
      setError("Network error. Please try again.")
      setLoading(false)
    }
  }

  if (checking) {
    return <div className="min-h-screen bg-background" />
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-gradient-to-b from-background to-muted/30 p-4">
      <Card className="w-full max-w-md">
        <CardHeader className="text-center">
          <div className="flex justify-center mb-4">
            <CrewshipLogoTile />
          </div>
          <div className="flex justify-center mb-2">
            <span className="inline-flex items-center gap-1.5 rounded-full bg-primary/10 px-3 py-1 text-xs font-medium text-primary">
              <Sparkles className="h-3.5 w-3.5" /> First-run setup
            </span>
          </div>
          <CardTitle className="text-2xl">Welcome aboard</CardTitle>
          <CardDescription>
            This Crewship instance is fresh. Create the admin account to get started — you&apos;ll be
            the workspace owner and can invite teammates later.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={handleSubmit} className="space-y-4">
            {error && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
                {error}
              </div>
            )}
            <div className="space-y-2">
              <Label htmlFor="full_name">Your name</Label>
              <Input
                id="full_name"
                value={fullName}
                onChange={(e) => setFullName(e.target.value)}
                placeholder="Captain Reynolds"
                autoFocus
                required
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
              />
              <p className="text-[11px] text-muted-foreground">
                You can rotate this any time from <code className="font-mono">crewship admin reset-password</code>{" "}
                on the host.
              </p>
            </div>
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? "Setting up..." : "Create admin & continue"}
            </Button>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
