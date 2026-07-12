"use client"

import { useCallback, useState } from "react"
import { Bell, BellOff, Copy, Globe, Mail, Plus, Send, Trash2 } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Skeleton } from "@/components/ui/skeleton"
import { Spinner } from "@/components/ui/spinner"
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from "@/components/ui/select"
import { cn } from "@/lib/utils"
import {
  useNotificationChannels,
  type NotificationChannel,
} from "@/hooks/use-notification-channels"
import { SettingsCard, SettingsEmpty, SettingsRow } from "../shared"

interface NotificationChannelsSectionProps {
  workspaceId: string
}

/**
 * Settings → Notifications: CRUD + test over the workspace's outbound
 * run-terminal delivery targets (email / signed webhook, issue #850).
 * First UI surface for a backend that previously had CLI-only access.
 */
export function NotificationChannelsSection({ workspaceId }: NotificationChannelsSectionProps) {
  const { channels, loading, error, create, remove, sendTest } =
    useNotificationChannels(workspaceId)

  // form state
  const [type, setType] = useState<"email" | "webhook">("webhook")
  const [target, setTarget] = useState("")
  const [secret, setSecret] = useState("")
  const [events, setEvents] = useState<"failed" | "completed" | "all">("failed")
  const [creating, setCreating] = useState(false)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  // Webhook signing secret revealed exactly once on create.
  const [revealedSecret, setRevealedSecret] = useState<{ id: string; secret: string } | null>(null)

  const canCreate = target.trim() !== "" && !creating

  const handleCreate = useCallback(async () => {
    if (!canCreate) return
    setCreating(true)
    try {
      const created = await create({
        type,
        ...(type === "webhook"
          ? { url: target.trim(), ...(secret.trim() ? { secret: secret.trim() } : {}) }
          : { to: target.trim() }),
        events: [events],
      })
      toast.success("Notification channel added")
      setTarget("")
      setSecret("")
      if (created?.secret && type === "webhook") {
        setRevealedSecret({ id: created.id, secret: created.secret })
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add channel")
    } finally {
      setCreating(false)
    }
  }, [canCreate, create, type, target, secret, events])

  const handleDelete = useCallback(async (ch: NotificationChannel) => {
    if (!window.confirm(`Delete this ${ch.type} channel?`)) return
    setDeletingId(ch.id)
    try {
      await remove(ch.id)
      if (revealedSecret?.id === ch.id) setRevealedSecret(null)
      toast.success("Channel deleted")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to delete channel")
    } finally {
      setDeletingId(null)
    }
  }, [remove, revealedSecret])

  const handleTest = useCallback(async (ch: NotificationChannel) => {
    setTestingId(ch.id)
    try {
      await sendTest(ch.id)
      toast.success("Test notification sent", {
        description: ch.type === "email" ? `Delivered to ${ch.to}` : `POSTed to ${ch.url}`,
      })
    } catch (e) {
      toast.error("Test send failed", {
        description: e instanceof Error ? e.message : undefined,
      })
    } finally {
      setTestingId(null)
    }
  }, [sendTest])

  if (loading && channels.length === 0) {
    return (
      <div className="space-y-5">
        <Skeleton className="h-[180px] rounded-xl" />
        <Skeleton className="h-[120px] rounded-xl" />
      </div>
    )
  }

  return (
    <div className="space-y-5">
      {/* ── Add channel ── */}
      <SettingsCard
        title="Add channel"
        description="Deliver run completion/failure events by email or signed webhook"
      >
        <SettingsRow label="Type" description="How the notification is delivered">
          <Select value={type} onValueChange={(v) => { setType(v as "email" | "webhook"); setTarget("") }}>
            <SelectTrigger className="w-[200px] h-7 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="webhook" className="text-xs">
                <span className="flex items-center gap-2"><Globe className="size-3" /> Webhook (signed)</span>
              </SelectItem>
              <SelectItem value="email" className="text-xs">
                <span className="flex items-center gap-2"><Mail className="size-3" /> Email</span>
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>

        <SettingsRow
          label={type === "webhook" ? "URL" : "Recipient"}
          description={type === "webhook" ? "HTTPS endpoint that receives the signed POST" : "Email address to notify"}
        >
          <Input
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder={type === "webhook" ? "https://example.com/hooks/crewship" : "ops@example.com"}
            type={type === "email" ? "email" : "url"}
            className="w-[280px] h-7 text-xs"
          />
        </SettingsRow>

        {type === "webhook" && (
          <SettingsRow label="Signing secret" description="Optional — auto-generated and revealed once when blank">
            <Input
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              placeholder="(auto-generate)"
              type="password"
              autoComplete="off"
              className="w-[280px] h-7 text-xs"
            />
          </SettingsRow>
        )}

        <SettingsRow label="Events" description="Which run outcomes trigger delivery">
          <Select value={events} onValueChange={(v) => setEvents(v as typeof events)}>
            <SelectTrigger className="w-[200px] h-7 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="failed" className="text-xs">Failures only</SelectItem>
              <SelectItem value="completed" className="text-xs">Completions only</SelectItem>
              <SelectItem value="all" className="text-xs">All terminal events</SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>

        <div className="flex items-center justify-end px-4 py-2.5">
          <Button
            type="button"
            size="sm"
            className="h-7 px-2.5 text-xs"
            disabled={!canCreate}
            onClick={handleCreate}
          >
            {creating ? <Spinner className="mr-1.5 size-3" /> : <Plus className="mr-1.5 size-3" />}
            Add channel
          </Button>
        </div>
      </SettingsCard>

      {/* ── One-time secret reveal ── */}
      {revealedSecret && (
        <div className="rounded-xl border border-amber-500/40 bg-amber-500/[0.04] px-4 py-3 space-y-1.5">
          <div className="text-xs font-medium text-foreground/90">
            Webhook signing secret — shown only once
          </div>
          <div className="flex items-center gap-2">
            <code className="text-[11px] font-mono bg-muted/60 rounded px-1.5 py-0.5 break-all">
              {revealedSecret.secret}
            </code>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              className="h-6 w-6 shrink-0"
              aria-label="Copy signing secret"
              onClick={() => {
                navigator.clipboard?.writeText(revealedSecret.secret)
                toast.success("Secret copied")
              }}
            >
              <Copy className="size-3" />
            </Button>
          </div>
          <p className="text-[11px] text-muted-foreground">
            Configure your receiver to verify the <code>X-Crewship-Signature</code> HMAC with this
            secret, then dismiss.{" "}
            <button
              type="button"
              className="underline hover:text-foreground"
              onClick={() => setRevealedSecret(null)}
            >
              Dismiss
            </button>
          </p>
        </div>
      )}

      {/* ── Channels list ── */}
      <SettingsCard
        title="Channels"
        description={
          channels.length === 0
            ? "No channels yet"
            : `${channels.length} channel${channels.length === 1 ? "" : "s"}`
        }
      >
        {error ? (
          <SettingsEmpty>Failed to load channels ({error})</SettingsEmpty>
        ) : channels.length === 0 ? (
          <div className="flex flex-col items-center justify-center py-10 text-center">
            <div className="w-8 h-8 rounded-lg bg-muted/50 flex items-center justify-center mb-2">
              <BellOff className="h-3.5 w-3.5 text-muted-foreground" />
            </div>
            <div className="text-xs font-medium text-foreground/80">No notification channels</div>
            <div className="text-[11px] text-muted-foreground mt-0.5 max-w-xs">
              Add an email or webhook target to get notified when routine runs finish or fail.
            </div>
          </div>
        ) : (
          channels.map((ch, i) => {
            const isLast = i === channels.length - 1
            const isDeleting = deletingId === ch.id
            const isTesting = testingId === ch.id
            const eventsLabel =
              ch.events.length === 0 ? "failures" : ch.events.join(", ")
            return (
              <div
                key={ch.id}
                className={cn(
                  "flex items-center justify-between gap-4 px-4 py-2.5",
                  !isLast && "border-b border-border/40",
                )}
              >
                <div className="flex items-center gap-2 text-xs text-foreground min-w-0">
                  {ch.type === "email" ? (
                    <Mail className="size-3 text-muted-foreground shrink-0" />
                  ) : (
                    <Globe className="size-3 text-muted-foreground shrink-0" />
                  )}
                  <span className="truncate font-mono text-[11px]">
                    {ch.type === "email" ? ch.to : ch.url}
                  </span>
                  <span className="text-[10px] text-muted-foreground shrink-0">
                    · {eventsLabel}
                  </span>
                  {!ch.enabled && (
                    <span className="text-[10px] text-muted-foreground/70 shrink-0">(disabled)</span>
                  )}
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    disabled={isTesting}
                    onClick={() => handleTest(ch)}
                    className="h-6 px-2 text-[11px] text-muted-foreground hover:text-foreground"
                  >
                    {isTesting ? <Spinner className="mr-1 size-3" /> : <Send className="mr-1 size-3" />}
                    Test
                  </Button>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    disabled={isDeleting}
                    onClick={() => handleDelete(ch)}
                    aria-label={`Delete ${ch.type} channel`}
                    className="h-6 w-6 text-muted-foreground hover:text-destructive hover:bg-destructive/10"
                  >
                    {isDeleting ? <Spinner className="size-3" /> : <Trash2 className="size-3" />}
                  </Button>
                </div>
              </div>
            )
          })
        )}
      </SettingsCard>

      <p className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
        <Bell className="size-3" />
        Channels fire on routine-run terminal events. Adding and deleting requires the MANAGER role
        or higher.
      </p>
    </div>
  )
}
