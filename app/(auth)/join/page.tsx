"use client"

import { useEffect, useId, useState } from "react"
import { useRouter } from "next/navigation"
import { AlertTriangle, Link2Off, LogIn, UserPlus, X } from "lucide-react"
import { AUTH_BUTTON, AuthBanner, AuthCard, AuthLink, AuthScreen } from "@/components/branding/auth-card"
import { PageEmpty } from "@/components/layout/page-state"
import { Button } from "@/components/ui/button"
import { Spinner } from "@/components/ui/spinner"
import { refreshWorkspaceSettings } from "@/hooks/use-workspace"
import { serverFetch } from "@/lib/server-base"
import { captureInvitationProof, clearInvitationProof } from "@/lib/invitation-proof"

// Where Sign in sends the person, and where it brings them back. The proof
// itself stays in this tab's storage, never in a URL.
const SIGN_IN_HREF = `/login?redirect=${encodeURIComponent("/join")}`

export default function JoinPage() {
  const router = useRouter()
  const [token, setToken] = useState<string | null>(null)
  const [guestConfirmation, setGuestConfirmation] = useState(false)
  const [guestAcknowledged, setGuestAcknowledged] = useState(false)
  const [failure, setFailure] = useState("This invitation is unavailable. Check the account or ask for a new invitation.")
  const [status, setStatus] = useState<"idle" | "saving" | "signin" | "error">("idle")
  const ackId = useId()
  useEffect(() => {
    setToken(captureInvitationProof())
  }, [])

  async function accept() {
    if (!token || status === "saving") return
    setStatus("saving")
    try {
      const response = await serverFetch("/api/v1/auth/invitation/accept", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token, ...(guestConfirmation ? { confirm_account_restriction: true } : {}) }),
      })
      if (response.status === 401) {
        setStatus("signin")
        return
      }
      if (!response.ok) {
        const body = await response.json().catch(() => null)
        if (body?.code === "guest_confirmation_required") {
          setGuestConfirmation(true)
          setStatus("idle")
          return
        }
        if (typeof body?.error === "string") setFailure(body.error)
        setStatus("error")
        return
      }
      clearInvitationProof()
      await refreshWorkspaceSettings()
      router.replace("/")
    } catch {
      setStatus("error")
    }
  }

  if (token === "") {
    return (
      <AuthScreen panel="join">
        <AuthCard icon={UserPlus} title="Accept the invitation">
          <PageEmpty
            icon={Link2Off}
            title="This invitation link is incomplete"
            description="Ask for the original invitation link."
            className="p-0"
          />
        </AuthCard>
      </AuthScreen>
    )
  }

  if (status === "signin") {
    return (
      <AuthScreen panel="join">
        <AuthCard
          icon={UserPlus}
          title="Accept the invitation"
          description="Sign in with the invited address, then accept."
          banner={<AuthBanner tone="warn" icon={LogIn}>Sign in first. You&apos;ll come back here.</AuthBanner>}
          actions={
            <>
              <Button asChild variant="outline" className={AUTH_BUTTON}>
                <a href="/signup">Create an account</a>
              </Button>
              <Button asChild className={AUTH_BUTTON}>
                <a href={SIGN_IN_HREF}>Sign in</a>
              </Button>
            </>
          }
        />
      </AuthScreen>
    )
  }

  const saving = status === "saving"
  const primaryDisabled = !token || saving || (guestConfirmation && !guestAcknowledged)

  return (
    <AuthScreen panel="join">
      <AuthCard
        icon={UserPlus}
        title={guestConfirmation ? "Accepting makes this account a Guest" : "Accept the invitation"}
        description={
          guestConfirmation
            ? "Read what changes for the whole account before you accept."
            : "Sign in with the invited address, then accept."
        }
        refusal={status === "error" ? failure : null}
        links={
          token ? (
            <>
              <AuthLink href={SIGN_IN_HREF}>Sign in</AuthLink>
              <AuthLink href="/signup">Create an account</AuthLink>
            </>
          ) : undefined
        }
        actions={
          <Button type="button" disabled={primaryDisabled} onClick={accept} className={AUTH_BUTTON}>
            {saving && <Spinner className="size-3.5" />}
            {saving ? "Accepting…" : guestConfirmation ? "Accept Guest restrictions" : "Accept invitation"}
          </Button>
        }
      >
        {guestConfirmation && (
          <>
            {/* The consequences, in the kit's confirm shape: what is lost,
                what to watch. Same facts as the server's Guest rule. */}
            <div role="status" aria-live="polite">
              <p className="sr-only">This invitation makes you a Guest.</p>
              <ul className="flex flex-col gap-1.5 text-body text-foreground">
                <li className="flex items-start gap-2">
                  <X aria-hidden className="mt-0.5 size-3.5 shrink-0 text-destructive" />
                  <span>Accepting restricts your entire account across all workspaces to explicitly shared resources.</span>
                </li>
                <li className="flex items-start gap-2">
                  <AlertTriangle aria-hidden className="mt-0.5 size-3.5 shrink-0 text-warn" />
                  <span>Your other workspaces will also follow the Guest restriction.</span>
                </li>
                <li className="flex items-start gap-2">
                  <AlertTriangle aria-hidden className="mt-0.5 size-3.5 shrink-0 text-warn" />
                  <span>Accounts that own or administer any workspace cannot accept; use a separate account for Guest access.</span>
                </li>
              </ul>
            </div>
            <label htmlFor={ackId} className="flex items-center gap-2 text-body coarse:min-h-12">
              {/* Native: touch-form grows every <button> to 44 px under a coarse
                  pointer, and the kit Checkbox is one. The label row carries
                  the touch target instead. */}
              <input
                id={ackId}
                type="checkbox"
                checked={guestAcknowledged}
                onChange={(e) => setGuestAcknowledged(e.target.checked)}
                className="size-4 shrink-0 accent-primary"
              />
              I understand this applies to the whole account
            </label>
          </>
        )}
      </AuthCard>
    </AuthScreen>
  )
}
