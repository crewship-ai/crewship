"use client"

import * as React from "react"
import Link from "next/link"
import { Plug, ShieldCheck, KeyRound, ArrowRight } from "lucide-react"

import { cn } from "@/lib/utils"

/**
 * Placeholder shown at `/integrations` while the legacy self-hosted MCP
 * connector UI is gated off (see `legacyMcpIntegrations()`). The hand-rolled
 * MCP server management is being replaced by a managed integration platform
 * (Composio): users connect their apps via OAuth instead of standing up and
 * babysitting their own MCP servers.
 *
 * This is an intentional stopgap — it will be replaced by the real managed
 * connector list once the Composio integration lands.
 */
export function ManagedIntegrationsComingSoon() {
  return (
    <div className="p-4 md:p-6 pb-10 space-y-4 bg-background min-h-[calc(100vh-48px)]">
      <div className="flex items-center gap-2">
        <Plug className="h-4 w-4 text-foreground/60" />
        <h1 className="text-body font-medium text-foreground/80">Connectors</h1>
      </div>

      <div
        className={cn(
          "mx-auto mt-8 max-w-xl rounded-xl border border-white/10 bg-card p-8 text-center",
          "shadow-lg shadow-blue-500/5",
        )}
      >
        <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-xl bg-blue-500/10">
          <Plug className="h-6 w-6 text-blue-400" />
        </div>

        <h2 className="text-base font-semibold text-foreground">
          Managed integrations are coming soon
        </h2>
        <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
          We&apos;re replacing self-hosted MCP servers with a managed
          integration platform. Soon you&apos;ll connect apps like GitHub,
          Slack, and Google in one click — OAuth, token refresh, and tool
          access handled for you, with nothing to host or babysit.
        </p>

        <div className="mt-6 grid gap-3 text-left sm:grid-cols-2">
          <div className="rounded-lg border border-white/10 bg-white/[0.02] p-3">
            <ShieldCheck className="h-4 w-4 text-blue-400" />
            <div className="mt-2 text-xs font-medium text-foreground/90">
              Per-user OAuth
            </div>
            <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
              Each agent acts on behalf of the connected user — no shared
              secrets.
            </div>
          </div>
          <div className="rounded-lg border border-white/10 bg-white/[0.02] p-3">
            <Plug className="h-4 w-4 text-blue-400" />
            <div className="mt-2 text-xs font-medium text-foreground/90">
              Hundreds of apps
            </div>
            <div className="mt-0.5 text-[11px] leading-relaxed text-muted-foreground">
              A single managed catalog replaces hand-configured MCP endpoints.
            </div>
          </div>
        </div>

        <div className="mt-6 flex items-center justify-center">
          <Link
            href="/credentials"
            className="inline-flex items-center gap-1.5 text-xs text-blue-400 transition-all hover:gap-2.5"
          >
            <KeyRound className="h-3.5 w-3.5" />
            Manage credentials in the meantime
            <ArrowRight className="h-3 w-3" />
          </Link>
        </div>
      </div>
    </div>
  )
}
