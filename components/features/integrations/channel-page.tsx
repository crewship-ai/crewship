"use client"

import * as React from "react"
import { Activity, Bot, FileText, Filter, Send, Terminal, Trash2 } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Skeleton } from "@/components/ui/skeleton"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { SettingsCard, SettingsDangerCard, SettingsRow, SettingsSegmented } from "@/components/features/settings/shared"
import { useChannelAgents } from "@/hooks/use-channel-agents"
import type { ChannelPatchBody } from "@/hooks/use-notification-channels"
import type { NotificationDelivery } from "@/hooks/use-notification-deliveries"
import { NOTIFICATION_CATEGORY_GROUPS } from "@/lib/notification-categories"
import { formatRelativeShort } from "@/lib/time"
import type { ConnectionRow } from "./connection-model"
import { ChannelMark, ChannelState, DeliveryRows, eventsSummary, priorityLabel } from "./channels-space"
import { Banner, CliLines, FlowSteps, InfoCard, MetaChip, ObjectPage } from "./integrations-ui"

/**
 * One channel or outgoing webhook (Harbor 2, I-CH): the flow When → Only if →
 * Message → Sends to, then Overview · Events · Deliveries · Settings.
 *
 * Not drawn, because the server has no such thing: "Send again" on a failed
 * delivery (the dispatcher already tries 3 times), rotating a webhook's
 * signing secret (it is shown once, when the webhook is created), two-way chat.
 */

export const CHANNEL_PANES = ["overview", "events", "deliveries", "settings"] as const
export type ChannelPane = (typeof CHANNEL_PANES)[number]

type Priority = NonNullable<ChannelPatchBody["min_priority"]>
const PRIORITIES: { value: Priority; label: string }[] = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Normal" },
  { value: "high", label: "High" },
  { value: "urgent", label: "Urgent" },
]
const ALL_CATEGORIES = NOTIFICATION_CATEGORY_GROUPS.flatMap((g) => g.categories.map((c) => c.key))

/** The allowlist after one switch; [] means every category again. */
export function toggleCategory(current: string[], key: string, on: boolean): string[] {
  const base = current.length === 0 ? ALL_CATEGORIES : current
  const next = on ? Array.from(new Set([...base, key])) : base.filter((k) => k !== key)
  return ALL_CATEGORIES.every((k) => next.includes(k)) ? [] : ALL_CATEGORIES.filter((k) => next.includes(k))
}

