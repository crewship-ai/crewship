"use client"

import { Bot, ChevronLeft, User } from "lucide-react"

import { cn } from "@/lib/utils"
import { ProviderMark } from "../provider-marks"
import { brandLogo } from "../composio/shared"
import type { AgentBindingsMap, AgentLite, ConnectedAccount } from "../composio/types"

/**
 * One connected tool account, in full.
 *
 * Same job as the notification connection detail: the list says a Gmail
 * account exists, this says whose it is, whether its token still works, and —
 * the question people actually open it for — which agents can act through it.
 *
 * Read-only. Composio owns connect, refresh and revoke, and those controls
 * live on its Connected accounts view; duplicating them here would be a second
 * weaker copy of a flow that already exists.
 */

interface ToolAccountDetailProps {
  account: ConnectedAccount
  agents: AgentLite[]
  bindings: AgentBindingsMap
  onBack: () => void
}

export function ToolAccountDetail({
  account,
  agents,
  bindings,
  onBack,
}: ToolAccountDetailProps) {
  const slug = account.toolkit.slug
  const active = account.status.toUpperCase() === "ACTIVE"

  // An agent reaches this account through a binding on its TOOLKIT, not on the
  // account row — Composio's model is per-toolkit. So "who can use this Gmail
  // account" is "who is bound to gmail", which is what the grant actually says.
  const boundAgents = agents.filter((a) =>
    (bindings[a.id] ?? []).some((b) => b.toolkit === slug),
  )

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
        <button
          type="button"
          onClick={onBack}
          className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs font-medium text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          Back to accounts
        </button>
        <span className="truncate text-xs font-medium text-foreground/85">{slug}</span>
      </div>

      <div className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4 md:p-6">
        <div className="flex flex-wrap items-start gap-3 rounded-xl border border-white/[0.08] bg-card px-4 py-3.5">
          <ProviderMark
            provider={slug}
            label={slug}
            logoUrl={account.toolkit.logo || brandLogo(slug)}
            className="h-9 w-9"
          />
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-medium capitalize text-foreground/90">{slug}</div>
            <div className="flex items-center gap-1.5 truncate font-mono text-[11px] text-muted-foreground">
              <User className="h-3 w-3 shrink-0" />
              {account.user_id}
            </div>
          </div>
          <span
            className={cn(
              "inline-flex items-center rounded-full border px-2 py-0.5 text-[10px]",
              active
                ? "border-emerald-400/30 bg-emerald-400/10 text-emerald-300"
                : "border-amber-400/30 bg-amber-400/10 text-amber-300",
            )}
          >
            {account.status.toLowerCase()}
          </span>
        </div>

        {!active && (
          <div className="rounded-lg border border-amber-400/30 bg-amber-400/[0.06] px-3 py-2 text-xs text-amber-200">
            This account&apos;s authorisation is no longer valid. Agents bound to{" "}
            <span className="font-mono">{slug}</span> cannot act through it until it is
            reconnected — use <span className="font-medium">Refresh</span> on the Connected
            accounts view.
          </div>
        )}

        <section className="overflow-hidden rounded-xl border border-white/[0.08] bg-card">
          <div className="flex items-baseline gap-2 border-b border-white/[0.06] px-4 py-2.5">
            <h3 className="text-[10px] font-semibold uppercase tracking-wider text-foreground/50">
              Agents that can act through it
            </h3>
            <span className="font-mono text-[10px] text-muted-foreground/60">
              {boundAgents.length}
            </span>
          </div>
          {boundAgents.length === 0 ? (
            <p className="px-4 py-3 text-xs leading-relaxed text-muted-foreground">
              None. The account is connected, but no agent is granted{" "}
              <span className="font-mono">{slug}</span> — grant one on{" "}
              <span className="font-medium text-foreground/70">Agent access</span>.
            </p>
          ) : (
            <ul className="divide-y divide-white/[0.04]">
              {boundAgents.map((a) => {
                const grant = (bindings[a.id] ?? []).find((b) => b.toolkit === slug)
                return (
                  <li key={a.id} className="flex items-center gap-2 px-4 py-2 text-xs">
                    <Bot className="h-3 w-3 shrink-0 text-muted-foreground/70" />
                    <span className="min-w-0 flex-1 truncate text-foreground/85">{a.name}</span>
                    {grant?.mode && (
                      <span
                        className="shrink-0 rounded-md border border-white/[0.08] bg-white/[0.03] px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                        title={
                          grant.mode === "full"
                            ? "Every tool on this toolkit"
                            : "A restricted set of tools"
                        }
                      >
                        {grant.mode}
                      </span>
                    )}
                  </li>
                )
              })}
            </ul>
          )}
        </section>
      </div>
    </div>
  )
}
