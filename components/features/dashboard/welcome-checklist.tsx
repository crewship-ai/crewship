"use client"

import { useEffect, useState } from "react"
import Link from "next/link"
import { CheckCircle2, Circle, X, Sparkles, ArrowRight, Terminal, Stethoscope } from "lucide-react"
import { Card, CardContent } from "@/components/ui/card"
import { Button } from "@/components/ui/button"

/**
 * localStorage flag set on successful onboarding completion. Presence
 * means "show the welcome checklist on the next dashboard mount."
 * Cleared on first explicit dismiss or by the user opting out via
 * "Got it" — never auto-cleared, so a refresh inside the dashboard
 * doesn't surprise-hide the checklist before they actually saw it.
 */
export const WELCOME_FLAG = "crewship.justOnboarded"

/**
 * Persistent flag set when the user dismisses the checklist. Future
 * sessions read this and skip rendering even if WELCOME_FLAG somehow
 * gets re-set (e.g. running onboarding twice). The two-flag design
 * intentionally separates "just landed" from "explicitly opted out."
 */
const DISMISSED_FLAG = "crewship.welcomeChecklistDismissed"

interface ChecklistItem {
  id: string
  done: boolean
  title: string
  description: string
  icon: React.ComponentType<{ className?: string }>
  cta?: { label: string; href: string } | { label: string; copy: string }
}

/**
 * Post-onboarding welcome banner. Renders only on first dashboard
 * visit after onboarding completes, and only until the user dismisses
 * it. Anonymous beta testers shouldn't have to read README.md to find
 * `crewship doctor` and the CLI pairing flow — the checklist surfaces
 * the three things they'll actually do in their first 10 minutes.
 *
 * Empty dashboard without this banner = "blank screen, what now?";
 * with it = a clear three-step path that maps to the README's "Beta
 * status & limitations" guidance.
 */
export function WelcomeChecklist({ firstAgentId }: { firstAgentId?: string | null }) {
  // Render-gate state — null while we're consulting localStorage so we
  // don't briefly flash the banner on a session that already dismissed.
  const [visible, setVisible] = useState<boolean | null>(null)

  useEffect(() => {
    if (typeof window === "undefined") {
      setVisible(false)
      return
    }
    try {
      const dismissed = window.localStorage.getItem(DISMISSED_FLAG) === "1"
      const justOnboarded = window.localStorage.getItem(WELCOME_FLAG) === "1"
      setVisible(justOnboarded && !dismissed)
    } catch {
      // localStorage may throw in private-browsing modes — fail closed
      // (no banner) rather than break the whole dashboard render.
      setVisible(false)
    }
  }, [])

  function dismiss() {
    try {
      window.localStorage.setItem(DISMISSED_FLAG, "1")
      window.localStorage.removeItem(WELCOME_FLAG)
    } catch {
      // ignore — banner will re-appear next mount if the write failed,
      // which is the right behaviour (user hasn't actually opted out).
    }
    setVisible(false)
  }

  if (!visible) return null

  const items: ChecklistItem[] = [
    {
      id: "first-chat",
      done: false,
      title: "Talk to your first agent",
      description: "Open the chat surface for the agent the wizard just created.",
      icon: Sparkles,
      cta: firstAgentId
        ? { label: "Open chat", href: `/crews/agents/${firstAgentId}/chat` }
        : { label: "Browse agents", href: "/crews" },
    },
    {
      id: "doctor",
      done: false,
      title: "Run `crewship doctor`",
      description:
        "Verifies Docker, the embedded UI, the DB, and your API key wiring. First thing to try if anything goes wrong.",
      icon: Stethoscope,
      cta: { label: "Copy command", copy: "crewship doctor" },
    },
    {
      id: "cli-pair",
      done: false,
      title: "Pair the CLI (optional)",
      description:
        "Drive Crewship from your terminal too: `crewship login --pair` walks you through linking this workspace to your shell.",
      icon: Terminal,
      cta: { label: "Copy command", copy: "crewship login --pair" },
    },
  ]

  return (
    <Card className="mb-6 border-primary/20 bg-gradient-to-br from-primary/5 via-background to-background">
      <CardContent className="p-5">
        <div className="flex items-start justify-between gap-4">
          <div className="space-y-1">
            <div className="flex items-center gap-2">
              <Sparkles className="h-4 w-4 text-primary" />
              <h3 className="text-base font-semibold">You're all set — three quick wins</h3>
            </div>
            <p className="text-xs text-muted-foreground">
              Onboarding put your first agent on the launch pad. These three steps cover the
              90% of "what now?" questions a fresh beta install raises.
            </p>
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={dismiss}
            aria-label="Dismiss welcome checklist"
            className="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
          >
            <X className="h-4 w-4" />
          </Button>
        </div>

        <ul className="mt-4 space-y-3">
          {items.map((item) => {
            const Icon = item.icon
            return (
              <li key={item.id} className="flex items-start gap-3">
                {item.done ? (
                  <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
                ) : (
                  <Circle className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground/60" />
                )}
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <Icon className="h-3.5 w-3.5 text-muted-foreground" />
                    <span className="text-sm font-medium">{item.title}</span>
                  </div>
                  <p className="text-xs text-muted-foreground mt-0.5">{item.description}</p>
                  <div className="mt-2">
                    {item.cta && "href" in item.cta && (
                      <Button asChild size="sm" variant="outline" className="h-7 text-xs">
                        <Link href={item.cta.href}>
                          {item.cta.label}
                          <ArrowRight className="ml-1 h-3 w-3" />
                        </Link>
                      </Button>
                    )}
                    {item.cta && "copy" in item.cta && (
                      <CopyButton text={item.cta.copy} label={item.cta.label} />
                    )}
                  </div>
                </div>
              </li>
            )
          })}
        </ul>
      </CardContent>
    </Card>
  )
}

/**
 * Tiny copy-to-clipboard button. Stays inline because the checklist is
 * the only place using it — a future second consumer would graduate
 * this into a shared component.
 */
function CopyButton({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState(false)
  async function handleCopy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1500)
    } catch {
      // Clipboard API can fail in non-secure contexts or when permission
      // is denied. Drop the visual feedback rather than throwing.
    }
  }
  return (
    <Button
      type="button"
      size="sm"
      variant="outline"
      className="h-7 text-xs font-mono"
      onClick={handleCopy}
    >
      {copied ? "Copied" : label}
    </Button>
  )
}