export function ChannelPage({
  workspaceId,
  row,
  pane,
  onPane,
  deliveries,
  providerOff,
  canEdit,
  onBack,
  onPatch,
  onTest,
  onRemove,
}: {
  workspaceId: string
  row: ConnectionRow
  pane: ChannelPane
  onPane: (pane: ChannelPane) => void
  /** This channel's slice of the log; null when the caller may not read it. */
  deliveries: NotificationDelivery[] | null
  /** The service is turned off for this server (Admin › Notifications). */
  providerOff: boolean
  /** false: another member's personal channel, or a workspace channel without manage rights. */
  canEdit: boolean
  onBack: () => void
  onPatch: (id: string, body: ChannelPatchBody) => Promise<void>
  onTest: (row: ConnectionRow) => Promise<void>
  /** Resolves once the server removed it; throws when it refused. */
  onRemove: (row: ConnectionRow) => Promise<void>
}) {
  const webhook = row.kind === "webhook"
  const space = webhook ? "Webhooks" : "Channels"
  const [confirmRemove, setConfirmRemove] = React.useState(false)
  const [testing, setTesting] = React.useState(false)
  const [saving, setSaving] = React.useState<string | null>(null)
  const priority = (row.channel?.min_priority ?? "low") as Priority

  const patch = async (what: string, body: ChannelPatchBody, done: string) => {
    setSaving(what)
    try {
      await onPatch(row.id, body)
      toast.success(done)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "The change was not saved")
    } finally {
      setSaving(null)
    }
  }

  const test = async () => {
    setTesting(true)
    try {
      await onTest(row)
    } finally {
      setTesting(false)
    }
  }

  const failed = (deliveries ?? []).filter((d) => d.status === "failed")
  const destination = webhook ? row.channel?.url : row.channel?.type === "email" ? row.channel?.to : row.providerLabel

  let body: React.ReactNode
  if (pane === "events") {
    body = (
      <>
        {NOTIFICATION_CATEGORY_GROUPS.map((g) => (
          <SettingsCard key={g.key} title={g.label}>
            {g.categories.map((c) => {
              const on = row.categories.length === 0 || row.categories.includes(c.key)
              return (
                <SettingsRow key={c.key} label={<span className="font-mono text-label">{c.key}</span>} description={c.hint}>
                  <Switch
                    checked={on}
                    disabled={!canEdit || saving !== null}
                    aria-label={`Send ${c.key} here`}
                    onCheckedChange={(next) => void patch(c.key, { categories: toggleCategory(row.categories, c.key, next) }, next ? `${c.label} is sent here` : `${c.label} is no longer sent here`)}
                  />
                </SettingsRow>
              )
            })}
          </SettingsCard>
        ))}
        <SettingsCard title="Lowest priority" description="Quieter events are not sent here" icon={Filter}>
          <div className="px-4 py-3">
            <SettingsSegmented
              label="Lowest priority"
              options={PRIORITIES}
              value={priority}
              disabled={!canEdit || saving !== null}
              onChange={(v) => void patch("priority", { min_priority: v }, `Sends ${priorityLabel(v).toLowerCase()} and up`)}
            />
          </div>
        </SettingsCard>
      </>
    )
  } else if (pane === "deliveries") {
    body =
      deliveries === null ? (
        <InfoCard title="Deliveries" icon={Activity}>
          <p className="px-4 py-3 text-body text-muted-foreground">The delivery log is admin-only: it spans every recipient in this workspace.</p>
        </InfoCard>
      ) : deliveries.length === 0 ? (
        <InfoCard title="Deliveries" icon={Activity}>
          <p className="px-4 py-3 text-body text-muted-foreground">Nothing has been sent here yet.</p>
        </InfoCard>
      ) : (
        <>
          <DeliveryRows deliveries={deliveries} caption={`Deliveries to ${row.name}`} />
          <p className="text-label text-muted-foreground">Crewship tries each delivery up to 3 times before it records a failure. Older ones are in Activity.</p>
        </>
      )
  } else if (pane === "settings") {
    body = (
      <>
        <SettingsCard title="Destination" icon={Send}>
          <SettingsRow label={webhook ? "URL" : row.channel?.type === "email" ? "Address" : "Where"} description={webhook ? "https only; private addresses are refused" : undefined}>
            <span className="max-w-[18rem] truncate font-mono text-label text-muted-foreground">{destination || "—"}</span>
          </SettingsRow>
          {webhook && (
            <SettingsRow label="Signing secret" description="Shown once, when the webhook was created. To replace it, add the webhook again.">
              <span className="text-label text-muted-foreground">{row.channel ? "Set" : "—"}</span>
            </SettingsRow>
          )}
          <SettingsRow label="Test" description="Sends one test message">
            <Button variant="outline" size="sm" disabled={!canEdit || testing} onClick={() => void test()}>
              <Send className="size-3.5" aria-hidden />
              Send test
            </Button>
          </SettingsRow>
          <SettingsRow label="Paused" description="Nothing is sent while it is paused">
            <Switch
              checked={!row.enabled}
              disabled={!canEdit || saving !== null}
              aria-label="Paused"
              onCheckedChange={(paused) => void patch("enabled", { enabled: !paused }, paused ? `${row.name} is paused` : `${row.name} sends again`)}
            />
          </SettingsRow>
        </SettingsCard>
        <SettingsCard title="Command line" icon={Terminal}>
          <CliLines
            lines={[
              [`crewship notifychannel test ${row.id}`, "Send a test"],
              [`crewship notifychannel deliveries --channel ${row.id}`, "Recent deliveries"],
              [`crewship notifychannel agents list ${row.id}`, "Agents that may send here"],
            ]}
          />
        </SettingsCard>
        {canEdit && (
          <SettingsDangerCard title="Danger zone">
            <SettingsRow label={webhook ? "Remove webhook" : "Remove channel"} description="Agents and routines stop sending here">
              <Button variant="outline" size="sm" onClick={() => setConfirmRemove(true)}>
                <Trash2 className="size-3.5" aria-hidden />
                Remove…
              </Button>
            </SettingsRow>
          </SettingsDangerCard>
        )}
      </>
    )
  } else {
    body = (
      <>
        <FlowSteps
          label="What this channel does"
          steps={[
            { icon: <Activity className="size-[18px]" />, label: "When", value: eventsSummary(row.categories) },
            { icon: <Filter className="size-[18px]" />, label: "Only if", value: `Priority ${priorityLabel(priority).toLowerCase()} and up` },
            { icon: <FileText className="size-[18px]" />, label: "Message", value: webhook ? "JSON, signed with X-Crewship-Signature" : "A message per kind of event" },
            { icon: <ChannelMark row={row} size={24} />, label: "Sends to", value: row.name },
          ]}
        />
        {providerOff && <Banner tone="warn" title="Turned off for this server">An admin turned {row.providerLabel} off in Admin › Notifications, so nothing is sent here until it is turned back on.</Banner>}
        {row.status === "failing" && (
          <Banner tone="bad" title={failed.length > 0 ? `${failed.length} ${failed.length === 1 ? "delivery" : "deliveries"} failed` : "The last delivery failed"} action={<Button size="sm" variant="outline" onClick={() => onPane("deliveries")}>Deliveries</Button>}>
            {failed[0]?.error ?? "The destination refused it."} Crewship tried 3 times; the run that caused it was not affected.
          </Banner>
        )}
        {row.readOnly && <Banner tone="info" title="Another member's personal channel">You can see that it exists and what kind it is; where it points is theirs.</Banner>}
        <div className="grid gap-3.5 lg:grid-cols-2">
          {webhook ? (
            <InfoCard title="What your system receives" icon={FileText}>
              <pre className="overflow-x-auto px-4 py-3 font-mono text-label text-foreground">{`POST ${row.channel?.url ?? "https://…"}
X-Crewship-Signature: sha256=…
X-Crewship-Event: routines.failed

{
  "event": "routines.failed",
  "priority": "high",
  "message": "…",
  "url": "https://…"
}`}</pre>
              <p className="border-t border-border px-4 py-2.5 text-label text-muted-foreground">Verify the signature with the signing secret shown when the webhook was created. Private addresses are refused.</p>
            </InfoCard>
          ) : (
            <InfoCard title="Last messages" icon={Activity}>
              {deliveries === null ? (
                <p className="px-4 py-3 text-body text-muted-foreground">The delivery log is admin-only.</p>
              ) : deliveries.length === 0 ? (
                <p className="px-4 py-3 text-body text-muted-foreground">Nothing has been sent here yet.</p>
              ) : (
                <ul className="divide-y divide-border">
                  {deliveries.slice(0, 4).map((d) => (
                    <li key={d.id} className="flex min-w-0 items-center gap-3 px-4 py-2">
                      <span className="w-16 shrink-0 font-mono text-label text-muted-foreground">{formatRelativeShort(d.created_at)}</span>
                      <span className="min-w-0 flex-1 truncate text-body text-foreground">{d.title || d.category}</span>
                      <span className="shrink-0 font-mono text-label text-muted-foreground">{d.category}</span>
                    </li>
                  ))}
                </ul>
              )}
            </InfoCard>
          )}
          <ChannelAgents workspaceId={workspaceId} channelId={row.id} />
        </div>
      </>
    )
  }

  return (
    <>
      <ObjectPage
        backLabel={space}
        onBack={onBack}
        lead={<ChannelMark row={row} size={52} />}
        title={row.name}
        titleMono={webhook}
        description={`${row.providerLabel} · ${row.scope === "personal" ? "Personal channel" : "Workspace channel"}`}
        chips={
          <>
            <ChannelState row={row} size="md" />
            <MetaChip>{row.categories.length === 0 ? "Every event" : `${row.categories.length} kinds of events`} · {priorityLabel(priority).toLowerCase()} and up</MetaChip>
          </>
        }
        tabs={[
          { id: "overview", label: "Overview" },
          { id: "events", label: "Events" },
          { id: "deliveries", label: "Deliveries" },
          { id: "settings", label: "Settings" },
        ]}
        tab={pane}
        onTab={(t) => onPane(t as ChannelPane)}
      >
        {body}
      </ObjectPage>
      <ConfirmDialog
        open={confirmRemove}
        onOpenChange={setConfirmRemove}
        title={`Remove ${row.name}?`}
        consequences={[
          { tone: "lost", text: "Agents and routines stop sending here" },
          { tone: "kept", text: "The delivery log keeps what was already sent" },
        ]}
        confirmLabel={webhook ? "Remove webhook" : "Remove channel"}
        destructive
        onConfirm={async () => {
          await onRemove(row)
        }}
      />
    </>
  )
}

