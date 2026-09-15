"use client"

import { IncomingTargetAvatar } from "../incoming-target-avatar"
import * as React from "react"
import Link from "next/link"
import { useQuery } from "@tanstack/react-query"
import {
  ChevronLeft,
  Copy,
  ExternalLink,
  Plus,
  RotateCw,
  Trash2,
} from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { KpiCard } from "@/components/features/dashboard/kpi-card"
import { usePageWebhooks, usePageWebhookRevoke } from "@/hooks/use-page-sharing"
import {
  INCOMING_KINDS,
  resolveIncomingTarget,
  type IncomingEndpoint,
  type IncomingSection,
  type IncomingTarget,
} from "../incoming-model"
import { incomingJSON, type IncomingData } from "../use-incoming-endpoints"
import {
  card,
  time,
  IncomingError,
  Loading,
  Panel,
  Empty,
} from "./incoming-shared"
import {
  absolute,
  copy,
  RevealDialog,
  type Reveal,
} from "./incoming-credentials"

export function IncomingWebhooksView({
  data,
  section,
  search,
  targetId,
  onSelect,
  onBack,
  onAdd,
  workspaceId,
}: {
  data: IncomingData
  section: IncomingSection
  search: string
  targetId: string | null
  onSelect: (t: IncomingTarget) => void
  onBack: () => void
  onAdd: (t?: IncomingTarget) => void
  workspaceId: string
}) {
  const [busy, setBusy] = React.useState(false)
  const [failure, setFailure] = React.useState<string | null>(null)
  const [reveal, setReveal] = React.useState<Reveal | null>(null)
  const [confirm, setConfirm] = React.useState<{
    title: string
    run: () => Promise<void>
  } | null>(null)
  const target = resolveIncomingTarget(data.targets, section, targetId)
  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setFailure(null)
    try {
      await fn()
    } catch (e) {
      setFailure(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  const toggle = (r: IncomingEndpoint, value: boolean) =>
    run(async () => {
      await data.hooks.update(r.id, { enabled: value })
    })
  const remove = (r: IncomingEndpoint) =>
    setConfirm({
      title: `Delete endpoint ${r.name}?`,
      run: async () => {
        await data.hooks.remove(r.id)
      },
    })
  const rotate = (r: IncomingEndpoint) =>
    setConfirm({
      title: `Rotate secret for ${r.name}?`,
      run: async () => {
        if (r.routine) {
          const result = await data.hooks.update(r.id, { rotate_secret: true })
          if (result?.signing_secret)
            setReveal({ secret: result.signing_secret, target: r.target })
        } else {
          const result = await incomingJSON<{ webhook_secret: string }>(
            `/api/v1/agents/${r.id}/webhook-secret/rotate?workspace_id=${encodeURIComponent(workspaceId)}`,
            { method: "POST" },
          )
          setReveal({
            secret: result.webhook_secret,
            url: absolute(r.path),
            target: r.target,
          })
          data.refresh()
        }
      },
    })
  if (data.loading && !data.targets.length) return <Loading />
  const rows = data.rows.filter(
    (r) =>
      (target
        ? r.target.id === target.id && r.target.kind === target.kind
        : section === "endpoints" || r.target.kind === section) &&
      `${r.name} ${r.target.name} ${r.target.slug} ${r.sender}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  )
  return (
    <>
      {targetId && !target && !data.loading ? (
        <div className="p-4 md:p-6">
          <IncomingError
            message="This target is unavailable in the current workspace."
            onRetry={data.refresh}
          />
          <Button variant="ghost" onClick={onBack}>
            Back to endpoints
          </Button>
        </div>
      ) : target ? (
        <>
          <div className="flex items-center gap-2 border-b border-border bg-card/40 px-4 py-2">
            <Button variant="ghost" size="sm" onClick={onBack}>
              <ChevronLeft className="size-3.5" />
              Back to endpoints
            </Button>
            <IncomingTargetAvatar target={target} />
            <span className="truncate text-xs font-medium">{target.name}</span>
          </div>
          <div className="space-y-4 p-4 md:p-6">
            <div
              className={`${card} flex flex-wrap items-center gap-3 px-4 py-3.5`}
            >
              <div className="min-w-0 flex-1">
                <h2 className="text-sm font-medium">{target.name}</h2>
                <p className="text-xs text-muted-foreground">
                  {INCOMING_KINDS.find((k) => k.key === target.kind)?.hint}
                </p>
              </div>
              <Button
                variant="soft"
                size="sm"
                onClick={() => onAdd(target)}
                disabled={target.kind === "agent" && rows.length > 0}
              >
                <Plus className="size-3.5" />
                Add endpoint
              </Button>
              <Button variant="ghost" size="sm" asChild>
                <Link
                  href={
                    target.kind === "page"
                      ? `/pages/${encodeURIComponent(target.slug)}`
                      : target.kind === "agent"
                        ? `/activity?walk=agent:${encodeURIComponent(target.id)}`
                        : `/activity?pipeline=${encodeURIComponent(target.slug)}`
                  }
                >
                  <ExternalLink className="size-3.5" />
                  {target.kind === "page" ? "Open Page" : "Open activity"}
                </Link>
              </Button>
            </div>
            {(failure || data.error) && (
              <IncomingError
                message={failure ?? data.error!}
                onRetry={data.refresh}
              />
            )}
            {target.kind === "page" ? (
              <PageEndpoints
                workspaceId={workspaceId}
                target={target}
                onAdd={() => onAdd(target)}
              />
            ) : (
              <>
                <Panel title="Endpoints">
                  <EndpointTable
                    rows={rows}
                    busy={busy}
                    onToggle={toggle}
                    onDelete={remove}
                    onRotate={rotate}
                  />
                  {!rows.length && <Empty onAdd={() => onAdd(target)} />}
                </Panel>
                <p className="text-xs text-muted-foreground">
                  {target.kind === "agent"
                    ? "The URL contains no secret and remains available. Only the signing secret is shown once. Agent endpoints support secret rotation; disabling or deleting an endpoint separately is not supported."
                    : "The token in the receiving URL is shown once. Save it when creating the endpoint. Rotating the signing secret preserves that URL and cannot recover it; create a replacement endpoint if the URL was lost."}
                </p>
                <RecentReceipts
                  workspaceId={workspaceId}
                  target={target}
                  rows={rows}
                />
              </>
            )}
          </div>
        </>
      ) : (
        <div className="space-y-4 p-4 md:p-6">
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
            <KpiCard
              label="Endpoints"
              value={data.error ? "—" : data.rows.length}
              subtitle="Routine + agent endpoints; Pages per Page"
            />
            <KpiCard
              label="Receiving"
              value={
                data.error
                  ? "—"
                  : data.rows.filter((r) => r.enabled && r.signed).length
              }
              subtitle="Enabled with credentials; not a delivery probe"
            />
            <KpiCard
              label="Needs attention"
              value={
                data.error
                  ? "—"
                  : data.rows.filter((r) => !r.enabled || !r.signed).length
              }
              subtitle="Disabled or missing a signing secret"
            />
            <KpiCard
              label="Received · 24h"
              value="—"
              subtitle="No aggregate count available from the API"
            />
          </div>
          {data.error && (
            <IncomingError message={data.error} onRetry={data.refresh} />
          )}
          {failure && <IncomingError message={failure} />}
          <Panel title={`Endpoints · ${rows.length} shown`}>
            <EndpointTable
              rows={rows}
              onSelect={onSelect}
              busy={busy}
              onToggle={toggle}
              onDelete={remove}
              onRotate={rotate}
            />
            {!rows.length && !data.error && section !== "page" && (
              <Empty onAdd={() => onAdd()} />
            )}
            <div className="border-t border-white/[0.06] px-4 py-3 text-xs text-muted-foreground">
              Workspace totals cover routine and agent endpoints. Page endpoints
              are read on demand from each Page; missing data is not counted as
              zero. Actions remain subject to the target&apos;s permissions.
            </div>
          </Panel>
          {(section === "page" || section === "endpoints") &&
            data.targets.some((t) => t.kind === "page") && (
              <Panel title="Pages · endpoints listed per Page">
                <ul className="divide-y divide-white/[0.04]">
                  {data.targets
                    .filter(
                      (t) =>
                        t.kind === "page" &&
                        `${t.name} ${t.slug}`
                          .toLowerCase()
                          .includes(search.toLowerCase()),
                    )
                    .map((t) => (
                      <li key={t.id}>
                        <Button
                          variant="ghost"
                          className="w-full justify-between px-4 text-xs"
                          onClick={() => onSelect(t)}
                        >
                          <span className="flex min-w-0 items-center gap-2">
                            <IncomingTargetAvatar target={t} />
                            <span className="truncate">{t.name}</span>
                          </span>
                          <span className="text-muted-foreground">
                            View endpoints · per Page
                          </span>
                        </Button>
                      </li>
                    ))}
                </ul>
              </Panel>
            )}
          <p className="text-xs text-muted-foreground">
            Routines can create, update or comment on Issues. For app-managed
            subscriptions, see{" "}
            <a
              className="text-primary hover:underline"
              href="/integrations?tab=tools&section=triggers"
            >
              Tools → Triggers
            </a>
            ; Incoming is for direct HTTP senders.
          </p>
        </div>
      )}
      <ConfirmDialog
        open={!!confirm}
        onOpenChange={(open) => {
          if (!open) setConfirm(null)
        }}
        title={confirm?.title ?? ""}
        consequences={[
          {
            tone: "lost",
            text: "Existing senders using the old credentials will no longer be accepted.",
          },
          {
            tone: "kept",
            text: "Already received work and its receipts remain.",
          },
        ]}
        confirmLabel="Confirm"
        destructive
        onConfirm={async () => {
          if (confirm) await run(confirm.run)
          setConfirm(null)
        }}
      />
      {reveal && (
        <RevealDialog reveal={reveal} onClose={() => setReveal(null)} />
      )}
    </>
  )
}

function EndpointTable({
  rows,
  busy,
  onToggle,
  onDelete,
  onRotate,
  onSelect,
}: {
  rows: IncomingEndpoint[]
  busy: boolean
  onToggle?: (r: IncomingEndpoint, v: boolean) => void
  onDelete?: (r: IncomingEndpoint) => void
  onRotate?: (r: IncomingEndpoint) => void
  onSelect?: (t: IncomingTarget) => void
}) {
  if (!rows.length) return null
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-left text-xs">
        <thead>
          <tr>
            {[
              "Endpoint",
              "Target",
              "Sender",
              "Authentication",
              "Fired / last",
              "Status",
              "Actions",
              "Credentials",
            ].map((h) => (
              <th
                key={h}
                className="whitespace-nowrap px-4 py-2 text-[10px] font-medium uppercase tracking-wider text-muted-foreground"
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y divide-white/[0.04]">
          {rows.map((r) => (
            <tr key={r.id} className="hover:bg-white/[0.02]">
              <td className="px-4 py-3">
                {onSelect ? (
                  <button
                    className="text-left font-medium hover:text-primary"
                    onClick={() => onSelect(r.target)}
                  >
                    {r.name}
                  </button>
                ) : (
                  <span className="font-medium">{r.name}</span>
                )}
                <code
                  className="mt-1 block max-w-64 truncate text-[10px] text-muted-foreground"
                  title={r.path}
                >
                  {r.path}
                </code>
              </td>
              <td className="px-4 py-3">
                <span className="flex items-center gap-1.5">
                  <IncomingTargetAvatar target={r.target} />
                  {r.target.name}
                </span>
              </td>
              <td className="px-4 py-3">{r.sender}</td>
              <td className="px-4 py-3">
                <span
                  className={`rounded-full px-2 py-1 text-[10px] ${r.signed ? "bg-success/10 text-success" : "bg-warn/10 text-warn"}`}
                >
                  {r.target.kind === "page"
                    ? "Producer token"
                    : r.signed
                      ? "Signing configured"
                      : "Missing secret"}
                </span>
                {r.routine && (
                  <span className="mt-1 block text-[10px] text-muted-foreground">
                    {r.routine.rate_limit_per_min || 600}/min
                  </span>
                )}
              </td>
              <td className="whitespace-nowrap px-4 py-3">
                {r.fired ?? "—"}
                <span className="block text-[10px] text-muted-foreground">
                  {time(r.last)}
                </span>
              </td>
              <td className="px-4 py-3">
                <span className="flex items-center gap-1.5">
                  <span
                    className={`size-1.5 rounded-full ${r.enabled ? "bg-success" : "bg-muted-foreground/40"}`}
                  />
                  {r.enabled ? "Enabled" : "Disabled"}
                </span>
              </td>
              <td className="px-4 py-3">
                <div className="flex items-center gap-1">
                  {r.routine && onToggle && (
                    <Switch
                      size="sm"
                      checked={r.enabled}
                      disabled={busy}
                      aria-label={`${r.enabled ? "Disable" : "Enable"} endpoint ${r.name}`}
                      onCheckedChange={(v) => onToggle(r, v)}
                    />
                  )}{" "}
                  {onDelete && (r.routine || r.target.kind === "page") && (
                    <Button
                      variant="ghost"
                      size="icon"
                      disabled={
                        busy || (r.target.kind === "page" && !r.enabled)
                      }
                      aria-label={`Delete endpoint ${r.name}`}
                      onClick={() => onDelete(r)}
                    >
                      <Trash2 className="size-3.5" />
                    </Button>
                  )}
                </div>
              </td>
              <td className="whitespace-nowrap px-4 py-3">
                <div className="flex items-center gap-1">
                  {r.target.kind === "agent" && (
                    <Button
                      variant="ghost"
                      size="icon"
                      title="Copy URL"
                      aria-label="Copy receiving URL"
                      onClick={() => copy(absolute(r.path))}
                    >
                      <Copy className="size-3" />
                    </Button>
                  )}
                  {onRotate && (
                    <Button
                      variant="ghost"
                      size="icon"
                      title="Rotate secret"
                      aria-label="Rotate secret"
                      disabled={busy}
                      onClick={() => onRotate(r)}
                    >
                      <RotateCw className="size-3.5" />
                    </Button>
                  )}
                  {r.target.kind !== "agent" && !onRotate && (
                    <span className="text-muted-foreground">—</span>
                  )}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
function PageEndpoints({
  workspaceId,
  target,
  onAdd,
}: {
  workspaceId: string
  target: IncomingTarget
  onAdd: () => void
}) {
  const read = usePageWebhooks(workspaceId, target.slug)
  const [failure, setFailure] = React.useState<string | null>(null)
  const [deleting, setDeleting] = React.useState<IncomingEndpoint | null>(null)
  const revoke = usePageWebhookRevoke(workspaceId, target.slug, {
    onOk: () => setDeleting(null),
    onRefused: setFailure,
  })
  const rows: IncomingEndpoint[] = read.webhooks.map((w) => ({
    id: w.id,
    name: w.name || w.panel,
    target,
    sender: "Page producer",
    signed: true,
    enabled: w.live,
    fired: w.fire_count,
    last: w.last_fired_at,
    path: "/api/v1/page-webhooks/•••••",
  }))
  return (
    <>
      {read.loading ? (
        <Skeleton className="h-36 rounded-xl" />
      ) : (
        <Panel title="Endpoints">
          <EndpointTable
            rows={rows}
            busy={revoke.isPending}
            onDelete={setDeleting}
          />
          {!rows.length && !read.error && !read.refusal && (
            <Empty onAdd={onAdd} />
          )}
        </Panel>
      )}
      {(read.error || read.refusal || failure) && (
        <IncomingError message={read.error ?? read.refusal ?? failure!} />
      )}
      <p className="text-xs text-muted-foreground">
        The receiving URL contains a producer token, shown once at creation. To
        replace it, add an endpoint and delete the old one after updating your
        sender.
      </p>
      <Panel title="Recent receipts">
        <p className="px-4 py-3 text-xs text-muted-foreground">
          Pages expose the last received time and lifetime count above.
          Individual receipts are not available from this API.
        </p>
      </Panel>
      <ConfirmDialog
        open={!!deleting}
        onOpenChange={(open) => {
          if (!open) setDeleting(null)
        }}
        title={`Delete endpoint ${deleting?.name ?? ""}?`}
        confirmLabel="Delete endpoint"
        destructive
        consequences={[
          {
            tone: "lost",
            text: "This producer token will stop accepting panel updates.",
          },
        ]}
        onConfirm={() => {
          if (deleting) revoke.mutate({ id: deleting.id })
        }}
      />
    </>
  )
}
function RecentReceipts({
  workspaceId,
  target,
  rows,
}: {
  workspaceId: string
  target: IncomingTarget
  rows: IncomingEndpoint[]
}) {
  const ids = rows.map((r) => r.id)
  type Receipt = {
    id: string
    received_at: string
    label: string
    href: string
    status: string
  }
  const query = useQuery({
    queryKey: ["incoming-receipts", workspaceId, target.kind, ...ids],
    enabled: ids.length > 0,
    queryFn: async ({ signal }): Promise<Receipt[]> => {
      if (target.kind === "agent") {
        const result = await incomingJSON<{
          items: {
            id: string
            received_at: string
            work_id: string | null
            filter_decision: string
          }[]
        }>(
          `/api/v1/workspaces/${workspaceId}/webhook-deliveries?endpoint_id=${encodeURIComponent(target.id)}`,
          { signal },
        )
        return result.items.slice(0, 8).map((r) => ({
          id: r.id,
          received_at: r.received_at,
          label: r.work_id ?? r.id,
          href: `/activity?walk=agent:${encodeURIComponent(target.id)}`,
          status: `Admission: ${r.filter_decision}`,
        }))
      }
      const pages = await Promise.all(
        ids.map((id) =>
          incomingJSON<{
            items: {
              id: string
              received_at: string
              run_id: string
              run_status: string | null
            }[]
          }>(
            `/api/v1/workspaces/${workspaceId}/routine-webhook-receipts?webhook_id=${encodeURIComponent(id)}`,
            { signal },
          ),
        ),
      )
      return pages
        .flatMap((p) => p.items)
        .sort((a, b) => b.received_at.localeCompare(a.received_at))
        .slice(0, 8)
        .map((r) => ({
          ...r,
          label: r.run_id,
          href: `/activity?run=${encodeURIComponent(r.run_id)}`,
          status: r.run_status ?? "Run record not available",
        }))
    },
  })
  return (
    <Panel title="Recent receipts">
      {query.isFetching ? (
        <Skeleton className="m-4 h-16" />
      ) : query.error ? (
        <div className="p-3">
          <IncomingError
            message={query.error.message}
            onRetry={() => void query.refetch()}
          />
        </div>
      ) : query.data?.length ? (
        <ul className="divide-y divide-white/[0.04]">
          {query.data.map((r) => (
            <li
              key={r.id}
              className="flex flex-wrap justify-between gap-2 px-4 py-3 text-xs"
            >
              <span>{time(r.received_at)}</span>
              <Link className="text-primary" href={r.href}>
                {r.label}
              </Link>
              <span>{r.status}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="px-4 py-3 text-xs text-muted-foreground">
          No recent receipts available for these endpoints.
        </p>
      )}
    </Panel>
  )
}
