"use client"

import { ArrowLeft, Lock, LogIn, SearchX, type LucideIcon } from "lucide-react"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import type { FrameAccess } from "./types"

const GATE: Record<Exclude<FrameAccess, "ok">, { icon: LucideIcon; tone: string; title: string; text: string; foot?: string }> = {
  signedout: { icon: LogIn, tone: "bg-primary/12 text-primary-hover", title: "Sign in to open this artifact", text: "You followed a link to an artifact. After you sign in you come back to this address.", foot: "Links never show content before sign-in." },
  denied: { icon: Lock, tone: "bg-warn/12 text-warn", title: "You don’t have access", text: "This artifact is not shared with you. Its owner can add you." },
  missing: { icon: SearchX, tone: "bg-muted text-muted-foreground", title: "This artifact doesn’t exist", text: "It was deleted, or the link is wrong." },
}

/** One clear page per gate; never the artifact's name or content behind it. */
export function AccessGate({ access, onHome, signInHref }: { access: Exclude<FrameAccess, "ok">; onHome: () => void; signInHref?: string }) {
  const gate = GATE[access]
  const Icon = gate.icon
  return <div role="region" aria-label={gate.title} data-access-gate={access} className="m-auto grid max-w-[420px] justify-items-center gap-2.5 px-5 py-10 text-center">
    <span aria-hidden className={cn("grid size-[52px] place-items-center rounded-xl", gate.tone)}><Icon className="size-[22px]" /></span>
    <h2 className="mt-1 text-xl font-semibold">{gate.title}</h2>
    <p className="text-sm text-muted-foreground">{gate.text}</p>
    <div className="mt-1.5 flex flex-wrap justify-center gap-2">
      {access === "signedout" && signInHref
        ? <Button asChild size="sm"><a href={signInHref}><LogIn aria-hidden />Sign in</a></Button>
        : <Button type="button" size="sm" variant="outline" onClick={onHome}><ArrowLeft aria-hidden />Back to Artifacts</Button>}
    </div>
    {gate.foot && <small className="text-xs text-muted-foreground">{gate.foot}</small>}
  </div>
}
