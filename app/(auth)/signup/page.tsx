"use client"

import { useState, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { AuthSplitShell } from "@/components/branding/auth-split-shell"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { serverFetch } from "@/lib/server-base"

export default function SignupPage() {
  const [fullName, setFullName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const router = useRouter()

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setError(null)

    if (password !== confirmPassword) {
      setError("Passwords do not match")
      return
    }

    setLoading(true)

     
    const res = await serverFetch("/api/v1/auth/signup", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ full_name: fullName, email, password }),
    })

    setLoading(false)

    if (!res.ok) {
      const data = await res.json()
      if (data.error?.fieldErrors) {
        const messages = Object.values(data.error.fieldErrors).flat()
        setError(messages.join(". ") || "Invalid input")
      } else {
        setError(typeof data.error === "string" ? data.error : "Something went wrong")
      }
      return
    }

    // The API answers 202 with the same generic body whether or not the
    // address already had an account, and hands out no session — telling
    // this form "already registered" (the old 409) or logging it straight
    // in would give away who has an account here. So: no auto-login, and
    // a banner on /login that promises no more than the API did.
    router.push("/login?signup=submitted")
  }

  return (
    <AuthSplitShell
      eyebrow="Self-hosted"
      headline="Bring your own hardware."
      blurb="Every agent runs in its own container, on machines you own."
    >
      <div>
        <p className="font-mono text-[11px] uppercase tracking-[0.11em] text-muted-foreground">
          Join
        </p>
        <h1 className="mt-3 text-balance text-[clamp(24px,2.2vw,30px)] font-extrabold leading-[1.14] tracking-[-0.028em]">
          Create your account
        </h1>
        <p className="mt-2 max-w-[44ch] text-sm text-muted-foreground">
          One account, then pick the crew you want running.
        </p>
        <form onSubmit={handleSubmit} className="mt-6 space-y-4">
            {error && (
              <p className="text-sm text-destructive text-center">{error}</p>
            )}
            <div className="space-y-2">
              <Label htmlFor="full_name">Full Name</Label>
              <Input
                id="full_name"
                type="text"
                placeholder="John Doe"
                value={fullName}
                onChange={(e) => setFullName(e.target.value)}
                required
                minLength={2}
              />
            </div>
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
                minLength={8}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="confirm_password">Confirm Password</Label>
              <Input
                id="confirm_password"
                type="password"
                value={confirmPassword}
                onChange={(e) => setConfirmPassword(e.target.value)}
                required
                minLength={8}
              />
            </div>
            <Button type="submit" className="w-full" disabled={loading}>
              {loading ? "Creating account..." : "Sign Up"}
            </Button>
            <p className="text-center text-xs text-muted-foreground">
              Already have an account?{" "}
              <a href="/login" className="text-primary hover:underline">
                Sign in
              </a>
            </p>
        </form>
      </div>
    </AuthSplitShell>
  )
}
