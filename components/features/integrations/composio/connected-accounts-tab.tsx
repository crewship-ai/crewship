"use client"

import * as React from "react"
import { Users } from "lucide-react"

import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import { ToolkitIcon, StatusDot, EmptyHint, toolkitLabel } from "./shared"
import type { Inventory } from "./types"

type AccountAction = "revoke" | "refresh" | "remove"

// ConnectedAccountsTab — connected accounts grouped by Composio user (the
// isolation bucket). Each account exposes revoke / refresh / remove; each user
// gets a "+ Connect account" that opens the shared ConnectModal pre-scoped to
// that user. All mutations refresh the inventory on success.
export function ConnectedAccountsTab({
  workspaceId,
  data,
  onConnectForUser,
  onChanged,
}: {
  workspaceId: string
  data: Inventory
  onConnectForUser: (userId: string) => void
  onChanged: () => void
}) {
  // accountId currently mutating → which action (drives the disabled/spinner).
  const [busy, setBusy] = React.useState<Record<string, AccountAction>>({})
  const [err, setErr] = React.useState<string | null>(null)

  const act = React.useCallback(
    async (accountId: string, action: AccountAction) => {
      setBusy((b) => ({ ...b, [accountId]: action }))
      setErr(null)
      try {
        const base = `/api/v1/integrations/composio/accounts/${accountId}`
        const url =
          action === "remove"
            ? `${base}?workspace_id=${workspaceId}`
            : `${base}/${action}?workspace_id=${workspaceId}`
        const r = await fetch(url, { method: action === "remove" ? "DELETE" : "POST" })
        if (!r.ok) {
          const body = await r.json().catch(() => null)
          throw new Error(body?.detail || `Failed (${r.status})`)
        }
        onChanged()
      } catch (e) {
        setErr(e instanceof Error ? e.message : "Action failed")
      } finally {
        setBusy((b) => {
          const next = { ...b }
          delete next[accountId]
          return next
        })
      }
    },
    [workspaceId, onChanged],
  )

  return (
    <section className="space-y-3">
      <h2 className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
        <Users className="h-3.5 w-3.5" /> Connected accounts
        <span className="font-normal normal-case tracking-normal text-muted-foreground/70">
          · grouped by Composio user ({data.users.length})
        </span>
      </h2>

      {err && <div className="text-[11px] text-red-400">{err}</div>}

      {data.users.length === 0 ? (
        <EmptyHint text="No connected accounts yet. Connect an app from the Catalog tab and it appears here, grouped by the user it belongs to." />
      ) : (
        <div className="space-y-2">
          {data.users.map((u) => (
            <div key={u.user_id} className="rounded-xl border border-white/10 bg-card p-3">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0 font-mono text-xs text-foreground/90 truncate">
                  {u.user_id}
                  <span className="ml-2 font-sans text-[10px] text-muted-foreground">
                    {u.connected_accounts.length} account
                    {u.connected_accounts.length === 1 ? "" : "s"}
                  </span>
                </div>
                <Button
                  variant="ghost"
                  size="xs"
                  className="shrink-0"
                  onClick={() => onConnectForUser(u.user_id)}
                >
                  + Connect account
                </Button>
              </div>
              {/* Accounts stack vertically — one full-width row each — so
                  duplicates and long slugs read top-to-bottom instead of
                  wrapping unpredictably as inline pills. Reuses the bordered-
                  rows pattern from the access editor's granted-apps list. */}
              <div className="mt-2 overflow-hidden rounded-lg border border-white/10">
                {u.connected_accounts.map((a) => {
                  const pending = busy[a.id]
                  return (
                    <div
                      key={a.id}
                      data-testid={`account-row-${a.id}`}
                      className="flex items-center gap-3 border-t border-white/[0.06] px-3 py-2 first:border-t-0"
                    >
                      <span className="flex min-w-0 flex-1 items-center gap-2 text-[13px]">
                        <ToolkitIcon toolkit={a.toolkit} size={16} />
                        <span className="truncate font-medium">
                          {toolkitLabel(a.toolkit.slug)}
                        </span>
                      </span>
                      <StatusDot status={a.status} />
                      <span className="flex shrink-0 items-center gap-1 border-l border-white/10 pl-2.5">
                        <AccountAction
                          label="Refresh"
                          onClick={() => act(a.id, "refresh")}
                          pending={pending === "refresh"}
                          disabled={!!pending}
                        />
                        <AccountAction
                          label="Revoke"
                          onClick={() => act(a.id, "revoke")}
                          pending={pending === "revoke"}
                          disabled={!!pending}
                        />
                        <AccountAction
                          label="Remove"
                          danger
                          onClick={() => act(a.id, "remove")}
                          pending={pending === "remove"}
                          disabled={!!pending}
                        />
                      </span>
                    </div>
                  )
                })}
              </div>
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

function AccountAction({
  label,
  onClick,
  pending,
  disabled,
  danger,
}: {
  label: string
  onClick: () => void
  pending: boolean
  disabled: boolean
  danger?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "rounded px-1.5 py-0.5 text-[10px] transition-colors disabled:opacity-50",
        danger
          ? "text-muted-foreground hover:text-red-400"
          : "text-muted-foreground hover:text-foreground",
      )}
    >
      {pending ? "…" : label}
    </button>
  )
}
