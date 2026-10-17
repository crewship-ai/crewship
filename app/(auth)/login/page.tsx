"use client"

import { Suspense, useState, useEffect, type FormEvent } from "react"
import { useRouter, useSearchParams } from "next/navigation"
import { User } from "lucide-react"
import {
  AUTH_BUTTON,
  AUTH_INPUT,
  AuthBanner,
  AuthCard,
  AuthCardSkeleton,
  AuthLink,
  AuthScreen,
} from "@/components/branding/auth-card"
import { CreateSurfaceField } from "@/components/layout/create-surface"
import { useAuth } from "@/hooks/use-auth"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Spinner } from "@/components/ui/spinner"
import { safeRedirectPath } from "./safe-redirect"
import { returnPageName } from "./return-to"
import { serverFetch } from "@/lib/server-base"

export default function LoginPage() {
  return (
    <Suspense>
      <LoginForm />
    </Suspense>
  )
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
  // A sign-in refused on the server side (e.g. Google for a suspended
  // account) lands here; like a wrong password, it does not say why.
  const refused = searchParams.get("error") === "signin"
  const redirectTarget = safeRedirectPath(searchParams.get("redirect"))
  // Display only: where the person lands is still redirectTarget.
  const returnTo = returnPageName(searchParams.get("redirect"))
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
  // redirecting to /bootstrap. The skeleton stays empty for the first second.
  if (!gateChecked) {
    return (
      <AuthScreen>
        <AuthCardSkeleton />
      </AuthScreen>
    )
  }

  // One rank per kind of feedback: a redirect state is a banner in the card,
  // anything the server refused is the band above the footer.
  const banner = registered ? (
    <AuthBanner tone="ok">Administrator account created. Sign in to continue setup.</AuthBanner>
  ) : signupSubmitted ? (
    // As vague as the API: the same sentence whether or not the address
    // already had an account.
    <AuthBanner tone="ok">If that address was not registered yet, the account is ready. Sign in below.</AuthBanner>
  ) : expired && !error ? (
    <AuthBanner tone="warn">Your session expired. Please sign in again.</AuthBanner>
  ) : null
  const refusal = error ?? (refused ? "We could not sign you in. Try again, or ask your administrator." : null)

  return (
    <AuthScreen>
      <AuthCard
        icon={User}
        title="Sign in to Crewship"
        description={
          returnTo ? (
            <>
              Sign in to manage your AI workforce.{" "}
              <span data-slot="auth-return-to">You&apos;ll return to {returnTo}.</span>
            </>
          ) : (
            "Sign in to manage your AI workforce."
          )
        }
        onSubmit={handleSubmit}
        banner={banner}
        refusal={refusal}
        links={
          // "or continue with" + the Google button lived here. Google sign-in
          // is switched off (2026-07-27) and its routes are no longer
          // registered, so a divider promising an alternative and a button
          // that cannot work are both worse than nothing.
          signupAllowed ? <AuthLink href="/signup">Create an account</AuthLink> : undefined
        }
        actions={
          <Button type="submit" disabled={loading} className={AUTH_BUTTON}>
            {loading && <Spinner className="size-3.5" />}
            {loading ? "Signing in…" : "Sign in"}
          </Button>
        }
      >
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
        {/* The link sits in the label row but after the field in the DOM, so
            Tab goes Email → Password → Forgot password → Sign in. Under a
            coarse pointer it is 44 px tall, so it drops below the field
            instead of overlapping it. */}
        <div className="relative flex flex-col">
          <CreateSurfaceField label="Password" htmlFor="password">
            <Input
              id="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
              className={AUTH_INPUT}
            />
          </CreateSurfaceField>
          <AuthLink href="/forgot-password" className="absolute top-0 right-0 text-label leading-none coarse:static coarse:self-end">
            Forgot password?
          </AuthLink>
        </div>
      </AuthCard>
    </AuthScreen>
  )
}
