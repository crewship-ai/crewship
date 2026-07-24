"use client"

import { useCallback, useState } from "react"
import { Bell, BellOff, Copy, Globe, Mail, MessageSquare, Plus, Send, Trash2, User } from "lucide-react"
import { toast } from "sonner"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Checkbox } from "@/components/ui/checkbox"
import { Switch } from "@/components/ui/switch"
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

// The 9 #1412 notification categories, in the fixed order the backend
// (internal/notify.AllCategories) declares them.
const NOTIFY_CATEGORIES = [
  "approvals", "escalations", "runs.failed", "runs.completed",
  "chat.replies", "security", "budget", "system", "memory",
] as const

type ChannelType = "email" | "webhook" | "shoutrrr"

/**
 * Settings → Notifications: CRUD + test over the workspace's outbound
 * delivery targets — email / signed webhook (run-terminal broadcast,
 * issue #850) and email / webhook / Slack / Discord / Telegram serving
 * the per-user category preference matrix (issue #1412). A channel here
 * is either WORKSPACE-scoped (this ADMIN/OWNER-managed list) or the
 * caller's OWN personal channel (self-service, any role).
 */
export function NotificationChannelsSection({ workspaceId }: NotificationChannelsSectionProps) {
  const { channels, loading, error, create, remove, sendTest, patch } =
    useNotificationChannels(workspaceId)

  // form state
  const [type, setType] = useState<ChannelType>("webhook")
  const [provider, setProvider] = useState<"slack" | "discord" | "telegram">("slack")
  const [target, setTarget] = useState("")
  const [secret, setSecret] = useState("")
  const [events, setEvents] = useState<"failed" | "completed" | "all">("failed")
  const [personal, setPersonal] = useState(false)
  const [categories, setCategories] = useState<string[]>([]) // empty = every category
  const [minPriority, setMinPriority] = useState<"low" | "medium" | "high" | "urgent">("low")
  const [creating, setCreating] = useState(false)
  const [deletingId, setDeletingId] = useState<string | null>(null)
  const [testingId, setTestingId] = useState<string | null>(null)
  // Webhook signing secret / shoutrrr service url revealed exactly once on create.
  const [revealedSecret, setRevealedSecret] = useState<{ id: string; secret: string; type: ChannelType } | null>(null)

  const canCreate = target.trim() !== "" && !creating

  const toggleCategory = useCallback((cat: string) => {
    setCategories((prev) => (prev.includes(cat) ? prev.filter((c) => c !== cat) : [...prev, cat]))
  }, [])

  const handleCreate = useCallback(async () => {
    if (!canCreate) return
    setCreating(true)
    try {
      const created = await create({
        type,
        ...(type === "webhook"
          ? { url: target.trim(), ...(secret.trim() ? { secret: secret.trim() } : {}) }
          : type === "shoutrrr"
            ? { provider, shoutrrr_url: target.trim() }
            : { to: target.trim() }),
        events: [events],
        personal,
        ...(categories.length > 0 ? { categories } : {}),
        ...(minPriority !== "low" ? { min_priority: minPriority } : {}),
      })
      toast.success("Notification channel added")
      setTarget("")
      setSecret("")
      if (created?.secret) {
        setRevealedSecret({ id: created.id, secret: created.secret, type })
      }
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to add channel")
    } finally {
      setCreating(false)
    }
  }, [canCreate, create, type, target, secret, events, provider, personal, categories, minPriority])

  const handleTogglePersonal = useCallback((next: boolean) => {
    setPersonal(next)
    if (next) setCategories([]) // admin allowlist is meaningless on a personal channel
  }, [])

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
        description:
          ch.type === "email" ? `Delivered to ${ch.to}`
            : ch.type === "shoutrrr" ? `Sent via ${ch.provider}`
              : `POSTed to ${ch.url}`,
      })
    } catch (e) {
      toast.error("Test send failed", {
        description: e instanceof Error ? e.message : undefined,
      })
    } finally {
      setTestingId(null)
    }
  }, [sendTest])

  const [togglingId, setTogglingId] = useState<string | null>(null)
  const handleToggleEnabled = useCallback(async (ch: NotificationChannel) => {
    setTogglingId(ch.id)
    try {
      await patch(ch.id, { enabled: !ch.enabled })
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to update channel")
    } finally {
      setTogglingId(null)
    }
  }, [patch])

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
        description="Deliver run outcomes and category notifications (approvals, escalations, chat replies, …) by email, webhook, Slack, Discord, or Telegram"
      >
        <SettingsRow label="Type" description="How the notification is delivered">
          <Select value={type} onValueChange={(v) => { setType(v as ChannelType); setTarget("") }}>
            <SelectTrigger className="w-[220px] h-7 text-xs">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="webhook" className="text-xs">
                <span className="flex items-center gap-2"><Globe className="size-3" /> Webhook (signed)</span>
              </SelectItem>
              <SelectItem value="email" className="text-xs">
                <span className="flex items-center gap-2"><Mail className="size-3" /> Email</span>
              </SelectItem>
              <SelectItem value="shoutrrr" className="text-xs">
                <span className="flex items-center gap-2"><MessageSquare className="size-3" /> Slack / Discord / Telegram</span>
              </SelectItem>
            </SelectContent>
          </Select>
        </SettingsRow>

        {type === "shoutrrr" && (
          <SettingsRow label="Provider" description="Which shoutrrr service">
            <Select value={provider} onValueChange={(v) => setProvider(v as typeof provider)}>
              <SelectTrigger className="w-[220px] h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="slack" className="text-xs">Slack</SelectItem>
                <SelectItem value="discord" className="text-xs">Discord</SelectItem>
                <SelectItem value="telegram" className="text-xs">Telegram</SelectItem>
              </SelectContent>
            </Select>
          </SettingsRow>
        )}

        <SettingsRow
          label={type === "webhook" ? "URL" : type === "shoutrrr" ? "Service URL" : "Recipient"}
          description={
            type === "webhook"
              ? "HTTPS endpoint that receives the signed POST"
              : type === "shoutrrr"
                ? `Apprise-style ${provider} service URL, e.g. ${
                    provider === "slack" ? "slack://hook:TOKEN@webhook"
                      : provider === "discord" ? "discord://token@channel"
                        : "telegram://token@telegram?chats=@you"
                  }`
                : "Email address to notify"
          }
        >
          <Input
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            placeholder={
              type === "webhook" ? "https://example.com/hooks/crewship"
                : type === "shoutrrr" ? `${provider}://…`
                  : "ops@example.com"
            }
            type={type === "email" ? "email" : type === "webhook" ? "url" : "text"}
            className="w-[320px] h-7 text-xs"
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

        <SettingsRow label="Events (legacy)" description="Run outcomes that trigger the workspace-wide broadcast (issue #850)">
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

        <SettingsRow label="Personal channel" description="Owned by you only — self-service, usable in your own preference matrix">
          <label className="flex items-center gap-2 cursor-pointer">
            <Checkbox checked={personal} onCheckedChange={(c) => handleTogglePersonal(c === true)} />
            <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
              <User className="size-3" /> Personal (any role can add their own)
            </span>
          </label>
        </SettingsRow>

        {!personal && (
          <SettingsRow label="Categories" description="Admin allowlist for the preference matrix — leave empty for every category">
            <div className="flex flex-wrap gap-x-3 gap-y-1.5 max-w-[420px]">
              {NOTIFY_CATEGORIES.map((cat) => (
                <label key={cat} className="flex items-center gap-1.5 cursor-pointer">
                  <Checkbox checked={categories.includes(cat)} onCheckedChange={() => toggleCategory(cat)} />
                  <span className="text-[11px] text-muted-foreground">{cat}</span>
                </label>
              ))}
            </div>
          </SettingsRow>
        )}

        {!personal && (
          <SettingsRow label="Priority floor" description="Skip items below this priority on this channel">
            <Select value={minPriority} onValueChange={(v) => setMinPriority(v as typeof minPriority)}>
              <SelectTrigger className="w-[160px] h-7 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="low" className="text-xs">Low (no floor)</SelectItem>
                <SelectItem value="medium" className="text-xs">Medium</SelectItem>
                <SelectItem value="high" className="text-xs">High</SelectItem>
                <SelectItem value="urgent" className="text-xs">Urgent</SelectItem>
              </SelectContent>
            </Select>
          </SettingsRow>
        )}

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
            {revealedSecret.type === "shoutrrr" ? "Service URL" : "Webhook signing secret"} — shown only once
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
            {revealedSecret.type === "shoutrrr" ? (
              "Store this service URL somewhere safe — it can't be read back."
            ) : (
              <>Configure your receiver to verify the <code>X-Crewship-Signature</code> HMAC with this secret.</>
            )}{" "}
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
              Add an email, webhook, Slack, Discord, or Telegram target — for routine-run outcomes and/or
              your own category preference matrix.
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
                  ) : ch.type === "shoutrrr" ? (
                    <MessageSquare className="size-3 text-muted-foreground shrink-0" />
                  ) : (
                    <Globe className="size-3 text-muted-foreground shrink-0" />
                  )}
                  <span className="truncate font-mono text-[11px]">
                    {ch.type === "email" ? ch.to : ch.type === "shoutrrr" ? ch.provider : ch.url}
                  </span>
                  {ch.scope === "user" && (
                    <span className="flex items-center gap-0.5 text-[10px] text-muted-foreground shrink-0">
                      <User className="size-2.5" /> personal
                    </span>
                  )}
                  <span className="text-[10px] text-muted-foreground shrink-0">
                    · {ch.categories && ch.categories.length > 0 ? ch.categories.join(", ") : "all categories"}
                  </span>
                  <span className="text-[10px] text-muted-foreground/70 shrink-0">
                    · broadcast: {eventsLabel}
                  </span>
                  {!ch.enabled && (
                    <span className="text-[10px] text-muted-foreground/70 shrink-0">(disabled)</span>
                  )}
                </div>
                <div className="flex items-center gap-1 shrink-0">
                  <Switch
                    size="sm"
                    checked={ch.enabled}
                    disabled={togglingId === ch.id}
                    onCheckedChange={() => handleToggleEnabled(ch)}
                    aria-label={ch.enabled ? "Disable channel" : "Enable channel"}
                    className="mr-1"
                  />
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
        Workspace-scoped channels require the ADMIN or OWNER role. Personal channels are self-service —
        manage your own delivery preferences per category under{" "}
        <span className="font-medium text-foreground/70">Notification prefs</span>.
      </p>
    </div>
  )
}