/** "Agents that may send here" — kept from the product; the server has an allowlist. */
function ChannelAgents({ workspaceId, channelId }: { workspaceId: string; channelId: string }) {
  const { agents, loading } = useChannelAgents(workspaceId, channelId)
  return (
    <InfoCard title="Agents that may send here" icon={Bot} right={loading ? undefined : <span className="text-label tabular-nums text-muted-foreground">{agents.length}</span>}>
      {loading ? (
        <div className="space-y-2 px-4 py-3">
          <Skeleton className="h-4 w-40" />
          <Skeleton className="h-4 w-28" />
        </div>
      ) : agents.length === 0 ? (
        <p className="px-4 py-3 text-body text-muted-foreground">None. Only Crewship itself delivers here; no agent can post of its own accord.</p>
      ) : (
        <ul className="divide-y divide-border">
          {agents.map((a) => (
            <li key={a.id} className="flex min-w-0 items-center gap-2 px-4 py-2 text-body">
              <Bot className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
              <span className="min-w-0 flex-1 truncate text-foreground">{a.agent_name || a.agent_id}</span>
              {a.agent_slug && <span className="shrink-0 font-mono text-label text-muted-foreground">{a.agent_slug}</span>}
            </li>
          ))}
        </ul>
      )}
    </InfoCard>
  )
}
