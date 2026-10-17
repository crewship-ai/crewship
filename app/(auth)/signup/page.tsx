"use client"

import { useEffect, useState, type FormEvent } from "react"
import { useRouter } from "next/navigation"
import { Info, UserPlus } from "lucide-react"
import { AUTH_BUTTON, AUTH_INPUT, AuthCard, AuthLink, AuthScreen } from "@/components/branding/auth-card"
import { CreateSurfaceField, CreateSurfaceNotice } from "@/components/layout/create-surface"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { serverFetch } from "@/lib/server-base"
import { captureInvitationProof } from "@/lib/invitation-proof"

export default function SignupPage() {
  const [fullName, setFullName] = useState("")
  const [email, setEmail] = useState("")
  const [password, setPassword] = useState("")
  const [confirmPassword, setConfirmPassword] = useState("")
  const [error, setError] = useState<string | null>(null)
  // A wrong value belongs under its field; `error` is what the server refused.
  const [confirmError, setConfirmError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const router = useRouter()
 const [invitationToken,setInvitationToken]=useState("")
 useEffect(()=>{setInvitationToken(captureInvitationProof())},[])
 const joinPath = invitationToken ? "/join" : ""
 const loginPath = joinPath ? `/login?redirect=${encodeURIComponent(joinPath)}` : "/login"

  async function handleSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault()
    setError(null)
    setConfirmError(null)

    if (password !== confirmPassword) {
      setConfirmError("Passwords do not match")
      return
    }

    setLoading(true)
    try {
      const res = await serverFetch("/api/v1/auth/signup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ full_name: fullName, email, password, ...(invitationToken ? { invitation_token:invitationToken } : {}) }),
      })

      if (!res.ok) {
        const data = await res.json().catch(() => null)
        if (data?.error?.fieldErrors) {
          const messages = Object.values(data.error.fieldErrors).flat()
          setError(messages.join(". ") || "Invalid input")
        } else {
          setError(typeof data?.error === "string" ? data.error : "Something went wrong")
        }
        return
      }

      // The API answers 202 with the same generic body whether or not the
      // address already had an account, and hands out no session — telling
      // this form "already registered" (the old 409) or logging it straight
      // in would give away who has an account here. So: no auto-login, and
      // a banner on /login that promises no more than the API did.
      router.push(joinPath ? `/login?signup=submitted&redirect=${encodeURIComponent(joinPath)}` : "/login?signup=submitted")
    } catch {
      setError("Network error. Please try again.")
    } finally {
      setLoading(false)
    }
  }

  return (
    <AuthScreen>
      <AuthCard
        icon={UserPlus}
        title="Create your account"
        description="One account, then pick the crew you want running."
        onSubmit={handleSubmit}
        refusal={error}
        banner={
          invitationToken ? (
            <CreateSurfaceNotice tone="info" icon={Info}>
              You&apos;re accepting an invitation. Use the address it was sent to.
            </CreateSurfaceNotice>
          ) : undefined
        }
        links={
          <span className="text-muted-foreground">
            Already have an account? <AuthLink href={loginPath}>Sign in</AuthLink>
          </span>
        }
        actions={
          <Button type="submit" disabled={loading} className={AUTH_BUTTON}>
            {loading && <Spinner className="size-3.5" />}
            {loading ? "Creating account…" : "Create account"}
          </Button>
        }
      >
        <CreateSurfaceField label="Full name" htmlFor="full_name">
          <Input
            id="full_name"
            type="text"
            autoComplete="name"
            placeholder="John Doe"
            value={fullName}
            onChange={(e) => setFullName(e.target.value)}
            required
            minLength={2}
            className={AUTH_INPUT}
          />
        </CreateSurfaceField>
        <CreateSurfaceField label="Email" htmlFor="email">
          <Input
            id="email"
            type="email"
            autoComplete="email"
            placeholder="you@company.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            required
            className={AUTH_INPUT}
          />
        </CreateSurfaceField>
        <CreateSurfaceField label="Password" htmlFor="password" hint="At least 8 characters.">
          <Input
            id="password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            minLength={8}
            className={AUTH_INPUT}
          />
        </CreateSurfaceField>
        <CreateSurfaceField label="Confirm password" htmlFor="confirm_password" error={confirmError}>
          <Input
            id="confirm_password"
            type="password"
            autoComplete="new-password"
            value={confirmPassword}
            onChange={(e) => {
              setConfirmPassword(e.target.value)
              setConfirmError(null)
            }}
            required
            minLength={8}
            aria-invalid={confirmError ? true : undefined}
            aria-describedby={confirmError ? "confirm_password-error" : undefined}
            className={AUTH_INPUT}
          />
        </CreateSurfaceField>
      </AuthCard>
    </AuthScreen>
  )
}
